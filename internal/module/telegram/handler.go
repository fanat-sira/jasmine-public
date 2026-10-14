package telegram

import (
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"

	"jasmine/internal/pkg/response"
)

type Handler struct {
	service     *Service
	secretToken string
	log         *slog.Logger
}

func NewHandler(service *Service, secretToken string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		service:     service,
		secretToken: secretToken,
		log:         log,
	}
}

// HandleWebhook receives and authenticates updates from the Telegram Bot API.
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Reject unconfigured webhook authentication in every environment.
	if h.secretToken == "" {
		response.Error(w, http.StatusServiceUnavailable, "webhook is not configured")
		return
	}
	// Validate the Telegram transport secret.
	if h.secretToken != "" {
		headerToken := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if subtle.ConstantTimeCompare([]byte(headerToken), []byte(h.secretToken)) != 1 {
			h.log.Warn("rejected webhook request with invalid secret token")
			response.Unauthorized(w, "invalid secret token")
			return
		}
	}

	// 2. Read update body
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		h.log.Warn("failed to read webhook body", slog.String("error", err.Error()))
		response.BadRequest(w, "failed to read request body")
		return
	}

	// 3. Process update
	if err := h.service.ProcessWebhookUpdate(r.Context(), body); err != nil {
		h.log.Error("error processing webhook update", slog.String("error", err.Error()))
		// Still return 200 OK so Telegram doesn't aggressively flood retries on application-level error
		response.JSON(w, http.StatusOK, map[string]string{"status": "error_logged"})
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
