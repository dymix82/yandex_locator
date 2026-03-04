package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/kardianos/service"
	"locator/modules/userinfo"
	"locator/modules/wifi"
)

const (
	defaultServerURL = "http://localhost:8080/api/locate"
	timeoutSec       = 10
	regKeyPath       = `SOFTWARE\LocatorClient`
	regURLName       = "ServerURL"
	regTokenName     = "Token"
)

// WifiNetwork структура для одной точки доступа
type WifiNetwork struct {
	BSSID          string `json:"bssid"`
	SignalStrength int    `json:"signal_strength"`
	Age            int    `json:"age"`
	Channel        int    `json:"channel"`
}

// ClientRequest структура запроса к серверу
type ClientRequest struct {
	Hostname   string        `json:"hostname"`
	Username   string        `json:"username"`
	OriginalIP string        `json:"original_ip,omitempty"`
	Wifi       []WifiNetwork `json:"wifi,omitempty"`
}

// Logger для условного логирования (только ручной режим)
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

// program реализует интерфейс service.Service
type program struct {
	stopChan chan struct{}
}

func (p *program) Start(s service.Service) error {
	p.stopChan = make(chan struct{})
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error {
	close(p.stopChan)
	return nil
}

func (p *program) run() {
	// Первая отправка сразу после старта
	p.execute()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
			p.execute()
		}
	}
}

func (p *program) execute() {
	// Читаем URL и токен из реестра
	serverURL, token, err := getConfigFromRegistry()
	if err != nil {
		log.Printf("Failed to read registry: %v", err)
		return
	}
	if serverURL == "" {
		log.Printf("Server URL is empty in registry, using default")
		serverURL = defaultServerURL
	}
	if token == "" {
		log.Printf("Token is empty in registry, requests will be unauthorized")
	}

	// Получаем данные
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		log.Printf("Warning: cannot get hostname: %v", err)
	}
	username := userinfo.GetActiveUsername()
	realIP := getPublicIP()
	if realIP == "" {
		log.Printf("Warning: cannot get public IP")
	}
	wifiNetworks := getWifiNetworks()

	// Формируем запрос
	clientReq := ClientRequest{
		Hostname:   hostname,
		Username:   username,
		OriginalIP: realIP,
		Wifi:       wifiNetworks,
	}

	jsonData, err := json.Marshal(clientReq)
	if err != nil {
		log.Printf("Failed to marshal request: %v", err)
		return
	}

	// Отправляем с токеном
	statusCode, responseBody, err := sendToServer(serverURL, token, jsonData)
	if err != nil {
		log.Printf("Failed to send data: %v", err)
		return
	}
	if statusCode != http.StatusOK {
		log.Printf("Server returned %d: %s", statusCode, string(responseBody))
	} else {
		log.Printf("Data sent successfully, server response: %s", string(responseBody))
	}
}

// Функции для работы с реестром
func getConfigFromRegistry() (string, string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", "", err
	}
	defer k.Close()

	url, _, err := k.GetStringValue(regURLName)
	if err != nil {
		return "", "", err
	}
	token, _, err := k.GetStringValue(regTokenName)
	if err != nil {
		// токен может отсутствовать (старая установка)
		token = ""
	}
	return url, token, nil
}

