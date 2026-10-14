package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"sort"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

type AssistantEventKind string

const (
	AssistantUserMessage    AssistantEventKind = "user_message"
	AssistantUserPhoto      AssistantEventKind = "user_photo"
	AssistantUserAlbum      AssistantEventKind = "user_album"
	AssistantUserDocument   AssistantEventKind = "user_document"
	AssistantReminderDue    AssistantEventKind = "reminder_due"
	AssistantScheduledEvent AssistantEventKind = "scheduled_event"
	AssistantExternalEvent  AssistantEventKind = "external_event"
)

// AssistantEvent contains provenance, not a domain-specific routing decision.
type AssistantEvent struct {
	ID                          string
	CorrelationID               string
	Kind                        AssistantEventKind
	OccurredAt                  time.Time
	UserID                      string
	ChatID, UpdateID, MessageID int64
	ReplyToMessageID            *int64
	Text                        string
	Image                       *telegram.ImageInput
	Album                       *telegram.AlbumInput
	Document                    *telegram.DocumentInput
	AttachmentObservations      []telegram.DomainReference
	ApprovedTools               map[string]bool
}

type JasmineRiskLevel string

const (
	RiskRead     JasmineRiskLevel = "read"
	RiskLowWrite JasmineRiskLevel = "low_write"
	RiskConfirm  JasmineRiskLevel = "confirm"
)

type JasmineToolSpec struct {
	Name        string `json:"name"`
	Skill       string `json:"skill"`
	Description string `json:"description"`
	// ArgumentSchema is a JSON Schema object exposed to the planner. ValidateArgs
	// performs application-owned decoding and validation before execution.
	ArgumentSchema       json.RawMessage  `json:"argument_schema"`
	RiskLevel            JasmineRiskLevel `json:"risk_level"`
	RequiresConfirmation bool             `json:"requires_confirmation"`
	IdempotencyScope     string           `json:"idempotency_scope,omitempty"`
}

