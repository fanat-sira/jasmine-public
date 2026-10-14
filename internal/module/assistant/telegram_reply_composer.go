package assistant

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

// TelegramReplyComposer turns verified application output into the final chat message.
// Only the OpenRouter reply model writes text that is sent to Telegram.
type TelegramReplyComposer struct {
	client      *openrouter.Client
	model       string
	runtimeInfo PublicRuntimeInfo
	knowledge   *PersonalKnowledge
	preferences *AssistantPreferences
}

func NewTelegramReplyComposer(client *openrouter.Client, model string) *TelegramReplyComposer {
	return &TelegramReplyComposer{client: client, model: model}
}

func (c *TelegramReplyComposer) SetPublicRuntimeInfo(info PublicRuntimeInfo)     { c.runtimeInfo = info }
func (c *TelegramReplyComposer) SetPersonalKnowledge(k *PersonalKnowledge)       { c.knowledge = k }
func (c *TelegramReplyComposer) SetAssistantPreferences(p *AssistantPreferences) { c.preferences = p }

func (c *TelegramReplyComposer) Compose(ctx context.Context, chatID int64, facts telegram.ReplyContext, history []telegram.TelegramMessage) (string, error) {
	if c == nil || c.client == nil || strings.TrimSpace(c.model) == "" {
		return "", fmt.Errorf("LLM_REPLY is not configured")
	}
	policy := telegram.ParseTurnContextPolicy(facts.UserMessage)
	facts.ContextPolicy = policy
	if c.knowledge != nil && policy.UseMemories && policy.UseProfile {
		relevant, err := c.knowledge.RelevantByChat(ctx, chatID, facts.UserMessage)
		if err != nil {
			return "", fmt.Errorf("load personal knowledge: %w", err)
		}
		if len(relevant) > 0 {
			state := make(map[string]any, len(facts.CurrentState)+1)
			for key, value := range facts.CurrentState {
				state[key] = value
			}
			state["personal_facts"] = relevant
			facts.CurrentState = state
		}
	}

	// Prior chat may contain workout history. Withhold it for this turn when
	// the owner explicitly excludes past workouts.
	if !policy.UseConversationHistory || !policy.UseDomainHistory {
		history = nil
	}
	messages := make([]openrouter.Message, 0, len(history))
	for _, item := range history {
		if item.Text == nil || strings.TrimSpace(*item.Text) == "" {
			continue
		}
		role := "user"
		if item.Direction == telegram.DirectionOutbound {
			role = "assistant"
		}
		messages = append(messages, openrouter.Message{Role: role, Content: redactCredentialText(*item.Text)})
	}
	report, err := trustedReplyReport(facts, history, c.runtimeInfo, wantsTechnicalSelfDescription(facts.UserMessage))
	if err != nil {
		return "", err
	}
	userMessage := redactCredentialText(strings.TrimSpace(facts.UserMessage))
	if userMessage == "" {
		userMessage = "[attachment_without_caption]"
	}
	aiCtx, cancel := context.WithTimeout(openrouter.WithSessionID(correlation.WithOperation(ctx, "telegram_reply"), conversationSessionID(chatID)), 75*time.Second)
	defer cancel()
	profile := emptyCommunicationProfile()
	if c.preferences != nil {
		profile, err = c.preferences.LoadByChat(ctx, chatID)
		if err != nil {
			return "", fmt.Errorf("load communication preferences: %w", err)
		}
	}
	systemPrompt := CompileReplyPrompt(profile, report, time.Now())
	answer, err := c.client.SendConversation(aiCtx, c.model, systemPrompt, messages, userMessage)
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("LLM_REPLY returned an empty answer")
	}
	return html.EscapeString(answer), nil
}