func setConfigToRegistry(url, token string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.SetStringValue(regURLName, url); err != nil {
		return err
	}
	if err := k.SetStringValue(regTokenName, token); err != nil {
		return err
	}
	return nil
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

// getWifiNetpoints получает список всех точек доступа через netsh
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

// sendToServer отправляет POST-запрос с токеном
func sendToServer(url, token string, jsonData []byte) (int, []byte, error) {
	client := http.Client{Timeout: timeoutSec * time.Second}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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

// runOnce выполняет одноразовую отправку (для ручного режима)
func runOnce(serverURL, token string, quiet bool) {
	logger := Logger{quiet: quiet}

	if !logger.quiet {
		fmt.Println("Клиент отправки геоданных на сервер (одноразовый режим)")
		fmt.Println("========================================================")
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		logger.Errorf("Предупреждение: не удалось получить hostname: %v\n", err)
	}
	logger.Printf("Hostname: %s\n", hostname)

	username := userinfo.GetActiveUsername()
	logger.Printf("Активный пользователь: %s\n", username)

	realIP := getPublicIP()
	if realIP == "" {
		logger.Errorln("Ошибка: не удалось получить публичный IP. Проверьте интернет-соединение.")
	} else {
		logger.Printf("Реальный публичный IP: %s\n", realIP)
	}

	logger.Println("\nСканируем Wi-Fi сети...")
	wifiNetworks := getWifiNetworks()
	logger.Printf("Найдено уникальных Wi-Fi сетей: %d\n", len(wifiNetworks))

	clientReq := ClientRequest{
		Hostname:   hostname,
		Username:   username,
		OriginalIP: realIP,
		Wifi:       wifiNetworks,
	}

	jsonData, err := json.MarshalIndent(clientReq, "", "  ")
	if err != nil {
		logger.Errorf("Ошибка формирования JSON: %v\n", err)
		return
	}
	logger.Println("\nJSON для отправки на сервер:")
	logger.Println(string(jsonData))

	logger.Printf("\nОтправляем данные на сервер %s...\n", serverURL)
	statusCode, responseBody, err := sendToServer(serverURL, token, jsonData)
	if err != nil {
		logger.Errorf("Ошибка при отправке: %v\n", err)
		return
	}

	fmt.Printf("HTTP Status: %d\n", statusCode)

	if statusCode == http.StatusOK {
		logger.Println("Данные успешно отправлены на сервер")
	} else {
		logger.Printf("Сервер вернул ошибку: %s\n", string(responseBody))
	}

	if !logger.quiet && len(responseBody) > 0 {
		fmt.Println("\nОтвет сервера:")
		fmt.Println(string(responseBody))
	}
}

func main() {
	// Настройка службы
	svcConfig := &service.Config{
		Name:        "LocatorClient",
		DisplayName: "Locator Client Service",
		Description: "Собирает геоданные и отправляет на сервер",
	}

	prg := &program{}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}

	// Обработка команд управления службой
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		switch cmd {
		case "install":
			if len(os.Args) < 4 {
				fmt.Println("Использование: client.exe install <server_url> <token>")
				fmt.Println("Пример: client.exe install http://93.77.187.76:8080/api/locate mysecrettoken")
				return
			}
			url := os.Args[2]
			token := os.Args[3]
			if err := setConfigToRegistry(url, token); err != nil {
				fmt.Printf("Ошибка записи в реестр: %v\n", err)
				return
			}
			if err := s.Install(); err != nil {
				fmt.Printf("Ошибка установки службы: %v\n", err)
				return
			}
			fmt.Println("Служба успешно установлена.")
			return
		case "uninstall":
			if err := s.Uninstall(); err != nil {
				fmt.Printf("Ошибка удаления службы: %v\n", err)
				return
			}
			fmt.Println("Служба успешно удалена.")
			return
		case "start":
			if err := s.Start(); err != nil {
				fmt.Printf("Ошибка запуска службы: %v\n", err)
				return
			}
			fmt.Println("Служба запущена.")
			return
		case "stop":
			if err := s.Stop(); err != nil {
				fmt.Printf("Ошибка остановки службы: %v\n", err)
				return
			}
			fmt.Println("Служба остановлена.")
			return
		case "run":
			// Исправлено: отдельный набор флагов
			runCmd := flag.NewFlagSet("run", flag.ExitOnError)
			serverURL := runCmd.String("server", defaultServerURL, "URL сервера")
			token := runCmd.String("token", "", "токен авторизации")
			quiet := runCmd.Bool("quiet", false, "тихий режим")
			runCmd.Parse(os.Args[2:])
			runOnce(*serverURL, *token, *quiet)
			return
		default:
			fmt.Printf("Неизвестная команда: %s\n", cmd)
			return
		}
	}

	// Запуск как служба
	if err := s.Run(); err != nil {
		log.Fatal(err)
	}
}