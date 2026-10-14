package telegram

import (
	"context"
	"strings"
)

// TurnContextPolicy controls what background information may be used for one
// turn. It never changes persisted user data or application permission checks.
type TurnContextPolicy struct {
	UseConversationHistory bool `json:"use_conversation_history"`
	UseMemories            bool `json:"use_memories"`
	UseProfile             bool `json:"use_profile"`
	UseDomainHistory       bool `json:"use_domain_history"`
}

func DefaultTurnContextPolicy() TurnContextPolicy {
	return TurnContextPolicy{true, true, true, true}
}

// ParseTurnContextPolicy recognizes explicit, temporary context exclusions.
// Style/persona requests do not alter this policy.
func ParseTurnContextPolicy(message string) TurnContextPolicy {
	policy := DefaultTurnContextPolicy()
	text := strings.ToLower(strings.ReplaceAll(message, "ё", "е"))
	text = strings.Join(strings.Fields(text), " ")
	containsAny := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if strings.Contains(text, phrase) {
				return true
			}
		}
		return false
	}
	if containsAny("используй только то, что я сейчас написал", "используй только то что я сейчас написал", "только мое сообщение сейчас", "только это сообщение", "only use what i just wrote") {
		return TurnContextPolicy{}
	}
	if containsAny("забудь контекст выше", "без контекста выше", "не учитывай предыдущие сообщения", "не учитывай переписку", "ignore previous messages", "ignore conversation history") {
		policy.UseConversationHistory = false
	}
	if containsAny("без учета памяти", "без учета моей памяти", "не учитывай память", "не используй память", "забудь мои предпочтения на этот ответ", "ignore memories") {
		policy.UseMemories = false
	}
	if containsAny("без учета профиля", "не учитывай профиль", "не используй профиль", "ignore my profile") {
		policy.UseProfile = false
	}
	if containsAny("не учитывай мои прошлые тренировки", "не учитывай прошлые тренировки", "не учитывай сейчас мои прошлые тренировки", "без учета прошлых тренировок", "без истории тренировок", "ignore my workout history", "ignore past workouts") {
		policy.UseDomainHistory = false
	}
	return policy
}

type turnContextPolicyKey struct{}

func WithTurnContextPolicy(ctx context.Context, policy TurnContextPolicy) context.Context {
	return context.WithValue(ctx, turnContextPolicyKey{}, policy)
}

func TurnContextPolicyFromContext(ctx context.Context) TurnContextPolicy {
	if policy, ok := ctx.Value(turnContextPolicyKey{}).(TurnContextPolicy); ok {
		return policy
	}
	return DefaultTurnContextPolicy()
}