type JasmineToolCall struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}
type JasmineObservation struct {
	Tool    string          `json:"tool"`
	Success bool            `json:"success"`
	Facts   string          `json:"facts,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}
type JasmineContext struct {
	Base         map[string]any
	SkillData    map[string]any
	Observations []JasmineObservation
}
type JasmineStep struct {
	Calls []JasmineToolCall
	Final *JasmineFinal
	// Call and Done keep older deterministic planners source-compatible.
	Call *JasmineToolCall
	Done bool
}
type JasmineFinal struct {
	Text      string
	EventType string
}
type JasminePlanner interface {
	Next(context.Context, AssistantEvent, *JasmineContext) (JasmineStep, error)
}
type JasmineContextProvider func(context.Context, AssistantEvent) (any, error)
type JasmineToolHandler func(context.Context, AssistantEvent, *JasmineContext, json.RawMessage) (*JasmineToolResult, error)
type JasmineArgValidator func(json.RawMessage) error
type JasmineToolResult struct {
	Facts       string
	Data        json.RawMessage
	OwnerUserID string
	Response    *telegram.IngestionResponse
	Done        bool
}
type JasmineTool struct {
	Spec         JasmineToolSpec
	ValidateArgs JasmineArgValidator
	Handle       JasmineToolHandler
}
type JasmineSkill interface {
	Name() string
	ContextProvider() JasmineContextProvider
	Tools() []JasmineTool
}
type jasmineRegisteredTool struct {
	spec     JasmineToolSpec
	context  JasmineContextProvider
	validate JasmineArgValidator
	handle   JasmineToolHandler
}
type JasmineTrace struct {
	CorrelationID, EventID, UserID string
	Step                           int
	Phase, Tool, Skill, Outcome    string
	Duration                       time.Duration
}
type JasmineTraceSink func(context.Context, JasmineTrace)
type JasmineRuntime struct {
	planner     JasminePlanner
	tools       map[string]jasmineRegisteredTool
	maxRounds   int
	base        JasmineContextProvider
	trace       JasmineTraceSink
	observeTurn func(context.Context, AssistantEvent) ([]string, error)
	preferences *AssistantPreferences
}

func NewJasmineRuntime(planner JasminePlanner, maxRounds int) *JasmineRuntime {
	if maxRounds <= 0 || maxRounds > 5 {
		maxRounds = 5
	}
	return &JasmineRuntime{planner: planner, tools: map[string]jasmineRegisteredTool{}, maxRounds: maxRounds}
}
func (r *JasmineRuntime) SetBaseContext(provider JasmineContextProvider) { r.base = provider }
func (r *JasmineRuntime) SetTurnObserver(observer func(context.Context, AssistantEvent) ([]string, error)) {
	r.observeTurn = observer
}
func (r *JasmineRuntime) SetPlanner(planner JasminePlanner)               { r.planner = planner }
func (r *JasmineRuntime) SetAssistantPreferences(p *AssistantPreferences) { r.preferences = p }
func (r *JasmineRuntime) SetTraceSink(sink JasmineTraceSink)              { r.trace = sink }
func (r *JasmineRuntime) emit(ctx context.Context, event AssistantEvent, step int, phase, tool, skill, outcome string, elapsed time.Duration) {
	if r.trace != nil {
		r.trace(ctx, JasmineTrace{CorrelationID: event.CorrelationID, EventID: event.ID, UserID: event.UserID, Step: step, Phase: phase, Tool: tool, Skill: skill, Outcome: outcome, Duration: elapsed})
	}
}
func (r *JasmineRuntime) RegisterSkill(skill JasmineSkill) error {
	if skill == nil || skill.Name() == "" {
		return fmt.Errorf("invalid Jasmine skill")
	}
	for _, tool := range skill.Tools() {
		if tool.Spec.Skill != skill.Name() {
			return fmt.Errorf("tool %q has wrong skill owner", tool.Spec.Name)
		}
		if err := r.register(tool.Spec, skill.ContextProvider(), tool.ValidateArgs, tool.Handle); err != nil {
			return err
		}
	}
	return nil
}
func (r *JasmineRuntime) Register(spec JasmineToolSpec, provider JasmineContextProvider, handler JasmineToolHandler) error {
	return r.register(spec, provider, nil, handler)
}
func (r *JasmineRuntime) register(spec JasmineToolSpec, provider JasmineContextProvider, validate JasmineArgValidator, handler JasmineToolHandler) error {
	if spec.Name == "" || spec.Skill == "" || handler == nil {
		return fmt.Errorf("invalid Jasmine tool registration")
	}
	if spec.RiskLevel == "" {
		spec.RiskLevel = RiskRead
	}
	if spec.RiskLevel != RiskRead && spec.RiskLevel != RiskLowWrite && spec.RiskLevel != RiskConfirm {
		return fmt.Errorf("invalid risk for %q", spec.Name)
	}
	if spec.RiskLevel != RiskRead && spec.IdempotencyScope == "" {
		return fmt.Errorf("write tool %q needs idempotency scope", spec.Name)
	}
	if spec.RiskLevel == RiskConfirm && !spec.RequiresConfirmation {
		return fmt.Errorf("confirm tool %q needs confirmation", spec.Name)
	}
	if len(spec.ArgumentSchema) == 0 {
		spec.ArgumentSchema = json.RawMessage(`{"type":"object","additionalProperties":false}`)
	}
	if !json.Valid(spec.ArgumentSchema) {
		return fmt.Errorf("invalid argument schema for %q", spec.Name)
	}
	if _, ok := r.tools[spec.Name]; ok {
		return fmt.Errorf("Jasmine tool %q is already registered", spec.Name)
	}
	r.tools[spec.Name] = jasmineRegisteredTool{spec: spec, context: provider, validate: validate, handle: handler}
	return nil
}
func (r *JasmineRuntime) ToolSpecs() []JasmineToolSpec {
	out := make([]JasmineToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, tool.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var (
	ErrJasmineRoundLimit           = errors.New("Jasmine tool round limit reached")
	ErrJasmineConfirmationRequired = errors.New("Jasmine tool requires confirmation")
	ErrJasmineRepeatedCall         = errors.New("Jasmine repeated identical tool call")
)

func (r *JasmineRuntime) Handle(ctx context.Context, event AssistantEvent) (*telegram.IngestionResponse, error) {
	if r == nil || r.planner == nil {
		return nil, fmt.Errorf("Jasmine planner is not configured")
	}
	if event.UserID == "" {
		return nil, fmt.Errorf("Jasmine event has no user")
	}
	if event.Kind != AssistantUserMessage && event.Kind != AssistantUserPhoto && event.Kind != AssistantUserAlbum && event.Kind != AssistantUserDocument {
		return nil, fmt.Errorf("unsupported Jasmine event %q", event.Kind)
	}
	if event.CorrelationID == "" {
		event.CorrelationID = correlation.GetCorrelationID(ctx)
	}
	if event.CorrelationID == "" {
		event.CorrelationID = event.ID
	}
	policy := telegram.ParseTurnContextPolicy(event.Text)
	ctx = telegram.WithTurnContextPolicy(ctx, policy)
	if r.preferences != nil {
		response, handled, err := r.preferences.Handle(ctx, event)
		if handled {
			if err != nil {
				return &telegram.IngestionResponse{ReplyContext: &telegram.ReplyContext{EventType: "communication_preferences_error", Errors: []string{"Could not confirm the communication settings change. Inspect current settings with /preferences; an import may need a new preview."}}}, nil
			}
			return response, nil
		}
	}
	state := &JasmineContext{Base: map[string]any{"context_policy": policy}, SkillData: map[string]any{}}
	if r.observeTurn != nil && (event.Kind == AssistantUserMessage || event.Kind == AssistantUserAlbum) {
		operations, err := r.observeTurn(ctx, event)
		if err != nil {
			return nil, fmt.Errorf("observe personal facts: %w", err)
		}
		if len(operations) > 0 {
			state.Base["personal_fact_operations"] = operations
		}
	}

	if r.base != nil {
		v, err := r.base(ctx, event)
		if err != nil {
			return nil, fmt.Errorf("base context: %w", err)
		}
		state.Base["profile"] = v
	}
	seen := map[[32]byte]struct{}{}
	count := 0
	for {
		start := time.Now()
		step, err := r.planner.Next(ctx, event, state)
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		r.emit(ctx, event, count+1, "planner", "", "", outcome, time.Since(start))
		if err != nil {
			return nil, err
		}
		if step.Final != nil {
			if len(step.Calls) != 0 || step.Call != nil {
				return nil, fmt.Errorf("final response cannot include tool calls")
			}
			r.emit(ctx, event, count, "final", "", "", "ok", 0)
			eventType := step.Final.EventType
			if eventType == "" {
				eventType = "general_chat"
			}
			reply := &telegram.ReplyContext{EventType: eventType}
			if ops, ok := state.Base["personal_fact_operations"]; ok {
				reply.CurrentState = map[string]any{"personal_fact_operations": ops}
				reply.ActionsPerformed = []string{"personal_knowledge.update"}
			}

			if len(state.Observations) != 0 {
				observations := make([]map[string]any, 0, len(state.Observations))
				for _, observation := range state.Observations {
					observations = append(observations, map[string]any{"tool": observation.Tool, "success": observation.Success, "data": observation.Data, "error": observation.Error})
				}
				if reply.CurrentState == nil {
					reply.CurrentState = map[string]any{}
				}
				reply.CurrentState["tool_observations"] = observations
			}
			return &telegram.IngestionResponse{ConfirmationHTML: html.EscapeString(step.Final.Text), ReplyContext: reply}, nil
		}
		if step.Done {
			return nil, fmt.Errorf("Jasmine planner finished without a final response")
		}
		calls := step.Calls
		if step.Call != nil {
			calls = append(calls, *step.Call)
		}
		if len(calls) == 0 {
			return nil, fmt.Errorf("Jasmine planner returned no action")
		}
		if count+len(calls) > r.maxRounds {
			return nil, ErrJasmineRoundLimit
		}
		type preparedCall struct {
			call  JasmineToolCall
			tool  jasmineRegisteredTool
			input json.RawMessage
			key   [32]byte
		}
		prepared := make([]preparedCall, 0, len(calls))
		pending := make(map[[32]byte]struct{}, len(calls))
		for _, call := range calls {
			tool, ok := r.tools[call.Name]
			if !ok {
				return nil, fmt.Errorf("unknown Jasmine tool %q", call.Name)
			}
			if (tool.spec.RequiresConfirmation || tool.spec.RiskLevel == RiskConfirm) && !event.ApprovedTools[call.Name] {
				return nil, fmt.Errorf("%w: %s", ErrJasmineConfirmationRequired, call.Name)
			}
			input := call.Input
			if len(input) == 0 {
				input = []byte(`{}`)
			}
			if err := validateJasmineInput(input, tool.spec.ArgumentSchema); err != nil {
				return nil, fmt.Errorf("invalid %s arguments: %w", call.Name, err)
			}
			if tool.validate != nil {
				if err := tool.validate(input); err != nil {
					return nil, fmt.Errorf("invalid %s arguments: %w", call.Name, err)
				}
			}
			var value any
			if err := json.Unmarshal(input, &value); err != nil {
				return nil, err
			}
			canonical, _ := json.Marshal(value)
			key := sha256.Sum256(append(append([]byte(call.Name), 0), canonical...))
			if _, ok := seen[key]; ok {
				return nil, fmt.Errorf("%w: %s", ErrJasmineRepeatedCall, call.Name)
			}
			if _, ok := pending[key]; ok {
				return nil, fmt.Errorf("%w: %s", ErrJasmineRepeatedCall, call.Name)
			}
			pending[key] = struct{}{}
			prepared = append(prepared, preparedCall{call: call, tool: tool, input: canonical, key: key})
		}
		for _, item := range prepared {
			call, tool, input := item.call, item.tool, item.input
			seen[item.key] = struct{}{}
			if tool.context != nil {
				if _, loaded := state.SkillData[tool.spec.Skill]; !loaded {
					v, err := tool.context(ctx, event)
					if err != nil {
						return nil, fmt.Errorf("load %s context: %w", tool.spec.Skill, err)
					}
					state.SkillData[tool.spec.Skill] = v
				}
			}
			count++
			start = time.Now()
			result, err := tool.handle(ctx, event, state, input)
			obs := JasmineObservation{Tool: call.Name, Success: err == nil && result != nil}
			if err != nil {
				obs.Error = err.Error()
			} else if result == nil {
				obs.Success = false
				obs.Error = "tool returned no result"
			} else if result.OwnerUserID != "" && result.OwnerUserID != event.UserID {
				obs.Success = false
				obs.Error = "tool result belongs to another user"
			} else {
				obs.Facts = result.Facts
				obs.Data = result.Data
			}
			state.Observations = append(state.Observations, obs)
			outcome = "ok"
			if !obs.Success {
				outcome = "error"
			}
			r.emit(ctx, event, count, "tool", call.Name, tool.spec.Skill, outcome, time.Since(start))
			if obs.Success && result.Done {
				return result.Response, nil
			}
		}
	}
}
func validateJasmineInput(input, schema json.RawMessage) error {
	var fields map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(input))
	if err := d.Decode(&fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("expected object")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	for _, reserved := range []string{"user_id", "chat_id", "update_id", "message_id", "owner_id"} {
		if _, ok := fields[reserved]; ok {
			return fmt.Errorf("identity field %q is owned by event", reserved)
		}
	}
	var s struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return err
	}
	for _, name := range s.Required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing %q", name)
		}
	}
	if s.AdditionalProperties != nil && !*s.AdditionalProperties {
		for name := range fields {
			if _, ok := s.Properties[name]; !ok {
				return fmt.Errorf("unknown field %q", name)
			}
		}
	}
	for name, raw := range fields {
		definition, ok := s.Properties[name]
		if !ok {
			continue
		}
		var rule struct {
			Type string            `json:"type"`
			Enum []json.RawMessage `json:"enum"`
		}
		if err := json.Unmarshal(definition, &rule); err != nil {
			return err
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		valid := true
		switch rule.Type {
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number":
			_, valid = value.(float64)
		case "integer":
			if n, ok := value.(float64); ok {
				valid = n == float64(int64(n))
			} else {
				valid = false
			}
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		}
		if !valid {
			return fmt.Errorf("field %q has wrong type", name)
		}
		if len(rule.Enum) > 0 {
			found := false
			for _, choice := range rule.Enum {
				if bytes.Equal(bytes.TrimSpace(choice), bytes.TrimSpace(raw)) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("field %q is outside enum", name)
			}
		}
	}
	return nil
}
func (r *JasmineRuntime) IngestMessage(ctx context.Context, userID string, chatID, updateID, messageID int64, replyID *int64, text string) (*telegram.IngestionResponse, error) {
	return r.Handle(ctx, AssistantEvent{ID: fmt.Sprintf("telegram:%d", updateID), Kind: AssistantUserMessage, OccurredAt: time.Now(), UserID: userID, ChatID: chatID, UpdateID: updateID, MessageID: messageID, ReplyToMessageID: replyID, Text: text})
}
func (r *JasmineRuntime) IngestImage(ctx context.Context, userID string, chatID, updateID, messageID int64, replyID *int64, caption string, image telegram.ImageInput) (*telegram.IngestionResponse, error) {
	return r.Handle(ctx, AssistantEvent{ID: fmt.Sprintf("telegram:%d", updateID), Kind: AssistantUserPhoto, OccurredAt: time.Now(), UserID: userID, ChatID: chatID, UpdateID: updateID, MessageID: messageID, ReplyToMessageID: replyID, Text: caption, Image: &image})
}
func (r *JasmineRuntime) IngestDocument(ctx context.Context, userID string, chatID, updateID, messageID int64, replyID *int64, doc telegram.DocumentInput) (*telegram.IngestionResponse, error) {
	return r.Handle(ctx, AssistantEvent{ID: fmt.Sprintf("telegram:%d", updateID), Kind: AssistantUserDocument, OccurredAt: time.Now(), UserID: userID, ChatID: chatID, UpdateID: updateID, MessageID: messageID, ReplyToMessageID: replyID, Text: doc.Caption, Document: &doc})
}
func (r *JasmineRuntime) IngestAlbum(ctx context.Context, userID string, chatID int64, album telegram.AlbumInput) (*telegram.IngestionResponse, error) {
	if len(album.Items) == 0 {
		return nil, fmt.Errorf("album has no images")
	}
	first := album.Items[0]
	return r.Handle(ctx, AssistantEvent{ID: fmt.Sprintf("album:%s:%d", album.MediaGroupID, first.UpdateID), Kind: AssistantUserAlbum, OccurredAt: time.Now(), UserID: userID, ChatID: chatID, UpdateID: first.UpdateID, MessageID: first.MessageID, Text: album.Caption, Album: &album})
}

// CompatibilityPlanner only preserves the specialized image path.
type CompatibilityPlanner struct{}

func (CompatibilityPlanner) Next(_ context.Context, event AssistantEvent, state *JasmineContext) (JasmineStep, error) {
	if len(state.Observations) > 0 && state.Observations[len(state.Observations)-1].Success {
		return JasmineStep{Final: &JasmineFinal{Text: state.Observations[len(state.Observations)-1].Facts}}, nil
	}
	switch event.Kind {
	case AssistantUserMessage:
		return JasmineStep{}, fmt.Errorf("text requires a registered Coach or specialized tool")
	case AssistantUserPhoto:
		return JasmineStep{Call: &JasmineToolCall{Name: "legacy.handle_photo"}}, nil
	default:
		return JasmineStep{}, fmt.Errorf("unsupported Jasmine event %q", event.Kind)
	}
}
func NewJasmineRuntimeWithLegacy(legacy *Orchestrator) (*JasmineRuntime, error) {
	if legacy == nil || legacy.coachAgent == nil || legacy.db == nil || legacy.telegramRepo == nil {
		return nil, fmt.Errorf("Coach and PostgreSQL must be configured for Jasmine runtime")
	}
	r := NewJasmineRuntime(NewCoachFallbackPlanner(legacy), 5)
	r.SetAssistantPreferences(legacy.preferences)
	r.SetTurnObserver(func(ctx context.Context, e AssistantEvent) ([]string, error) {
		if legacy.knowledge == nil {
			return nil, nil
		}
		if _, isMarkdown := profileMarkdownText(e.Text); isMarkdown {
			return nil, nil
		}
		if _, isJSON := profileJSONText(e.Text); isJSON {
			return nil, nil
		}
		operations, err := legacy.knowledge.Observe(ctx, e.UserID, e.UpdateID, e.MessageID, e.Text)
		if err != nil {
			slog.Warn("personal fact extraction skipped", "update_id", e.UpdateID, "error", err)
			return nil, nil
		}
		return operations, nil
	})
	r.SetBaseContext(func(ctx context.Context, e AssistantEvent) (any, error) {
		var goal, timezone string
		if err := legacy.db.QueryRow(ctx, `SELECT COALESCE(goal,''),COALESCE(timezone,'UTC') FROM users WHERE id=$1`, e.UserID).Scan(&goal, &timezone); err != nil {
			return nil, err
		}
		policy := telegram.TurnContextPolicyFromContext(ctx)
		if !policy.UseProfile {
			goal = ""
		}
		var facts []PersonalFact
		if legacy.knowledge != nil && policy.UseProfile && policy.UseMemories {
			selected, err := legacy.knowledge.Relevant(ctx, e.UserID, e.Text)
			if err != nil {
				return nil, err
			}
			facts = selected
		}
		recent := make([]CoachRecentMessage, 0, 50)
		if policy.UseConversationHistory && policy.UseDomainHistory && legacy.telegramRepo != nil && e.ChatID != 0 {
			messages, err := legacy.telegramRepo.ListMessages(ctx, e.ChatID, 51)
			if err != nil {
				return nil, err
			}
			for _, m := range messages {
				if m.MessageID == e.MessageID || m.Text == nil {
					continue
				}
				if m.Direction != telegram.DirectionInbound && m.Direction != telegram.DirectionOutbound {
					continue
				}
				recent = append(recent, CoachRecentMessage{MessageID: m.MessageID, Direction: m.Direction, Text: *m.Text})
			}
			if len(recent) > 50 {
				recent = recent[len(recent)-50:]
			}
		}
		return struct {
			Goal          string               `json:"goal,omitempty"`
			Timezone      string               `json:"timezone"`
			PersonalFacts []PersonalFact       `json:"personal_facts,omitempty"`
			Recent        []CoachRecentMessage `json:"recent_messages,omitempty"`
		}{goal, timezone, facts, recent}, nil
	})
	r.SetTraceSink(func(_ context.Context, t JasmineTrace) {
		slog.Info("jasmine_trace", "correlation_id", t.CorrelationID, "event_id", t.EventID, "step", t.Step, "phase", t.Phase, "tool", t.Tool, "skill", t.Skill, "outcome", t.Outcome, "duration_ms", t.Duration.Milliseconds())
	})
	err := r.Register(JasmineToolSpec{Name: "legacy.handle_specialized_text", Skill: "legacy", Description: "Only profile, nutrition and pending image/old ingestion clarifications; never a normal Coach turn", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update"}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		allowed, err := isSpecializedText(ctx, legacy, e, nil)
		if err != nil {
			return nil, err
		}
		if !allowed {
			c, err := legacy.coachAgent.build(ctx, e.UserID, e.ChatID, e.MessageID, e.ReplyToMessageID)
			if err != nil {
				return nil, err
			}
			allowed, err = isSpecializedText(ctx, legacy, e, c)
			if err != nil {
				return nil, err
			}
		}
		if !allowed {
			return nil, fmt.Errorf("ordinary Coach text cannot use legacy adapter")
		}
		res, err := executeSpecializedText(ctx, legacy, e)
		if err != nil {
			return nil, err
		}
		return &JasmineToolResult{Response: res, Done: true, OwnerUserID: e.UserID}, nil
	})
	if err != nil {
		return nil, err
	}
	err = r.Register(JasmineToolSpec{Name: "legacy.handle_photo", Skill: "legacy", Description: "Compatibility image handler", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update"}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		if e.Image == nil {
			return nil, fmt.Errorf("photo event has no image")
		}
		res, err := legacy.IngestImage(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.ReplyToMessageID, e.Text, *e.Image)
		return &JasmineToolResult{Response: res, Done: true}, err
	})
	if err != nil {
		return nil, err
	}
	err = r.Register(JasmineToolSpec{Name: "legacy.handle_album", Skill: "legacy", Description: "Analyze one Telegram album as a logical turn", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_media_group"}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		if e.Album == nil {
			return nil, fmt.Errorf("album event has no attachments")
		}
		res, err := legacy.IngestAlbum(ctx, e.UserID, e.ChatID, *e.Album)
		return &JasmineToolResult{Response: res, Done: true, OwnerUserID: e.UserID}, err
	})
	if err != nil {
		return nil, err
	}
	err = r.Register(JasmineToolSpec{Name: "legacy.handle_document", Skill: "legacy", Description: "Ingest Telegram text and JSON documents; never analyze as photo", RiskLevel: RiskLowWrite, IdempotencyScope: "telegram_update"}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
		if e.Document == nil {
			return nil, fmt.Errorf("document event has no document")
		}
		res, err := legacy.IngestDocument(ctx, e.UserID, e.ChatID, e.UpdateID, e.MessageID, e.ReplyToMessageID, *e.Document)
		return &JasmineToolResult{Response: res, Done: true, OwnerUserID: e.UserID}, err
	})
	if err != nil {
		return nil, err
	}
	if legacy.knowledge != nil {
		err = r.Register(JasmineToolSpec{Name: "personal.read", Skill: "personal", Description: "Read relevant current personal facts, including identity and profile information", RiskLevel: RiskRead}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
			facts, err := legacy.knowledge.Relevant(ctx, e.UserID, e.Text)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(map[string]any{"current_facts": facts})
			if err != nil {
				return nil, err
			}
			return &JasmineToolResult{Facts: "personal_facts_read", Data: data, OwnerUserID: e.UserID}, nil
		})
		if err != nil {
			return nil, err
		}
		err = r.Register(JasmineToolSpec{Name: "personal.list_projects", Skill: "personal", Description: "List current named project entities from personal knowledge", RiskLevel: RiskRead}, nil, func(ctx context.Context, e AssistantEvent, _ *JasmineContext, _ json.RawMessage) (*JasmineToolResult, error) {
			facts, err := legacy.knowledge.ListProjects(ctx, e.UserID)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(map[string]any{"projects": facts})
			if err != nil {
				return nil, err
			}
			return &JasmineToolResult{Facts: "projects_read", Data: data, OwnerUserID: e.UserID}, nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := r.RegisterSkill(NewMealSkill(legacy)); err != nil {
		return nil, err
	}
	if legacy.coachAgent != nil {
		if err := r.RegisterSkill(NewCoachSkill(legacy.coachAgent)); err != nil {
			return nil, err
		}
	}
	return r, nil
}
