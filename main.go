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
	"strings"
	"time"
    "strconv"
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
	// Ошибки выводим всегда
	fmt.Fprintln(os.Stderr, a...)
}

func (l *Logger) Errorf(format string, a ...interface{}) {
	// Ошибки выводим всегда
	fmt.Fprintf(os.Stderr, format, a...)
}

var log Logger

func init() {
	// Загружаем .env файл
	_ = godotenv.Load()
	yandexAPIKey = os.Getenv("YANDEX_API_KEY")
	if yandexAPIKey == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: не задан YANDEX_API_KEY в .env файле или переменных окружения.")
		fmt.Fprintln(os.Stderr, "Создайте файл .env с содержимым: YANDEX_API_KEY=ваш_ключ")
		os.Exit(1)
	}
}

func main() {
	// Парсим флаги командной строки
	quiet := flag.Bool("quiet", false, "тихий режим (вывод только ошибок и кода ответа)")
	flag.Parse()
	
	log = Logger{quiet: *quiet}

	if !log.quiet {
		fmt.Println("Утилита определения геолокации")
		fmt.Println("===============================")
	}

	// 1. Получаем hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		log.Errorf("Предупреждение: не удалось получить hostname: %v\n", err)
	}
	log.Printf("Hostname: %s\n", hostname)

	// 2. Публичный IP
	publicIP := getPublicIP()
	if publicIP == "" {
		log.Errorln("Ошибка: не удалось получить публичный IP. Проверьте интернет-соединение.")
	} else {
		log.Printf("Публичный IP: %s\n", publicIP)
	}

	// 3. Wi-Fi сети
	log.Println("\nСканируем Wi-Fi сети...")
	wifiNetworks := getWifiNetworks()
	log.Printf("Найдено уникальных Wi-Fi сетей: %d\n", len(wifiNetworks))

	// 4. Проверка наличия данных
	if len(wifiNetworks) == 0 && publicIP == "" {
		log.Errorln("\nОшибка: нет данных для отправки.")
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
		log.Errorf("Ошибка формирования JSON: %v\n", err)
		os.Exit(1)
	}
	log.Println("\nJSON для отправки:")
	log.Println(string(jsonData))

	// 6. Отправка запроса
	log.Println("\nОтправляем запрос на Яндекс API...")
	resp, rawResponse, statusCode, err := sendRequest(jsonData)
	if err != nil {
		log.Errorf("Ошибка: %v\n", err)
		os.Exit(1)
	}
	
	// Всегда выводим код ответа
	fmt.Printf("HTTP Status: %d\n", statusCode)
	
	if !log.quiet {
		fmt.Println("Запрос успешно отправлен! (HTTP 200)")
	}

	// 7. Вывод полного ответа сервера (только если не quiet)
	if !log.quiet {
		fmt.Println("\nПолный ответ сервера:")
		fmt.Println(string(rawResponse))
	}

	// 8. Вывод координат в нужном формате (только если не quiet)
	if resp != nil && resp.Location.Point.Lat != 0 && resp.Location.Point.Lon != 0 {
		log.Printf("\nКоординаты: %.6f, %.6f\n", resp.Location.Point.Lat, resp.Location.Point.Lon)
		log.Printf("Ссылка на карту: https://maps.yandex.ru/?ll=%.6f,%.6f&z=17\n", 
			resp.Location.Point.Lon, resp.Location.Point.Lat)
		
		// Вывод точности, если она есть
		if resp.Location.Accuracy > 0 {
			log.Printf("Точность: %.1f метров\n", resp.Location.Accuracy)
		}
	} else {
		log.Println("\nПредупреждение: координаты не найдены в ответе сервера.")
	}

	// 9. Сохранение результата в файл
	if resp != nil {
		err = saveResult(hostname, resp)
		if err != nil {
			log.Errorf("Ошибка сохранения результата: %v\n", err)
		} else {
			log.Println("Результат сохранен в output.json")
		}
	}

	// 10. Статистика (только если не quiet)
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
    log.Println("  Используем netsh для сканирования...")
    cmd := exec.Command("cmd", "/c", "chcp 437 > nul && netsh wlan show networks mode=bssid")

    output, err := cmd.Output()
	fmt.Println("=== RAW NETSH OUTPUT ===")
    fmt.Println(string(output))
    fmt.Println("=== END RAW OUTPUT ===")
    if err != nil {
        log.Printf("  Ошибка запуска netsh: %v\n", err)
        return nil
    }

    // Для отладки можно раскомментировать:
    // fmt.Println(string(output))

    // Разделяем вывод на блоки сетей (два перевода строки)
    blocks := strings.Split(string(output), "\r\n\r\n")
    var networks []WifiNetwork

    for _, block := range blocks {
        // Блок должен содержать BSSID, иначе это не сеть
        if !strings.Contains(block, "BSSID") {
            continue
        }

        lines := strings.Split(block, "\r\n")
        var bssid, signalStr, channelStr string

        for _, line := range lines {
            line = strings.TrimSpace(line)

            // Поиск BSSID
            if strings.Contains(line, "BSSID") {
                // Ожидается формат "BSSID 1 : xx:xx:xx:xx:xx:xx" или "BSSID : xx:xx..."
                parts := strings.SplitN(line, ":", 2)
                if len(parts) == 2 {
                    bssid = strings.TrimSpace(parts[1])
                    // Проверим, что это похоже на MAC-адрес (опционально)
                    if matched, _ := regexp.MatchString(`([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}`, bssid); !matched {
                        bssid = "" // сброс, если не похоже
                    }
                }
                continue
            }

            // Поиск сигнала (русский или английский)
            if strings.Contains(line, "Сигнал") || strings.Contains(line, "Signal") {
                re := regexp.MustCompile(`(\d+)%`)
                if matches := re.FindStringSubmatch(line); matches != nil {
                    signalStr = matches[1]
                }
                continue
            }

            // Поиск канала
            if strings.Contains(line, "Канал") || strings.Contains(line, "Channel") {
                re := regexp.MustCompile(`(\d+)`)
                if matches := re.FindStringSubmatch(line); matches != nil {
                    channelStr = matches[1]
                }
                continue
            }
        }

        // Если все данные найдены, добавляем сеть
        if bssid != "" && signalStr != "" && channelStr != "" {
            signal, _ := strconv.Atoi(signalStr)
            channel, _ := strconv.Atoi(channelStr)
            // Конвертируем процент в dBm (приблизительно)
            signalDBm := (signal * 70 / 100) - 100
            networks = append(networks, WifiNetwork{
                BSSID:          bssid,
                Channel:        channel,
                SignalStrength: signalDBm,
                Age:            0,
            })
        }
    }

    // Удаление дубликатов по BSSID
    seen := make(map[string]bool)
    var unique []WifiNetwork
    for _, net := range networks {
        if !seen[net.BSSID] {
            seen[net.BSSID] = true
            unique = append(unique, net)
        }
    }

    // Сортировка по силе сигнала
    sort.Slice(unique, func(i, j int) bool {
        return unique[i].SignalStrength > unique[j].SignalStrength
    })

    return unique
}

// sendRequest выполняет POST запрос и возвращает как структуру, так и сырой ответ и код статуса
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

	// Парсим ответ в структуру
	var response YandexResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		// Если не удалось распарсить в структуру, всё равно возвращаем сырой ответ
		return nil, body, resp.StatusCode, nil
	}

	return &response, body, resp.StatusCode, nil
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

	// Для отладки выводим сохраненный JSON (только если не quiet)
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