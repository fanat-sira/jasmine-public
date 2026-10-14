package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/telegram"
)

// CommunicationProfile is private, user-owned reply data. It never enters a planner.
type CommunicationProfile struct {
	Version      int64             `json:"version"`
	Settings     map[string]string `json:"settings"`
	CustomRules  []string          `json:"custom_rules"`
	Instructions string            `json:"custom_instructions"`
	Personality  string            `json:"personality,omitempty"`
}
type PreferenceChanges struct {
	Set          map[string]string `json:"set,omitempty"`
	Forget       []string          `json:"forget,omitempty"`
	Rules        *[]string         `json:"custom_rules,omitempty"`
	Instructions *string           `json:"custom_instructions,omitempty"`
}
type AssistantPreferences struct {
	db               *pgxpool.Pool
	client           *openrouter.Client
	model            string
	ownerID          int64
	ownerPersonality string
}

func NewAssistantPreferences(db *pgxpool.Pool, client *openrouter.Client, model string) *AssistantPreferences {
	return &AssistantPreferences{db: db, client: client, model: model}
}
func (p *AssistantPreferences) ConfigureOwner(id int64, personality string) {
	p.ownerID = id
	p.ownerPersonality = personality
}
func emptyCommunicationProfile() CommunicationProfile {
	return CommunicationProfile{Settings: map[string]string{}, CustomRules: []string{}}
}

var preferenceEnums = map[string]string{
	"form_of_address": "formal informal", "language": "ru en auto", "tone": "neutral friendly playful professional",
	"verbosity": "concise balanced detailed", "humor": "none occasional frequent", "emoji": "allowed disabled",
	"formatting": "structured conversational", "grammatical_address": "masculine feminine neutral",
	"letter_case": "standard lowercase", "final_sentence_period": "allowed disabled", "long_dashes": "allowed disabled",
}

func validatePreferenceChanges(c PreferenceChanges) error {
	for key, value := range c.Set {
		values, ok := preferenceEnums[key]
		valid := false
		for _, allowed := range strings.Fields(values) {
			if value == allowed {
				valid = true
			}
		}
		if !ok || !valid {
			return errors.New("unknown communication preference or value")
		}
	}
	for _, key := range c.Forget {
		if _, ok := preferenceEnums[key]; !ok {
			return errors.New("unknown communication preference")
		}
	}
	if c.Instructions != nil && (!utf8.ValidString(*c.Instructions) || len(*c.Instructions) > 32768 || strings.ContainsRune(*c.Instructions, 0)) {
		return errors.New("custom instructions must be UTF-8 text up to 32 KiB")
	}
	if c.Rules != nil {
		if len(*c.Rules) > 16 {
			return errors.New("at most 16 custom style rules are allowed")
		}
		for _, rule := range *c.Rules {
			if !utf8.ValidString(rule) || len(rule) > 512 || strings.TrimSpace(rule) == "" || strings.ContainsRune(rule, 0) {
				return errors.New("invalid custom style rule")
			}
		}
	}
	return nil
}
func applyPreferenceChanges(profile CommunicationProfile, c PreferenceChanges) CommunicationProfile {
	if profile.Settings == nil {
		profile.Settings = map[string]string{}
	}
	for _, key := range c.Forget {
		delete(profile.Settings, key)
	}
	for key, value := range c.Set {
		profile.Settings[key] = value
	}
	if c.Rules != nil {
		profile.CustomRules = *c.Rules
	}
	if c.Instructions != nil {
		profile.Instructions = *c.Instructions
	}
	return profile
}
func (p *AssistantPreferences) Load(ctx context.Context, userID string) (CommunicationProfile, error) {
	profile := emptyCommunicationProfile()
	var raw []byte
	err := p.db.QueryRow(ctx, `SELECT version,profile FROM assistant_preferences WHERE user_id=$1`, userID).Scan(&profile.Version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return profile, nil
	}
	if err != nil {
		return profile, err
	}
	version := profile.Version
	if err = json.Unmarshal(raw, &profile); err != nil {
		return profile, err
	}
	profile.Version = version
	return profile, nil
}
func (p *AssistantPreferences) LoadByChat(ctx context.Context, chatID int64) (CommunicationProfile, error) {
	var userID string
	err := p.db.QueryRow(ctx, `SELECT user_id FROM telegram_users WHERE telegram_user_id=$1`, chatID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyCommunicationProfile(), nil
	}
	if err != nil {
		return CommunicationProfile{}, err
	}
	profile, err := p.Load(ctx, userID)
	if err == nil && p.ownerID == chatID && profile.Personality == "" {
		profile.Personality = p.ownerPersonality
	}
	return profile, err
}

