//go:build windows

package userinfo

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// GetActiveUsername возвращает имя пользователя, вошедшего в систему (активного или отключённого).
// Игнорирует системные учётные записи.
func GetActiveUsername() string {
	// Сначала пробуем переменные окружения (если запущено из консоли пользователя)
	if username := os.Getenv("USERNAME"); username != "" && !strings.HasSuffix(username, "$") {
		return username
	}
	if username := os.Getenv("USER"); username != "" && !strings.HasSuffix(username, "$") {
		return username
	}

	// Загружаем библиотеки
	wtsapi32 := syscall.NewLazyDLL("wtsapi32.dll")
	procWTSEnumerateSessionsW := wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInformationW := wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory := wtsapi32.NewProc("WTSFreeMemory")

	const (
		WTS_CURRENT_SERVER_HANDLE = 0
		WTSUserName               = 5
		// Состояния сессии
		WTSActive       = 0
		WTSConnected    = 1
		WTSConnectQuery = 2
		WTSShadow       = 3
		WTSDisconnected = 4
		WTSIdle         = 5
		WTSListen       = 6
		WTSReset        = 7
		WTSDown         = 8
		WTSInit         = 9
	)

	var sessionInfo *byte
	var count uint32

	ret, _, _ := procWTSEnumerateSessionsW.Call(
		WTS_CURRENT_SERVER_HANDLE,
		0,
		1,
		uintptr(unsafe.Pointer(&sessionInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		return "unknown"
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(sessionInfo)))

	type WTS_SESSION_INFO struct {
		SessionID      uint32
		WinStationName *uint16
		State          uint32
	}

	// Массив допустимых состояний – любое, кроме служебных (Listen, Idle и т.д.)
	// На практике нам подойдут Active, Connected, Disconnected
	validStates := map[uint32]bool{
		WTSActive:       true,
		WTSConnected:    true,
		WTSDisconnected: true,
	}

	for i := uint32(0); i < count; i++ {
		item := (*WTS_SESSION_INFO)(unsafe.Pointer(
			uintptr(unsafe.Pointer(sessionInfo)) + uintptr(i)*unsafe.Sizeof(WTS_SESSION_INFO{}),
		))

		if !validStates[item.State] {
			continue
		}

		var userName *uint16
		var bytes uint32
		ret, _, _ = procWTSQuerySessionInformationW.Call(
			WTS_CURRENT_SERVER_HANDLE,
			uintptr(item.SessionID),
			WTSUserName,
			uintptr(unsafe.Pointer(&userName)),
			uintptr(unsafe.Pointer(&bytes)),
		)
		if ret == 0 || userName == nil {
			continue
		}
		name := syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(userName))[:bytes/2])
		procWTSFreeMemory.Call(uintptr(unsafe.Pointer(userName)))

		// Исключаем системные имена
		if name != "" && !strings.HasSuffix(name, "$") &&
			name != "SYSTEM" && name != "LOCAL SERVICE" && name != "NETWORK SERVICE" {
			return name
		}
	}

	// Если ничего не нашли, пробуем через переменные окружения ещё раз (на случай, если раньше отсекли из-за $)
	if username := os.Getenv("USERNAME"); username != "" {
		return username
	}
	return "unknown"
}
