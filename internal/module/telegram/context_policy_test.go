package telegram

import "testing"

func TestTurnContextPolicyExactRequests(t *testing.T) {
	cases := []struct {
		message                            string
		history, memories, profile, domain bool
	}{
		{"привет ты кто", true, true, true, true},
		{"че", true, true, true, true},
		{"/start", true, true, true, true},
		{"можешь общаться как dommy mommy?", true, true, true, true},
		{"отвечай максимально сухо", true, true, true, true},
		{"забудь стиль жасмин на один ответ", true, true, true, true},
		{"для этого ответа не учитывай мои прошлые тренировки", true, true, true, false},
		{"ответь без учета памяти", true, false, true, true},
		{"не учитывай профиль", true, true, false, true},
		{"для этого вопроса забудь контекст выше", false, true, true, true},
		{"используй только то что я сейчас написал", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.message, func(t *testing.T) {
			got := ParseTurnContextPolicy(tc.message)
			if got.UseConversationHistory != tc.history || got.UseMemories != tc.memories || got.UseProfile != tc.profile || got.UseDomainHistory != tc.domain {
				t.Fatalf("policy for %q: %+v", tc.message, got)
			}
		})
	}
}
