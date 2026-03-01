package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	yandexAPIURL  = "https://locator.api.maps.yandex.ru/v1/locate"
	opensearchURL = "https://admin:password@opensearch:9200" // адрес OpenSearch
	indexName     = "locations"
)

var yandexAPIKey string

// RequestFromClient структура входящего запроса от клиента
type ClientRequest struct {
	Hostname   string        `json:"hostname"`
	Username   string        `json:"username"`
	OriginalIP string        `json:"original_ip"`
	IP         string        `json:"ip"` // IP для отправки в Яндекс (может быть пустым)
	Wifi       []WifiNetwork `json:"wifi"`
}

// WifiNetwork такая же как в клиенте
type WifiNetwork struct {
	BSSID          string `json:"bssid"`
	SignalStrength int    `json:"signal_strength"`
	Age            int    `json:"age"`
	Channel        int    `json:"channel"`
}

// YandexRequest тело запроса к Яндексу
type YandexRequest struct {
	IP []struct {
		Address string `json:"address"`
	} `json:"ip,omitempty"`
	Wifi []WifiNetwork `json:"wifi,omitempty"`
}

// YandexResponse ответ от Яндекса
type YandexResponse struct {
	Location struct {
		Point struct {
			Lon float64 `json:"lon"`
			Lat float64 `json:"lat"`
		} `json:"point"`
		Accuracy float64 `json:"accuracy"`
	} `json:"location"`
}

// Document для сохранения в OpenSearch
type OpenSearchDocument struct {
	Timestamp  time.Time `json:"timestamp"`
	Hostname   string    `json:"hostname"`
	Username   string    `json:"username"`
	OriginalIP string    `json:"original_ip"`
	Location   struct {
		Point struct {
			Lon float64 `json:"lon"`
			Lat float64 `json:"lat"`
		} `json:"point"`
		Accuracy float64 `json:"accuracy"`
	} `json:"location"`
}

// createHTTPClient возвращает HTTP-клиент с возможностью отключить проверку сертификата
func createHTTPClient(insecureSkipVerify bool) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureSkipVerify,
		},
	}
	return &http.Client{Transport: tr}
}

func main() {
	yandexAPIKey = os.Getenv("YANDEX_API_KEY")
	if yandexAPIKey == "" {
		log.Fatal("YANDEX_API_KEY not set")
	}

	http.HandleFunc("/api/locate", handleLocate)
	log.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Читаем тело запроса
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Cannot read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var clientReq ClientRequest
	if err := json.Unmarshal(body, &clientReq); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Логируем полученные данные
	log.Printf("Received from %s: host=%s, user=%s, ip=%s, wifi=%d",
		r.RemoteAddr, clientReq.Hostname, clientReq.Username, clientReq.OriginalIP, len(clientReq.Wifi))

	// Формируем запрос к Яндексу
	yandexReq := YandexRequest{
		Wifi: clientReq.Wifi,
	}
	if clientReq.IP != "" {
		yandexReq.IP = []struct {
			Address string `json:"address"`
		}{{Address: clientReq.IP}}
	}

	yandexBody, err := json.Marshal(yandexReq)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		log.Printf("Marshal error: %v", err)
		return
	}

	// Отправляем запрос к Яндексу (используем стандартный клиент с проверкой)
	yandexResp, yandexStatusCode, err := callYandexAPI(yandexBody)
	if err != nil {
		http.Error(w, "Yandex API error", http.StatusBadGateway)
		log.Printf("Yandex API call error: %v", err)
		return
	}
	if yandexStatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Yandex API returned %d", yandexStatusCode), http.StatusBadGateway)
		log.Printf("Yandex API returned status %d", yandexStatusCode)
		return
	}

	// Сохраняем в OpenSearch (асинхронно)
	go saveToOpenSearch(clientReq, yandexResp)

	// Отправляем ответ клиенту
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(yandexResp)
}

func callYandexAPI(reqBody []byte) (*YandexResponse, int, error) {
	url := fmt.Sprintf("%s?apikey=%s", yandexAPIURL, yandexAPIKey)
	// Используем стандартный клиент (проверка сертификата включена)
	resp, err := http.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}

	var yandexResp YandexResponse
	if err := json.Unmarshal(body, &yandexResp); err != nil {
		return nil, resp.StatusCode, err
	}
	return &yandexResp, resp.StatusCode, nil
}

func saveToOpenSearch(clientReq ClientRequest, yandexResp *YandexResponse) {
	doc := OpenSearchDocument{
		Timestamp:  time.Now(),
		Hostname:   clientReq.Hostname,
		Username:   clientReq.Username,
		OriginalIP: clientReq.OriginalIP,
	}
	doc.Location.Point.Lon = yandexResp.Location.Point.Lon
	doc.Location.Point.Lat = yandexResp.Location.Point.Lat
	doc.Location.Accuracy = yandexResp.Location.Accuracy

	docBody, err := json.Marshal(doc)
	if err != nil {
		log.Printf("Failed to marshal OpenSearch document: %v", err)
		return
	}

	// Создаём клиент с отключённой проверкой имени хоста (InsecureSkipVerify = true)
	client := createHTTPClient(true)

	url := fmt.Sprintf("%s/%s/_doc", opensearchURL, indexName)
	resp, err := client.Post(url, "application/json", bytes.NewReader(docBody))
	if err != nil {
		log.Printf("Failed to save to OpenSearch: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("OpenSearch returned error: %s", body)
	} else {
		log.Printf("Saved to OpenSearch, id: %s", resp.Header.Get("Location"))
	}
}
