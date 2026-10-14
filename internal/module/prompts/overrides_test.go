package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompts.json")
	if err := os.WriteFile(path, []byte(`{"jasmine_reply":"  PRIVATE_TEMPLATE\n"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverrides(path)
	if err != nil || got[KeyJasmineReply] != "  PRIVATE_TEMPLATE\n" {
		t.Fatalf("template preservation failed: %v", err)
	}
	for _, raw := range []string{
		`null`, `{}`, `[]`, `{"jasmine_reply":null}`, `{"jasmine_reply":" "}`,
		`{"unknown":"PRIVATE_TEMPLATE"}`, `{"jasmine_reply":42}`,
		`{"jasmine_reply":"PRIVATE_TEMPLATE"} trailing`, string([]byte{0xff}),
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOverrides(path); err == nil || strings.Contains(err.Error(), "PRIVATE_TEMPLATE") {
			t.Fatal("invalid file accepted or private content included in error")
		}
	}
	if _, err := LoadOverrides(path + ".missing"); err == nil {
		t.Fatal("missing configured file accepted")
	}
}
