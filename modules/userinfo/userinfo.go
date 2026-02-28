//go:build windows

package userinfo

import (
	"syscall" // добавьте этот импорт, если его нет
	"unsafe"
	"os"
)

// getActiveUsername получает имя активного пользователя Windows
func GetActiveUsername() string {
	// Сначала пробуем переменные окружения (для консольного запуска)
	if username := os.Getenv("USERNAME"); username != "" {
		return username
	}
	if username := os.Getenv("USER"); username != "" {
		return username
	}

	// Загружаем необходимые DLL
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	wtsapi32 := syscall.NewLazyDLL("wtsapi32.dll")

	// Функция для получения ID активной консольной сессии
	procWTSGetActiveConsoleSessionId := kernel32.NewProc("WTSGetActiveConsoleSessionId")
	// Функция для получения информации о сессии (имя пользователя)
	procWTSQuerySessionInformationW := wtsapi32.NewProc("WTSQuerySessionInformationW")
	// Функция для освобождения памяти
	procWTSFreeMemory := wtsapi32.NewProc("WTSFreeMemory")

	// Получаем ID сессии
	ret, _, _ := procWTSGetActiveConsoleSessionId.Call()
	sessionId := uint32(0xFFFFFFFF) // INVALID_SESSION_ID
	if ret != 0xFFFFFFFF {
		sessionId = uint32(ret)
	}

	// Константы WinAPI
	const (
		WTS_CURRENT_SERVER_HANDLE = 0
		WTSUserName               = 5 // Информационный класс для имени пользователя
	)

	var userName *uint16
	var bytes uint32
	// Вызываем WTSQuerySessionInformationW для получения имени пользователя
	ret, _, _ = procWTSQuerySessionInformationW.Call(
		WTS_CURRENT_SERVER_HANDLE,
		uintptr(sessionId),
		WTSUserName,
		uintptr(unsafe.Pointer(&userName)),
		uintptr(unsafe.Pointer(&bytes)),
	)
	if ret == 0 {
		// Ошибка вызова
		return "unknown"
	}
	// Освобождаем память после использования
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(userName)))

	// Преобразуем UTF-16 строку в Go-строку
	return syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(userName))[:bytes/2])
}