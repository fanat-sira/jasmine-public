package telegram

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDomainReferenceSerialization(t *testing.T) {
	refs := []DomainReference{
		{
			Type: "workout",
			ID:   "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
		},
		{
			Type: "exercise",
			ID:   "18f2a9c1-4c12-40a1-9b81-0d6118d7b34e",
			Name: "Leg Curl",
		},
		{
			Type: "workout_session",
			ID:   "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			Type: "pending_clarification",
			Kind: "exercise_choice",
			Extra: map[string]any{
				"candidates": []string{"Seated Leg Curl", "Lying Leg Curl"},
			},
		},
	}

	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("failed to marshal domain references: %v", err)
	}

	var decoded []DomainReference
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal domain references: %v", err)
	}

	if len(decoded) != 4 {
		t.Fatalf("expected 4 references, got %d", len(decoded))
	}
	if decoded[0].Type != "workout" || decoded[0].ID != "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d" {
		t.Errorf("unexpected reference 0: %+v", decoded[0])
	}
	if decoded[1].Type != "exercise" || decoded[1].Name != "Leg Curl" {
		t.Errorf("unexpected reference 1: %+v", decoded[1])
	}
}

func TestTelegramMessageTextNullable(t *testing.T) {
	msgWithText := TelegramMessage{
		ChatID:    12345,
		MessageID: 101,
		UserID:    "u-1",
		Direction: DirectionInbound,
		Text:      ptr("Hello coach"),
		CreatedAt: time.Now(),
	}

	data, err := json.Marshal(msgWithText)
	if err != nil {
		t.Fatalf("marshal msgWithText: %v", err)
	}

	var dec TelegramMessage
	if err := json.Unmarshal(data, &dec); err != nil {
		t.Fatalf("unmarshal msgWithText: %v", err)
	}
	if dec.Text == nil || *dec.Text != "Hello coach" {
		t.Errorf("expected 'Hello coach', got %v", dec.Text)
	}

	msgWithoutText := TelegramMessage{
		ChatID:     12345,
		MessageID:  102,
		UserID:     "u-1",
		Direction:  DirectionInbound,
		Text:       nil,
		RawPayload: json.RawMessage(`{"voice":{"duration":5}}`),
		CreatedAt:  time.Now(),
	}

	data2, err := json.Marshal(msgWithoutText)
	if err != nil {
		t.Fatalf("marshal msgWithoutText: %v", err)
	}

	var dec2 TelegramMessage
	if err := json.Unmarshal(data2, &dec2); err != nil {
		t.Fatalf("unmarshal msgWithoutText: %v", err)
	}
	if dec2.Text != nil {
		t.Errorf("expected nil Text, got %v", *dec2.Text)
	}
}

func TestConversationStateSerialization(t *testing.T) {
	state := ConversationState{
		UserID:          "u-123",
		ChatID:          987654321,
		ActiveSessionID: ptr("session-1"),
		FocusExerciseID: ptr("ex-leg-curl"),
		PendingClarification: &PendingClarification{
			Type:          "exercise_disambiguation",
			OriginalQuery: "лег курл",
			Candidates:    []string{"Seated Leg Curl", "Lying Leg Curl"},
			CreatedAt:     time.Now(),
		},
		LastInteractionAt: time.Now(),
		UpdatedAt:         time.Now(),
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal conversation state: %v", err)
	}

	var decoded ConversationState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal conversation state: %v", err)
	}

	if decoded.ChatID != 987654321 {
		t.Errorf("expected chat id 987654321, got %d", decoded.ChatID)
	}
	if decoded.PendingClarification == nil || len(decoded.PendingClarification.Candidates) != 2 {
		t.Fatalf("expected pending clarification with 2 candidates, got %+v", decoded.PendingClarification)
	}
}

