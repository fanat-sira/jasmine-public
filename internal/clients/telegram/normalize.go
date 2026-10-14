package telegram

import "strings"

var botTextReplacer = strings.NewReplacer(
	"—", "-", "–", "-", "―", "-", "−", "-", "‐", "-", "‑", "-",
	"ё", "е", "Ё", "Е",
	"«", "\"", "»", "\"", "„", "\"", "“", "\"", "”", "\"",
	"‟", "\"", "‹", "\"", "›", "\"", "‘", "\"", "’", "\"",
	"‚", "\"", "‛", "\"", "＂", "\"", "「", "\"", "」", "\"",
	"『", "\"", "』", "\"", "〝", "\"", "〞", "\"",
)

// NormalizeBotText applies the chat's typography rules at the Telegram boundary.
func NormalizeBotText(text string) string {
	return botTextReplacer.Replace(text)
}
