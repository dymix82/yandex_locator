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
	"github.com/joho/godotenv"

    "locator/modules/userinfo"
	"locator/modules/wifi"
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
	IP   []IPAddress    `json:"ip,omitempty"`  // omitempty – поле не будет добавлено, если пустое
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
	Raw map[string]interface{} `json:"-"`
}

// LocationResponse структура для сохранения в файл
type LocationResponse struct {
	Timestamp  string `json:"timestamp"`
	Hostname   string `json:"hostname"`
	Username   string `json:"username"`
	OriginalIP string `json:"original_ip,omitempty"` // реальный IP (если был получен)
	Location   struct {
		Point struct {
			Lon float64 `json:"lon"`
			Lat float64 `json:"lat"`
		} `json:"point"`
		Accuracy float64 `json:"accuracy"`
	} `json:"location"`
}

// Logger структура для условного логирования
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

func init() {
	_ = godotenv.Load()
	yandexAPIKey = os.Getenv("YANDEX_API_KEY")
	if yandexAPIKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: не задан YANDEX_API_KEY в .env файле или переменных окружения.")
		fmt.Fprintln(os.Stderr, "Создайте файл .env с содержимым: YANDEX_API_KEY=ваш_ключ")
		os.Exit(1)
	}
}

func main() {
	quiet := flag.Bool("quiet", false, "тихий режим (вывод только ошибок и кода ответа)")
	flag.Parse()

	log = Logger{quiet: *quiet}

	if !log.quiet {
		fmt.Println("Утилита определения геолокации")
		fmt.Println("===============================")
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		log.Errorf("Предупреждение: не удалось получить hostname: %v\n", err)
	}
	log.Printf("Hostname: %s\n", hostname)

	// Получаем реальный публичный IP (может быть пустым)
	realIP := getPublicIP()
	if realIP == "" {
		log.Errorln("Ошибка: не удалось получить публичный IP. Проверьте интернет-соединение.")
	} else {
		log.Printf("Реальный публичный IP: %s\n", realIP)
	}

	// Получаем имя активного пользователя
	username := userinfo.GetActiveUsername()
	log.Printf("Активный пользователь: %s\n", username)

	// Определяем, нужно ли скрыть IP в запросе к API
	hideIP := isLocalNetwork(realIP)
	if hideIP {
		log.Println("IP принадлежит локальной подсети 80.237.17.0/24 – поле IP не будет отправлено в API")
	}

	// Сканируем Wi-Fi сети
	log.Println("\nСканируем Wi-Fi сети...")
	wifiNetworks := getWifiNetworks()
	log.Printf("Найдено уникальных Wi-Fi сетей: %d\n", len(wifiNetworks))

	// Формируем тело запроса
	requestBody := RequestBody{
		Wifi: wifiNetworks,
	}
	// Добавляем IP только если он не пустой и не подлежит скрытию
	if realIP != "" && !hideIP {
		requestBody.IP = []IPAddress{{Address: realIP}}
	}

	jsonData, err := json.MarshalIndent(requestBody, "", "  ")
	if err != nil {
		log.Errorf("Ошибка формирования JSON: %v\n", err)
		os.Exit(1)
	}
	log.Println("\nJSON для отправки:")
	log.Println(string(jsonData))

	// Отправка запроса (даже если IP скрыт, Wi-Fi сети могут быть)
	log.Println("\nОтправляем запрос на Яндекс API...")
	resp, rawResponse, statusCode, err := sendRequest(jsonData)
	if err != nil {
		log.Errorf("Ошибка: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("HTTP Status: %d\n", statusCode)

	if !log.quiet {
		fmt.Println("Запрос успешно отправлен! (HTTP 200)")
	}

	if !log.quiet {
		fmt.Println("\nПолный ответ сервера:")
		fmt.Println(string(rawResponse))
	}

	if resp != nil && resp.Location.Point.Lat != 0 && resp.Location.Point.Lon != 0 {
		log.Printf("\nКоординаты: %.6f, %.6f\n", resp.Location.Point.Lat, resp.Location.Point.Lon)
		log.Printf("Ссылка на карту: https://maps.yandex.ru/?ll=%.6f,%.6f&z=17\n",
			resp.Location.Point.Lon, resp.Location.Point.Lat)

		if resp.Location.Accuracy > 0 {
			log.Printf("Точность: %.1f метров\n", resp.Location.Accuracy)
		}
	} else {
		log.Println("\nПредупреждение: координаты не найдены в ответе сервера.")
	}

	// Сохраняем результат в файл (с оригинальным IP, даже если он скрыт в запросе)
	if resp != nil {
		err = saveResult(hostname, username, realIP, resp)
		if err != nil {
			log.Errorf("Ошибка сохранения результата: %v\n", err)
		} else {
			log.Println("Результат сохранен в output.json")
		}
	}

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

// isLocalNetwork проверяет, принадлежит ли IP подсети 80.237.17.0/24
func isLocalNetwork(ip string) bool {
	if ip == "" {
		return false
	}
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	return parts[0] == "80" && parts[1] == "237" && parts[2] == "17"
}


// getWifiNetworks получает список всех точек доступа через netsh
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
	// fmt.Println("=== RAW NETSH OUTPUT ===")
	// fmt.Println(string(output))
	// fmt.Println("=== END RAW OUTPUT ===")
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

// sendRequest выполняет POST запрос
func sendRequest(jsonData []byte) (*YandexResponse, []byte, int, error) {
	fullURL := fmt.Sprintf("%s?apikey=%s", apiURL, yandexAPIKey)
	client := http.Client{Timeout: timeoutSec * time.Second}
	req, err := http.NewRequest("POST", fullURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, resp.StatusCode, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, body, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var response YandexResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, body, resp.StatusCode, nil
	}

	return &response, body, resp.StatusCode, nil
}

// saveResult сохраняет результат в файл output.json
func saveResult(hostname, username, originalIP string, yandexResp *YandexResponse) error {
	var locationResp LocationResponse

	locationResp.Timestamp = time.Now().Format(time.RFC3339)
	locationResp.Hostname = hostname
	locationResp.Username = username
	if originalIP != "" {
		locationResp.OriginalIP = originalIP
	}
	locationResp.Location.Point.Lon = yandexResp.Location.Point.Lon
	locationResp.Location.Point.Lat = yandexResp.Location.Point.Lat
	locationResp.Location.Accuracy = yandexResp.Location.Accuracy

	jsonData, err := json.MarshalIndent(locationResp, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга JSON: %v", err)
	}

	err = os.WriteFile("output.json", jsonData, 0644)
	if err != nil {
		return fmt.Errorf("ошибка записи файла: %v", err)
	}

	log.Println("\nСохраненный JSON:")
	log.Println(string(jsonData))

	return nil
}

// printStats выводит статистику
func printStats(networks []WifiNetwork) {
	if len(networks) == 0 || log.quiet {
		return
	}
	fmt.Println("\nСтатистика:")
	fmt.Printf("  Отправлено уникальных Wi-Fi сетей: %d\n", len(networks))

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

	channels := make([]int, 0, len(chMap))
	for ch := range chMap {
		channels = append(channels, ch)
	}
	sort.Ints(channels)
	fmt.Printf("  Каналы: %v\n", channels)
}