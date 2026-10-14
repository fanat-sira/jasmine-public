package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestCoachPromptUsesUserLocalDate(t *testing.T) {
	instant := time.Date(2026, 9, 23, 21, 30, 0, 0, time.UTC)
	prompt := coachPromptAt(instant, "Europe/Moscow")
	if !strings.Contains(strings.ToLower(prompt), "сегодня 24.09.2026") || strings.Contains(prompt, "{{current_date}}") {
		t.Fatal("Jasmine prompt is missing the user's local date or has an unresolved date placeholder")
	}
}
