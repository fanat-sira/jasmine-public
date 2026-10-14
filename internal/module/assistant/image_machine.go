package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"github.com/jackc/pgx/v5"
)

type pendingMachineImage struct {
	Draft          GymMachineDisplayDraft `json:"draft"`
	Caption        string                 `json:"caption"`
	ImageUpdateID  int64                  `json:"image_update_id"`
	ImageMessageID int64                  `json:"image_message_id"`
	FileUniqueID   string                 `json:"file_unique_id"`
	FileID         string                 `json:"file_id"`
	CorrelationID  string                 `json:"correlation_id"`
}

var machineDatePhrase = regexp.MustCompile(`(?i)\d{1,2}\s+(января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)`)

func machineFromCaption(caption string) string {
	s := strings.ToLower(strings.TrimSpace(caption))
	s = machineDatePhrase.ReplaceAllString(s, "")
	for _, word := range []string{"позавчера", "вчера", "сегодня", "после тренировки", "понедельник", "вторник", "среду", "четверг", "пятницу", "субботу", "воскресенье", "за ", "это ", "утром", "вечером"} {
		s = strings.ReplaceAll(s, word, "")
	}
	return strings.TrimSpace(s)
}

func validMachineDraft(d *GymMachineDisplayDraft) bool {
	if d == nil || d.Confidence < 0.65 {
		return false
	}
	if d.DurationSeconds != nil && (*d.DurationSeconds < 0 || *d.DurationSeconds > 86400) {
		return false
	}
	if d.DistanceKM != nil && (*d.DistanceKM < 0 || *d.DistanceKM > 1000) {
		return false
	}
	if d.Calories != nil && (*d.Calories < 0 || *d.Calories > 10000) {
		return false
	}
	return d.DurationSeconds != nil || d.DistanceKM != nil || d.Calories != nil
}
func (o *Orchestrator) saveMachinePending(ctx context.Context, userID string, chatID int64, p pendingMachineImage, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	if o.telegramRepo == nil {
		return nil, fmt.Errorf("conversation state unavailable")
	}
	state, err := o.telegramRepo.GetConversationState(ctx, userID)
	if err != nil && err != telegram.ErrNotFound {
		return nil, err
	}
	if state == nil {
		state = &telegram.ConversationState{UserID: userID, ChatID: chatID}
	}
	if state.PendingClarification != nil && state.PendingClarification.Type != "machine_display_needs_machine" {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "pending_clarification", Clarification: []string{state.PendingClarification.Type}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	state.ChatID = chatID
	state.PendingClarification = &telegram.PendingClarification{Type: "machine_display_needs_machine", OriginalQuery: p.Caption, Context: b, CreatedAt: time.Now()}
	if err = o.telegramRepo.UpsertConversationState(ctx, state); err != nil {
		return nil, err
	}
	return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "machine_name_needed", Clarification: []string{"machine_name"}, CurrentState: map[string]any{"metrics": p.Draft}, ToolResults: refs}, DomainReferences: refs}, nil
}
func (o *Orchestrator) logMachine(ctx context.Context, userID string, chatID, replyUpdateID, replyMessageID int64, name string, p pendingMachineImage, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	if !validMachineDraft(&p.Draft) {
		return &telegram.IngestionResponse{NeedsClarify: true, ReplyContext: &telegram.ReplyContext{EventType: "machine_reading_unclear", Clarification: []string{"metrics_not_readable"}, ToolResults: refs}, DomainReferences: refs}, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return o.saveMachinePending(ctx, userID, chatID, p, refs)
	}
	at, err := o.imageLocalTime(ctx, userID, p.Caption)
	if err != nil {
		return nil, err
	}
	historical := !sameLocalDay(at, time.Now().In(at.Location()))
	metrics := workout.MachineMetrics{MachineName: name, At: at, Historical: historical, DurationSeconds: p.Draft.DurationSeconds, DistanceKM: p.Draft.DistanceKM, Calories: p.Draft.Calories, HeartRateBPM: p.Draft.HeartRateBPM, SpeedKMH: p.Draft.SpeedKMH, Resistance: p.Draft.Resistance, Level: p.Draft.Level, InclinePercent: p.Draft.InclinePercent, Watts: p.Draft.Watts, RPM: p.Draft.RPM, Steps: p.Draft.Steps, Floors: p.Draft.Floors, Strokes: p.Draft.Strokes, Repetitions: p.Draft.Repetitions, LoadKG: p.Draft.LoadKG, FileUniqueID: p.FileUniqueID, SourceUpdateID: p.ImageUpdateID, SourceMessageID: p.ImageMessageID}
	result, err := o.imageAction(ctx, userID, chatID, replyUpdateID, replyMessageID, &refs, func(tx pgx.Tx) error {
		workoutID, err := o.ingestionPipe.ApplyMachineMetricsTx(ctx, tx, userID, metrics)
		if err != nil {
			return err
		}
		refs = append(refs, telegram.DomainReference{Type: "workout", ID: workoutID, Extra: map[string]any{"machine_name": name, "metrics": metrics}})
		state, err := o.telegramRepo.GetConversationState(ctx, userID)
		if err != nil && err != telegram.ErrNotFound {
			return err
		}
		if state != nil && state.PendingClarification != nil && state.PendingClarification.Type == "machine_display_needs_machine" {
			state.PendingClarification = nil
			return o.telegramRepo.UpsertConversationStateTx(ctx, tx, state)
		}
		return nil
	})
	if errors.Is(err, workout.ErrMachineUnresolved) {
		return o.saveMachinePending(ctx, userID, chatID, p, refs)
	}
	return result, err
}

func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