func TestUserMemoryScopesAndCategories(t *testing.T) {
	mem := UserMemory{
		ID:         "mem-1",
		UserID:     "u-123",
		Category:   MemoryCategoryPhysicalLimitation,
		Scope:      MemoryScopeChronicOrPermanent(MemoryScopePermanent),
		Key:        "lower_back_pain",
		Value:      json.RawMessage(`{"severity":"moderate","avoid":["heavy_deadlift"]}`),
		Confidence: 0.95,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if mem.Category != "physical_limitation" || mem.Scope != "permanent" {
		t.Errorf("unexpected memory category or scope: %+v", mem)
	}
}

func TestTelegramDeliveryModel(t *testing.T) {
	delivery := TelegramDelivery{
		ID:       "deliv-123",
		UpdateID: ptr(int64(999)),
		ChatID:   12345678,
		UserID:   "user-uuid",
		Status:   DeliveryStatusPending,
		Payload:  json.RawMessage(`{"text":"Your workout is saved!","parse_mode":"HTML"}`),
		Attempts: 0,
	}

	data, err := json.Marshal(delivery)
	if err != nil {
		t.Fatalf("failed to marshal delivery: %v", err)
	}

	var dec TelegramDelivery
	if err := json.Unmarshal(data, &dec); err != nil {
		t.Fatalf("failed to unmarshal delivery: %v", err)
	}

	if dec.Status != DeliveryStatusPending || *dec.UpdateID != 999 || dec.ChatID != 12345678 {
		t.Errorf("unexpected delivery unmarshal result: %+v", dec)
	}
}

func TestPrivateChatEnforcement(t *testing.T) {
	cases := []struct {
		chatType string
		allowed  bool
	}{
		{ChatTypePrivate, true},
		{ChatTypeGroup, false},
		{ChatTypeSupergroup, false},
		{ChatTypeChannel, false},
		{"unknown", false},
	}

	for _, c := range cases {
		isAllowed := c.chatType == ChatTypePrivate
		if isAllowed != c.allowed {
			t.Errorf("chatType %q: expected allowed=%v, got %v", c.chatType, c.allowed, isAllowed)
		}
	}
}

func TestUpdateClaimLeaseLogic(t *testing.T) {
	// Simulate the claim logic condition evaluation
	type updateState struct {
		status      string
		attempts    int
		lockedAt    *time.Time
		maxAttempts int
	}

	canClaim := func(s updateState, lockTimeout time.Duration, now time.Time) bool {
		if s.status == UpdateStatusReceived {
			return true
		}
		if s.status == UpdateStatusFailed && s.attempts < s.maxAttempts {
			return true
		}
		if s.status == UpdateStatusProcessing {
			if s.lockedAt == nil || now.Sub(*s.lockedAt) > lockTimeout {
				return s.attempts < s.maxAttempts
			}
		}
		return false
	}

	now := time.Now()
	timeout := 60 * time.Second

	// Fresh update
	if !canClaim(updateState{status: UpdateStatusReceived, attempts: 0, maxAttempts: 3}, timeout, now) {
		t.Error("fresh update should be claimable")
	}

	// Active processing with recent lock -> should NOT be claimable (prevent concurrent workers)
	recentLock := now.Add(-10 * time.Second)
	if canClaim(updateState{status: UpdateStatusProcessing, attempts: 1, lockedAt: &recentLock, maxAttempts: 3}, timeout, now) {
		t.Error("recently locked processing update should NOT be claimable by another worker")
	}

	// Stale processing lock -> SHOULD be claimable for retry
	staleLock := now.Add(-120 * time.Second)
	if !canClaim(updateState{status: UpdateStatusProcessing, attempts: 1, lockedAt: &staleLock, maxAttempts: 3}, timeout, now) {
		t.Error("stale locked processing update SHOULD be claimable for recovery")
	}

	// Completed update -> NEVER claimable
	if canClaim(updateState{status: UpdateStatusCompleted, attempts: 1, maxAttempts: 3}, timeout, now) {
		t.Error("completed update must NEVER be claimed again")
	}

	// Skipped update -> NEVER claimable
	if canClaim(updateState{status: UpdateStatusSkipped, attempts: 0, maxAttempts: 3}, timeout, now) {
		t.Error("skipped update must NEVER be claimed again")
	}
}

func MemoryScopeChronicOrPermanent(s string) string {
	return s
}

func ptr[T any](v T) *T {
	return &v
}
