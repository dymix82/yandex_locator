//go:build windows

package userinfo

import (
	"os"
	"syscall"
	"unsafe"
)

// GetActiveUsername возвращает имя пользователя активной интерактивной сессии.
// Сначала проверяет переменные окружения USERNAME/USER (для консольного запуска).
// Затем через WTS API перебирает все сессии и ищет активную (WTSActive).
// Если не находит, возвращает "unknown".
func GetActiveUsername() string {
	// Попытка через переменные окружения (быстрый путь)
	if username := os.Getenv("USERNAME"); username != "" {
		return username
	}
	if username := os.Getenv("USER"); username != "" {
		return username
	}

	// Загружаем необходимые DLL
	wtsapi32 := syscall.NewLazyDLL("wtsapi32.dll")
	procWTSEnumerateSessionsW := wtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInformationW := wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory := wtsapi32.NewProc("WTSFreeMemory")

	// Константы WinAPI
	const (
		WTS_CURRENT_SERVER_HANDLE = 0
		WTSActive                 = 0 // Состояние сессии "активна"
		WTSUserName               = 5 // Информационный класс – имя пользователя
	)

	var sessionInfo *byte
	var count uint32

	// Получаем список всех сессий
	ret, _, _ := procWTSEnumerateSessionsW.Call(
		WTS_CURRENT_SERVER_HANDLE,
		0, // Reserved
		1, // Version
		uintptr(unsafe.Pointer(&sessionInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if ret == 0 {
		return "unknown"
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(sessionInfo)))

	// Структура WTS_SESSION_INFO (согласно документации)
	type WTS_SESSION_INFO struct {
		SessionID      uint32
		WinStationName *uint16
		State          uint32
	}

	// Перебираем сессии
	for i := uint32(0); i < count; i++ {
		// Получаем указатель на i-ю структуру
		item := (*WTS_SESSION_INFO)(unsafe.Pointer(
			uintptr(unsafe.Pointer(sessionInfo)) + uintptr(i)*unsafe.Sizeof(WTS_SESSION_INFO{}),
		))

		if item.State == WTSActive {
			// Получаем имя пользователя для этой сессии
			var userName *uint16
			var bytes uint32
			ret, _, _ = procWTSQuerySessionInformationW.Call(
				WTS_CURRENT_SERVER_HANDLE,
				uintptr(item.SessionID),
				WTSUserName,
				uintptr(unsafe.Pointer(&userName)),
				uintptr(unsafe.Pointer(&bytes)),
			)
			if ret != 0 && userName != nil {
				defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(userName)))
				return syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(userName))[:bytes/2])
			}
		}
	}
	return "unknown"
}
