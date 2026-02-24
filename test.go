package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	apiURL     = "https://locator.api.maps.yandex.ru/v1/locate"
	timeoutSec = 10
)

var yandexAPIKey string

// IPAddress представляет блок IP в JSON
type IPAddress struct {
	Address string `json:"address"`
}

// WifiNetwork представляет одну точку доступа
type WifiNetwork struct {
	BSSID           string `json:"bssid"`
	SignalStrength  int    `json:"signal_strength"`
	Age             int    `json:"age"`
	Channel         int    `json:"channel"`
}

// RequestBody полный запрос к API
type RequestBody struct {
	IP   []IPAddress    `json:"ip"`
	Wifi []WifiNetwork `json:"wifi,omitempty"`
}

// YandexResponse обновленная структура ответа от API
type YandexResponse struct {
	Location struct {
		Point struct {
			Lon float64 `json:"lon"`
			Lat float64 `json:"lat"`
		} `json:"point"`
		Accuracy float64 `json:"accuracy"`
	} `json:"location"`
	Raw map[string]interface{} `json:"-"` // для хранения полного ответа
}

// LocationResponse структура для сохранения в файл
type LocationResponse struct {
	Timestamp string `json:"timestamp"`
	Hostname  string `json:"hostname"`
	Location  struct {
		Point struct {
			Lon float64 `json:"lon"`
			Lat float64 `json:"lat"`
		} `json:"point"`
		Accuracy float64 `json:"accuracy"`
	} `json:"location"`
}

func init() {
	// Загружаем .env файл
	_ = godotenv.Load()
	yandexAPIKey = os.Getenv("YANDEX_API_KEY")
	if yandexAPIKey == "" {
		fmt.Println("Ошибка: не задан YANDEX_API_KEY в .env файле или переменных окружения.")
		fmt.Println("Создайте файл .env с содержимым: YANDEX_API_KEY=ваш_ключ")
		os.Exit(1)
	}
}

func main() {
	fmt.Println("Утилита определения геолокации")
	fmt.Println("===============================")

	// 1. Получаем hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		fmt.Printf("Предупреждение: не удалось получить hostname: %v\n", err)
	}
	fmt.Printf("Hostname: %s\n", hostname)

	// 2. Публичный IP
	publicIP := getPublicIP()
	if publicIP == "" {
		fmt.Println("Ошибка: не удалось получить публичный IP. Проверьте интернет-соединение.")
	} else {
		fmt.Printf("Публичный IP: %s\n", publicIP)
	}

	// 3. Wi-Fi сети
	fmt.Println("\nСканируем Wi-Fi сети...")
	wifiNetworks := getWifiNetworks()
	fmt.Printf("Найдено уникальных Wi-Fi сетей: %d\n", len(wifiNetworks))

	// 4. Проверка наличия данных
	if len(wifiNetworks) == 0 && publicIP == "" {
		fmt.Println("\nОшибка: нет данных для отправки.")
		os.Exit(1)
	}

	// 5. Формирование JSON
	requestBody := RequestBody{
		IP: []IPAddress{{Address: publicIP}},
	}
	if len(wifiNetworks) > 0 {
		requestBody.Wifi = wifiNetworks
	}

	jsonData, err := json.MarshalIndent(requestBody, "", "  ")
	if err != nil {
		fmt.Printf("Ошибка формирования JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nJSON для отправки:")
	fmt.Println(string(jsonData))

	// 6. Отправка запроса
	fmt.Println("\nОтправляем запрос на Яндекс API...")
	resp, rawResponse, err := sendRequest(jsonData)
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Запрос успешно отправлен! (HTTP 200)")

	// 7. Вывод полного ответа сервера
	fmt.Println("\nПолный ответ сервера:")
	fmt.Println(string(rawResponse))

	// 8. Вывод координат в нужном формате
	if resp != nil && resp.Location.Point.Lat != 0 && resp.Location.Point.Lon != 0 {
		fmt.Printf("\nКоординаты: %.6f, %.6f\n", resp.Location.Point.Lat, resp.Location.Point.Lon)
		fmt.Printf("Ссылка на карту: https://maps.yandex.ru/?ll=%.6f,%.6f&z=17\n", 
			resp.Location.Point.Lon, resp.Location.Point.Lat)
		
		// Вывод точности, если она есть
		if resp.Location.Accuracy > 0 {
			fmt.Printf("Точность: %.1f метров\n", resp.Location.Accuracy)
		}
	} else {
		fmt.Println("\nПредупреждение: координаты не найдены в ответе сервера.")
	}

	// 9. Сохранение результата в файл
	if resp != nil {
		err = saveResult(hostname, resp)
		if err != nil {
			fmt.Printf("Ошибка сохранения результата: %v\n", err)
		} else {
			fmt.Println("Результат сохранен в output.json")
		}
	}

	// 10. Статистика
	printStats(wifiNetworks)
}

// getPublicIP запрашивает внешний IP
func getPublicIP() string {
	client := http.Client{Timeout: timeoutSec * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ip))
}

