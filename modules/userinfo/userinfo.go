//go:build windows

package userinfo

import (
	"os"
	"strings"

	wapi "github.com/iamacarpet/go-win64api"
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

	users, _ := wapi.ListLoggedInUsers()
	if len(users) > 0 {
		return users[0].Username
	}

	// Если ничего не нашли, пробуем через переменные окружения ещё раз (на случай, если раньше отсекли из-за $)
	if username := os.Getenv("USERNAME"); username != "" {
		return username
	}
	return "unknown"
}
