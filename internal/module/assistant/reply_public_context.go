package assistant

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"jasmine/internal/module/telegram"
)

// PublicRuntimeInfo contains only application facts safe to discuss with the owner.
// Credentials and raw configuration structs must never be added here.
type PublicRuntimeInfo struct {
	Runtime                 string          `json:"runtime"`
	MainModel               string          `json:"main_model"`
	PhotoModel              string          `json:"photo_model"`
	ReplyModel              string          `json:"reply_model"`
	Skills                  []string        `json:"skills"`
	Tools                   []string        `json:"tools"`
	PromptKeys              []string        `json:"prompt_keys"`
	HasOpenRouter           bool            `json:"openrouter_configured"`
	HasAssistantPreferences bool            `json:"communication_preferences_enabled"`
	Capabilities            map[string]bool `json:"capabilities"`
	SupportedAttachments    []string        `json:"supported_attachments"`
	PersistentDomains       []string        `json:"persistent_domains"`
}

func (i PublicRuntimeInfo) sorted() PublicRuntimeInfo {
	i.Skills = append([]string(nil), i.Skills...)
	i.Tools = append([]string(nil), i.Tools...)
	i.PromptKeys = append([]string(nil), i.PromptKeys...)
	sort.Strings(i.Skills)
	sort.Strings(i.Tools)
	sort.Strings(i.PromptKeys)
	i.Capabilities = capabilitiesFromTools(i.Tools)
	i.Capabilities["can_manage_communication_preferences"] = i.HasAssistantPreferences
	if i.HasAssistantPreferences {
		i.PersistentDomains = append(i.PersistentDomains, "assistant_preferences")
	}
	i.SupportedAttachments = []string{"photo", "photo_album", "text_document", "json_document"}
	if i.Capabilities["can_log_meals"] {
		i.PersistentDomains = append(i.PersistentDomains, "nutrition")
	}
	if i.Capabilities["can_manage_workouts"] {
		i.PersistentDomains = append(i.PersistentDomains, "workouts")
	}
	if i.Capabilities["can_import_personal_profile"] {
		i.PersistentDomains = append(i.PersistentDomains, "personal_knowledge")
	}
	return i
}

func capabilitiesFromTools(tools []string) map[string]bool {
	have := map[string]bool{}
	for _, name := range tools {
		have[name] = true
	}
	return map[string]bool{
		"can_log_meals":               have["meal.bulk_save"] || have["meal.add_item"] || have["legacy.handle_photo"],
		"can_read_meals":              have["meal.read_current"],
		"can_manage_workouts":         have["coach.log_set"] || have["coach.propose_workout"],
		"can_import_personal_profile": have["legacy.handle_document"],
		"can_read_personal_knowledge": have["personal.read"],
		"can_list_projects":           have["personal.list_projects"],
		"can_process_multiple_photos": have["legacy.handle_album"],
		"can_execute_arbitrary_sql":   false,
	}
}

func wantsTechnicalSelfDescription(text string) bool {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	for _, phrase := range []string{"тех детал", "техническ", "как ты устроен", "как у тебя устроен", "какие модели", "какая модель", "какие tools", "какие инструменты", "tool trace", "tool трейс", "тул трейс", "планировщик", "planner", "промпт", "runtime", "как работает память", "как устроена память", "почему ты выбрала этот tool", "что умеешь", "что ты умеешь", "че умеешь", "чё умеешь", "вообще умеешь", "можешь сохранять", "можешь писать в дневник", "можешь записывать еду", "ты сохраняешь", "ты умеешь записывать", "ты можешь писать в бд", "ты можешь читать бд", "читать бд", "писать в бд", "ты можешь записывать", "какие возможности"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

var credentialText = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|redis)://[^\s"']+|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----|sk-or-v1-[a-z0-9_-]+|bearer\s+[^\s"']+|\b[0-9]{6,}:[a-z0-9_-]{20,}\b|(?:api[_-]?key|bot[_-]?token|jwt[_-]?secret|password)\s*[:=]\s*[^\s,"'}]+`)

func redactCredentialText(text string) string {
	return credentialText.ReplaceAllString(text, "[REDACTED]")
}

func sensitiveMetadataKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range []string{"api_key", "token", "secret", "password", "authorization", "private_key", "credential", "env_file"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

func sanitizeMetadata(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if sensitiveMetadataKey(key) {
				continue
			}
			out[key] = sanitizeMetadata(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeMetadata(item)
		}
		return out
	case string:
		return redactCredentialText(v)
	default:
		return v
	}
}

func trustedReplyReport(facts telegram.ReplyContext, history []telegram.TelegramMessage, runtimeInfo PublicRuntimeInfo, includeRuntime bool) (string, error) {
	// UserMessage is sent separately as the real final user turn.
	facts.UserMessage = ""
	external := facts.ExternalContent
	facts.ExternalContent = nil
	policy := facts.ContextPolicy
	if !policy.UseProfile || !policy.UseMemories || !policy.UseDomainHistory {
		state := make(map[string]any, len(facts.CurrentState))
		for key, value := range facts.CurrentState {
			lower := strings.ToLower(key)
			if !policy.UseProfile && (strings.Contains(lower, "profile") || strings.Contains(lower, "personal_fact") || lower == "goal" || lower == "target_calories") {
				continue
			}
			if !policy.UseMemories && (strings.Contains(lower, "memor") || strings.Contains(lower, "personal_fact")) {
				continue
			}
			if !policy.UseDomainHistory && (strings.Contains(lower, "history") || strings.Contains(lower, "past_workout")) {
				continue
			}
			state[key] = value
		}
		facts.CurrentState = state
	}
	report := map[string]any{"application_facts": facts}
	if len(external) != 0 {
		report["untrusted_external_content"] = external
	}
	if includeRuntime {
		report["public_runtime"] = runtimeInfo.sorted()
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Direction == telegram.DirectionOutbound {
				report["last_message_correlation_id"] = history[i].CorrelationID
				report["last_message_domain_references"] = history[i].DomainReferences
				break
			}
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return "", err
	}
	clean, err := json.Marshal(sanitizeMetadata(raw))
	if err != nil {
		return "", err
	}
	return string(clean), nil
}