// getWifiNetworks получает список всех точек доступа через netsh
func getWifiNetworks() []WifiNetwork {
	fmt.Println("  Используем netsh для сканирования...")
	
	// Запускаем netsh
	cmd := exec.Command("netsh", "wlan", "show", "networks", "mode=bssid")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("  Ошибка запуска netsh: %v\n", err)
		return nil
	}

	// Парсим вывод netsh
	lines := strings.Split(string(output), "\r\n")
	var networks []WifiNetwork
	var currentBSSID string
	var currentChannel int
	var currentSignal int
	
	// Регулярные выражения для поиска
	bssidRegex := regexp.MustCompile(`BSSID\s+\d+\s+:\s*([0-9A-Fa-f:]{17})`)
	signalRegex := regexp.MustCompile(`Сигнал\s+:\s*(\d+)%`)
	channelRegex := regexp.MustCompile(`Канал\s+:\s*(\d+)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// Поиск BSSID
		if matches := bssidRegex.FindStringSubmatch(line); matches != nil {
			// Сохраняем предыдущую точку, если она есть
			if currentBSSID != "" && currentChannel != 0 && currentSignal != 0 {
				networks = append(networks, WifiNetwork{
					BSSID:           currentBSSID,
					Channel:         currentChannel,
					SignalStrength:  currentSignal,
					Age:             0,
				})
			}
			// Начинаем новую точку
			currentBSSID = matches[1]
			currentChannel = 0
			currentSignal = 0
			continue
		}
		
		// Поиск сигнала
		if matches := signalRegex.FindStringSubmatch(line); matches != nil && currentBSSID != "" {
			fmt.Sscanf(matches[1], "%d", &currentSignal)
			// Конвертируем процент в dBm
			currentSignal = (currentSignal * 70 / 100) - 100
			continue
		}
		
		// Поиск канала
		if matches := channelRegex.FindStringSubmatch(line); matches != nil && currentBSSID != "" {
			fmt.Sscanf(matches[1], "%d", &currentChannel)
			continue
		}
	}

	// Добавляем последнюю найденную точку
	if currentBSSID != "" && currentChannel != 0 && currentSignal != 0 {
		networks = append(networks, WifiNetwork{
			BSSID:           currentBSSID,
			Channel:         currentChannel,
			SignalStrength:  currentSignal,
			Age:             0,
		})
	}

	// Удаляем дубликаты по BSSID
	seen := make(map[string]bool)
	var unique []WifiNetwork
	for _, net := range networks {
		if !seen[net.BSSID] {
			seen[net.BSSID] = true
			unique = append(unique, net)
		}
	}

	// Сортируем по силе сигнала (от сильного к слабому)
	sort.Slice(unique, func(i, j int) bool {
		return unique[i].SignalStrength > unique[j].SignalStrength
	})

	return unique
}

// sendRequest выполняет POST запрос и возвращает как структуру, так и сырой ответ
func sendRequest(jsonData []byte) (*YandexResponse, []byte, error) {
	fullURL := fmt.Sprintf("%s?apikey=%s", apiURL, yandexAPIKey)
	client := http.Client{Timeout: timeoutSec * time.Second}
	req, err := http.NewRequest("POST", fullURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Парсим ответ в структуру
	var response YandexResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		// Если не удалось распарсить в структуру, всё равно возвращаем сырой ответ
		return nil, body, nil
	}

	return &response, body, nil
}

// saveResult сохраняет результат в файл output.json
func saveResult(hostname string, yandexResp *YandexResponse) error {
	// Создаем структуру для сохранения
	var locationResp LocationResponse
	
	// Устанавливаем timestamp в формате ISO 8601
	locationResp.Timestamp = time.Now().Format(time.RFC3339)
	
	// Устанавливаем hostname
	locationResp.Hostname = hostname
	
	// Устанавливаем координаты
	locationResp.Location.Point.Lon = yandexResp.Location.Point.Lon
	locationResp.Location.Point.Lat = yandexResp.Location.Point.Lat
	
	// Устанавливаем точность
	locationResp.Location.Accuracy = yandexResp.Location.Accuracy

	// Конвертируем в JSON с отступами
	jsonData, err := json.MarshalIndent(locationResp, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга JSON: %v", err)
	}

	// Сохраняем в файл
	err = os.WriteFile("output.json", jsonData, 0644)
	if err != nil {
		return fmt.Errorf("ошибка записи файла: %v", err)
	}

	// Для отладки выводим сохраненный JSON
	fmt.Println("\nСохраненный JSON:")
	fmt.Println(string(jsonData))

	return nil
}

// printStats выводит статистику
func printStats(networks []WifiNetwork) {
	if len(networks) == 0 {
		return
	}
	fmt.Println("\nСтатистика:")
	fmt.Printf("  Отправлено уникальных Wi-Fi сетей: %d\n", len(networks))

	// Диапазон сигналов
	minSig, maxSig := networks[0].SignalStrength, networks[0].SignalStrength
	chMap := make(map[int]bool)
	for _, n := range networks {
		if n.SignalStrength < minSig {
			minSig = n.SignalStrength
		}
		if n.SignalStrength > maxSig {
			maxSig = n.SignalStrength
		}
		chMap[n.Channel] = true
	}
	fmt.Printf("  Диапазон сигналов: от %d dBm до %d dBm\n", minSig, maxSig)

	// Список каналов
	channels := make([]int, 0, len(chMap))
	for ch := range chMap {
		channels = append(channels, ch)
	}
	sort.Ints(channels)
	fmt.Printf("  Каналы: %v\n", channels)
}