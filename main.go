package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"locator/modules/userinfo"
	"locator/modules/wifi"
)

const (
	defaultServerURL = "http://localhost:8080/api/locate" // по умолчанию для тестов
	timeoutSec       = 10
)

// WifiNetwork структура для одной точки доступа (такая же, как на сервере)
type WifiNetwork struct {
	BSSID          string `json:"bssid"`
	SignalStrength int    `json:"signal_strength"`
	Age            int    `json:"age"`
	Channel        int    `json:"channel"`
}

// ClientRequest структура запроса к вашему серверу
type ClientRequest struct {
	Hostname   string        `json:"hostname"`
	Username   string        `json:"username"`
	OriginalIP string        `json:"original_ip,omitempty"` // реальный IP клиента (если получен)
	Wifi       []WifiNetwork `json:"wifi,omitempty"`
}

// ServerResponse ожидаемый ответ от сервера (может содержать координаты или статус)
type ServerResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// При желании можно добавить поля location, если сервер их возвращает
}

// Logger для условного логирования
type Logger struct {
	quiet bool
}

func (l *Logger) Println(a ...interface{}) {
	if !l.quiet {
		fmt.Println(a...)
	}
}

func (l *Logger) Printf(format string, a ...interface{}) {
	if !l.quiet {
		fmt.Printf(format, a...)
	}
}

func (l *Logger) Errorln(a ...interface{}) {
	fmt.Fprintln(os.Stderr, a...)
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
}

var log Logger

func main() {
	// Определяем флаги
	serverURL := flag.String("server", defaultServerURL, "URL сервера для отправки данных (например, http://192.168.1.100:8080/api/locate)")
	quiet := flag.Bool("quiet", false, "тихий режим (вывод только ошибок)")
	flag.Parse()

	log = Logger{quiet: *quiet}

	if !log.quiet {
		fmt.Println("Клиент отправки геоданных на сервер")
		fmt.Println("=====================================")
	}

	// 1. Получаем hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		log.Errorf("Предупреждение: не удалось получить hostname: %v\n", err)
	}
	log.Printf("Hostname: %s\n", hostname)

	// 2. Получаем имя активного пользователя
	username := userinfo.GetActiveUsername()
	log.Printf("Активный пользователь: %s\n", username)

	// 3. Получаем реальный публичный IP (может быть пустым)
	realIP := getPublicIP()
	if realIP == "" {
		log.Errorln("Ошибка: не удалось получить публичный IP. Проверьте интернет-соединение.")
	} else {
		log.Printf("Реальный публичный IP: %s\n", realIP)
	}

	// 4. Сканируем Wi-Fi сети
	log.Println("\nСканируем Wi-Fi сети...")
	wifiNetworks := getWifiNetworks()
	log.Printf("Найдено уникальных Wi-Fi сетей: %d\n", len(wifiNetworks))

	// 5. Формируем запрос к серверу
	clientReq := ClientRequest{
		Hostname:   hostname,
		Username:   username,
		OriginalIP: realIP,
		Wifi:       wifiNetworks,
	}

	jsonData, err := json.MarshalIndent(clientReq, "", "  ")
	if err != nil {
		log.Errorf("Ошибка формирования JSON: %v\n", err)
		os.Exit(1)
	}
	log.Println("\nJSON для отправки на сервер:")
	log.Println(string(jsonData))

	// 6. Отправка запроса на сервер
	log.Printf("\nОтправляем данные на сервер %s...\n", *serverURL)
	statusCode, responseBody, err := sendToServer(*serverURL, jsonData)
	if err != nil {
		log.Errorf("Ошибка при отправке: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("HTTP Status: %d\n", statusCode)

	if statusCode == http.StatusOK {
		log.Println("Данные успешно отправлены на сервер")
	} else {
		log.Printf("Сервер вернул ошибку: %s\n", string(responseBody))
	}

	// 7. Если есть тело ответа, выводим его (не в quiet-режиме)
	if !log.quiet && len(responseBody) > 0 {
		fmt.Println("\nОтвет сервера:")
		fmt.Println(string(responseBody))
	}
}

// getPublicIP запрашивает внешний IP клиента
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

// getWifiNetworks получает список всех точек доступа через netsh (без изменений)
func getWifiNetworks() []WifiNetwork {
	log.Println("  Инициируем принудительное сканирование Wi-Fi через Native API...")

	_, err := wifi.ScanAndList()
	if err != nil {
		log.Printf("  Ошибка Native API: %v, используем запасной вариант\n", err)
	}

	log.Println("  Сканирование запущено, ожидаем завершения (3 секунды)...")
	time.Sleep(3 * time.Second)

	cmd := exec.Command("cmd", "/c", "chcp 437 > nul && netsh wlan show networks mode=bssid")

	output, err := cmd.Output()
	if err != nil {
		log.Printf("  Ошибка запуска netsh: %v\n", err)
		return nil
	}

	blocks := strings.Split(string(output), "\r\n\r\n")
	var networks []WifiNetwork

	for _, block := range blocks {
		if !strings.Contains(block, "BSSID") {
			continue
		}

		lines := strings.Split(block, "\r\n")
		var bssid, signalStr, channelStr string

		for _, line := range lines {
			line = strings.TrimSpace(line)

			if strings.Contains(line, "BSSID") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					bssid = strings.TrimSpace(parts[1])
					if matched, _ := regexp.MatchString(`([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}`, bssid); !matched {
						bssid = ""
					}
				}
				continue
			}

			if strings.Contains(line, "Сигнал") || strings.Contains(line, "Signal") {
				re := regexp.MustCompile(`(\d+)%`)
				if matches := re.FindStringSubmatch(line); matches != nil {
					signalStr = matches[1]
				}
				continue
			}

			if strings.Contains(line, "Канал") || strings.Contains(line, "Channel") {
				re := regexp.MustCompile(`(\d+)`)
				if matches := re.FindStringSubmatch(line); matches != nil {
					channelStr = matches[1]
				}
				continue
			}
		}

		if bssid != "" && signalStr != "" && channelStr != "" {
			signal, _ := strconv.Atoi(signalStr)
			channel, _ := strconv.Atoi(channelStr)
			signalDBm := (signal * 70 / 100) - 100
			networks = append(networks, WifiNetwork{
				BSSID:          bssid,
				Channel:        channel,
				SignalStrength: signalDBm,
				Age:            0,
			})
		}
	}

	seen := make(map[string]bool)
	var unique []WifiNetwork
	for _, net := range networks {
		if !seen[net.BSSID] {
			seen[net.BSSID] = true
			unique = append(unique, net)
		}
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i].SignalStrength > unique[j].SignalStrength
	})

	return unique
}

// sendToServer отправляет POST-запрос на указанный URL
func sendToServer(url string, jsonData []byte) (int, []byte, error) {
	client := http.Client{Timeout: timeoutSec * time.Second}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}
