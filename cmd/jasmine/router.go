package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"jasmine/internal/pkg/response"
)

func newRouter(polling bool, webhook http.HandlerFunc, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(150 * time.Second))
	r.Use(requestLogger(log))
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, http.StatusOK, map[string]string{"status": "ok", "time": fmt.Sprintf("%d", time.Now().Unix())})
	})
	if !polling {
		r.Post("/api/v1/telegram/webhook", webhook)
	}
	return r
}
