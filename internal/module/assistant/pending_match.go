package assistant

import (
	"regexp"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
)

var pendingNumber = regexp.MustCompile(`^(?:[1-9]|[1-9][0-9])$`)

// A pending clarification is relevant only to a plausible answer, not every later turn.
func pendingMatches(p *telegram.PendingClarification, text string, replyTo *int64) bool {
	if p == nil || (!p.CreatedAt.IsZero() && time.Since(p.CreatedAt) > 30*time.Minute) {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "ё", "е")))
	if t == "" || strings.HasPrefix(t, "/") {
		return false
	}
	if replyTo != nil {
		return true
	}
	if wantsPersonalIntrospection(t) || wantsTechnicalSelfDescription(t) || strings.Contains(t, "проект") || strings.Contains(t, "профил") || t == "привет" || t == "бро" {
		return false
	}
	switch p.Type {
	case "image_unknown_needs_intent":
		for _, x := range []string{"еда", "съел", "продукт", "этикет", "тренажер", "дисплей", "весы", "упражн", "фото", "фотк", "калори", "бжу", "что на фото", "что это"} {
			if strings.Contains(t, x) {
				return true
			}
		}
	case "machine_display_needs_machine":
		if len([]rune(t)) > 80 || strings.Contains(t, "?") {
			return false
		}
		for _, x := range []string{"жим", "тяга", "разгиб", "сгиб", "присед", "машина", "тренажер", "блок", "гакк", "хаммер", "эллипс", "беговая", "велотрен", "гребн"} {
			if strings.Contains(t, x) {
				return true
			}
		}
	case "exercise_disambiguation", "correction_disambiguation":
		if pendingNumber.MatchString(t) {
			return true
		}
		for _, candidate := range p.Candidates {
			if strings.EqualFold(strings.TrimSpace(candidate), t) {
				return true
			}
		}
		return len([]rune(t)) < 70 && (strings.Contains(t, "перв") || strings.Contains(t, "втор") || strings.Contains(t, "последн"))
	}
	return false
}
