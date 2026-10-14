package assistant

import (
	"jasmine/internal/module/telegram"
	"testing"
	"time"
)

func TestPendingClarificationIsScoped(t *testing.T) {
	p := &telegram.PendingClarification{Type: "image_unknown_needs_intent", CreatedAt: time.Now()}
	for _, text := range []string{"кто я", "что ты умеешь", "какие проекты", "привет", "/start"} {
		if pendingMatches(p, text, nil) {
			t.Fatalf("unrelated turn captured: %q", text)
		}
	}
	if !pendingMatches(p, "что это?", nil) {
		t.Fatal("relevant image question lost")
	}
	p.Type = "machine_display_needs_machine"
	if !pendingMatches(p, "эллипс", nil) {
		t.Fatal("machine answer lost")
	}
	p.CreatedAt = time.Now().Add(-time.Hour)
	if pendingMatches(p, "эллипс", nil) {
		t.Fatal("expired pending captured turn")
	}
}
