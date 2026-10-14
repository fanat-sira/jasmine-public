// Package response предоставляет единый формат JSON-ответов для всего API.
// Все эндпоинты должны использовать эти хелперы — это гарантирует консистентность.
package response

import (
	"encoding/json"
	"net/http"
)

// Envelope — стандартный конверт для любого ответа API.
//
//	Успех:  {"success": true,  "data": {...}}
//	Ошибка: {"success": false, "error": "сообщение об ошибке"}
type Envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// JSON отправляет успешный ответ с произвольной полезной нагрузкой.
func JSON(w http.ResponseWriter, statusCode int, data any) {
	write(w, statusCode, Envelope{
		Success: true,
		Data:    data,
	})
}

// Error отправляет стандартизированный ответ об ошибке.
func Error(w http.ResponseWriter, statusCode int, message string) {
	write(w, statusCode, Envelope{
		Success: false,
		Error:   message,
	})
}

// BadRequest — сокращение для 400 Bad Request.
func BadRequest(w http.ResponseWriter, message string) {
	Error(w, http.StatusBadRequest, message)
}

// Unauthorized — сокращение для 401 Unauthorized.
func Unauthorized(w http.ResponseWriter, message string) {
	Error(w, http.StatusUnauthorized, message)
}

// Forbidden — сокращение для 403 Forbidden.
func Forbidden(w http.ResponseWriter, message string) {
	Error(w, http.StatusForbidden, message)
}

// NotFound — сокращение для 404 Not Found.
func NotFound(w http.ResponseWriter, message string) {
	Error(w, http.StatusNotFound, message)
}

// InternalError — сокращение для 500 Internal Server Error.
// Скрывает детали ошибки от клиента (безопасность).
func InternalError(w http.ResponseWriter) {
	Error(w, http.StatusInternalServerError, "internal server error")
}

// Conflict — сокращение для 409 Conflict (например, ресурс уже существует).
func Conflict(w http.ResponseWriter, message string) {
	Error(w, http.StatusConflict, message)
}

// write — внутренний хелпер: сериализует конверт и записывает в ResponseWriter.
func write(w http.ResponseWriter, statusCode int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	// Если маршал вдруг упал — пишем fallback прямо в тело, не паникуя.
	if err := json.NewEncoder(w).Encode(env); err != nil {
		http.Error(w, `{"success":false,"error":"failed to encode response"}`, http.StatusInternalServerError)
	}
}
