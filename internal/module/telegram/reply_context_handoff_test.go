package telegram

import "testing"

func TestIngestionResponseHandoffIgnoresPrewrittenReply(t *testing.T) {
	refs := []DomainReference{{Type: "meal", ID: "meal-1", Extra: map[string]any{"calories": 175}}}
	for _, response := range []*IngestionResponse{
		{ConfirmationHTML: "Записала завтрак: 175 ккал", DomainReferences: refs},
		{ConfirmationHTML: "Записала завтрак: 175 ккал", DomainReferences: refs, ReplyContext: &ReplyContext{EventType: "meal_logged"}},
	} {
		facts := replyContextForIngestionResponse(response)
		if facts.UserMessage != "" || len(facts.Facts) != 0 || len(facts.ToolResults) != 1 || facts.ToolResults[0].Extra["calories"] != 175 {
			t.Fatalf("prewritten reply leaked or verified data lost: %+v", facts)
		}
	}
}
