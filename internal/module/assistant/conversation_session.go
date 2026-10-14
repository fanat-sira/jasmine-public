package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// conversationSessionID is stable per private Telegram chat without exposing its numeric ID.
func conversationSessionID(chatID int64) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(chatID, 10)))
	return "telegram-" + hex.EncodeToString(sum[:16])
}
