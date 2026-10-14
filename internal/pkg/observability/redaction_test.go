package observability

import (
	"strings"
	"testing"
)

func TestLLMErrorsRedactCredentials(t *testing.T) {
	raw := "bot123456:ABC-DEF_GHI sk-or-v1-abcdef123456 Bearer synthetic-test-token"
	got := SanitizeString(raw)
	for _, value := range []string{"bot123456:ABC-DEF_GHI", "sk-or-v1-abcdef123456", "synthetic-test-token"} {
		if strings.Contains(got, value) {
			t.Fatalf("credential retained in diagnostic")
		}
	}
}
