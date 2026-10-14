package config

import "testing"

func TestTelegramAllowedUserIDs(t *testing.T) {
	ids, err := (TelegramConfig{AllowedUserIDs: "123, 456,123"}).AllowedTelegramUserIDs()
	if err != nil || len(ids) != 2 || ids[0] != 123 || ids[1] != 456 {
		t.Fatalf("parsed IDs = %v, error = %v", ids, err)
	}
	for _, value := range []string{"", " ", "abc", "123,", "0", "-5"} {
		if _, err := (TelegramConfig{AllowedUserIDs: value}).AllowedTelegramUserIDs(); err == nil {
			t.Fatalf("expected error for %q", value)
		}
	}
}