// Mutate locks one user's row, deduplicates the Telegram update and records both
// versions in the same transaction. Reset/undo retain the owner's base personality.
func (p *AssistantPreferences) Mutate(ctx context.Context, e AssistantEvent, c PreferenceChanges, action, filename, proposalID string) (CommunicationProfile, error) {
	if action != "replace" && action != "import" && action != "reset" && action != "restore" {
		return CommunicationProfile{}, errors.New("invalid communication settings action")
	}
	if (action == "import") != (proposalID != "") {
		return CommunicationProfile{}, errors.New("imports require a specific confirmed proposal")
	}
	var proposalRef any
	if proposalID != "" {
		proposalRef = proposalID
	}

	if err := validatePreferenceChanges(c); err != nil {
		return CommunicationProfile{}, err
	}
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return CommunicationProfile{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO assistant_preferences(user_id) VALUES($1) ON CONFLICT DO NOTHING`, e.UserID); err != nil {
		return CommunicationProfile{}, err
	}
	current := emptyCommunicationProfile()
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT version,profile FROM assistant_preferences WHERE user_id=$1 FOR UPDATE`, e.UserID).Scan(&current.Version, &raw); err != nil {
		return current, err
	}
	version := current.Version
	if err = json.Unmarshal(raw, &current); err != nil {
		return current, err
	}
	current.Version = version
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_preference_revisions WHERE user_id=$1 AND source_update_id=$2)`, e.UserID, e.UpdateID).Scan(&duplicate); err != nil {
		return current, err
	}
	if duplicate {
		return current, tx.Commit(ctx)
	}
	if proposalID != "" {
		var changes []byte
		var base int64
		err = tx.QueryRow(ctx, `SELECT changes,base_version,source_filename FROM assistant_preference_proposals WHERE id=$1 AND user_id=$2 AND chat_id=$3 AND status='pending' AND expires_at>NOW() FOR UPDATE`, proposalID, e.UserID, e.ChatID).Scan(&changes, &base, &filename)
		if err != nil {
			return current, errors.New("proposal is missing, expired or belongs to another conversation")
		}
		if base != current.Version {
			return current, errors.New("preferences changed since preview; upload the file again")
		}
		if err = json.Unmarshal(changes, &c); err != nil {
			return current, err
		}
		if err = validatePreferenceChanges(c); err != nil {
			return current, err
		}
	}
	previous := raw
	switch action {
	case "reset":
		current.Settings = map[string]string{}
		current.CustomRules = []string{}
		current.Instructions = ""
	case "restore":
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT previous_profile FROM assistant_preference_revisions WHERE user_id=$1 ORDER BY version DESC LIMIT 1`, e.UserID).Scan(&old); err != nil {
			return current, errors.New("no previous communication settings")
		}
		if err = json.Unmarshal(old, &current); err != nil {
			return current, err
		}
	default:
		if action == "replace" && c.Rules != nil {
			rules := append([]string{}, current.CustomRules...)
			for _, rule := range *c.Rules {
				found := false
				for _, old := range rules {
					if old == rule {
						found = true
					}
				}
				if !found {
					rules = append(rules, rule)
				}
			}
			c.Rules = &rules
			if err = validatePreferenceChanges(c); err != nil {
				return current, err
			}
		}
		current = applyPreferenceChanges(current, c)
	}
	current.Version = version + 1
	updated, _ := json.Marshal(current)
	if _, err = tx.Exec(ctx, `UPDATE assistant_preferences SET version=$2,profile=$3,updated_at=NOW() WHERE user_id=$1`, e.UserID, current.Version, updated); err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assistant_preference_revisions(user_id,version,previous_profile,profile,source_update_id,source_message_id,source_filename,action,confirmed,source_proposal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, e.UserID, current.Version, previous, updated, e.UpdateID, e.MessageID, filename, action, proposalID != "", proposalRef); err != nil {
		return current, err
	}
	if proposalID != "" {
		if _, err = tx.Exec(ctx, `UPDATE assistant_preference_proposals SET status='applied' WHERE id=$1 AND user_id=$2`, proposalID, e.UserID); err != nil {
			return current, err
		}
	}
	return current, tx.Commit(ctx)
}
func preferenceReply(kind string, profile CommunicationProfile, performed bool) *telegram.IngestionResponse {
	// Do not expose the owner personality or internal application instructions.
	profile.Personality = ""
	reply := &telegram.ReplyContext{EventType: kind, CurrentState: map[string]any{"communication_preferences": profile}}
	if performed {
		reply.ActionsPerformed = []string{"assistant_preferences.update"}
		reply.Facts = []string{fmt.Sprintf("Communication preferences saved, version %d", profile.Version)}
	}
	return &telegram.IngestionResponse{ReplyContext: reply}
}

func (o *Orchestrator) AssistantPreferences() *AssistantPreferences { return o.preferences }
