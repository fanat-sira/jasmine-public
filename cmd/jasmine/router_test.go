package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTelegramOnlyTransportSurface(t *testing.T) {
	for _, polling := range []bool{true, false} {
		calls := 0
		r := newRouter(polling, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/users/me", "/api/v1/food", "/api/v1/workouts", "/api/v1/admin/users", "/swagger/index.html", "/"} {
			for _, method := range []string{"GET", "POST"} {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
				if w.Code != 404 {
					t.Errorf("polling=%v %s %s: expected 404 got %d", polling, method, path, w.Code)
				}
			}
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
		if w.Code != 200 {
			t.Fatal("health is missing")
		}
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/telegram/webhook", nil))
		want := 204
		if polling {
			want = 404
		}
		if w.Code != want {
			t.Errorf("polling=%v webhook status=%d want=%d", polling, w.Code, want)
		}
		if polling && calls != 0 {
			t.Fatal("polling exposed webhook")
		}
	}
}
