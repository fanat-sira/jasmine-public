package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"jasmine/internal/pkg/correlation"
)

func TestPostgreSQL_UpdateClaimConcurrency(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tgRepo := telegram.NewRepository(pool)

	updateID := int64(987654)
	corrID := correlation.GenerateUpdateCorrelationID(updateID)

	// Save received update
	err := tgRepo.SaveUpdate(ctx, &telegram.TelegramUpdate{
		UpdateID:       updateID,
		CorrelationID:  corrID,
		ChatID:         123456,
		TelegramUserID: 123456,
		Status:         telegram.UpdateStatusReceived,
		RawPayload:     []byte(`{"update_id":987654}`),
		CreatedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("failed saving update: %v", err)
	}

	// Launch 10 concurrent workers attempting to claim the update
	concurrency := 10
	var wg sync.WaitGroup
	claims := make([]*telegram.TelegramUpdate, concurrency)
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", idx)
			u, err := tgRepo.ClaimUpdate(ctx, updateID, workerID, 3, 30*time.Second)
			claims[idx] = u
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	successfulClaims := 0
	conflictErrors := 0
	for i := 0; i < concurrency; i++ {
		if claims[i] != nil && errs[i] == nil {
			successfulClaims++
		} else if errors.Is(errs[i], telegram.ErrUpdateConflict) {
			conflictErrors++
		}
	}

	if successfulClaims != 1 {
		t.Errorf("expected EXACTLY 1 successful claim, got %d", successfulClaims)
	}
	if conflictErrors != concurrency-1 {
		t.Errorf("expected %d conflict errors, got %d", concurrency-1, conflictErrors)
	}
}

func TestPostgreSQL_ActiveSessionConcurrencyLockAndUniqueIndex(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tgRepo := telegram.NewRepository(pool)

	// Provision user
	tgUser, err := tgRepo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{
		TelegramUserID: 555666,
		Username:       stringPtr("session_racer"),
	})
	if err != nil {
		t.Fatalf("failed creating user: %v", err)
	}

	// 2 concurrent transactions attempting to create an active proposed session
	concurrency := 2
	var wg sync.WaitGroup
	insertedSessions := make([]*telegram.WorkoutSession, concurrency)
	insertErrors := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				insertErrors[idx] = err
				return
			}
			defer tx.Rollback(ctx)

			// Lock user row first to serialize session creation
			var lockedUID string
			err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1 FOR UPDATE`, tgUser.UserID).Scan(&lockedUID)
			if err != nil {
				insertErrors[idx] = err
				return
			}

			// Check for active session
			var existingCount int
			err = tx.QueryRow(ctx,
				`SELECT count(*) FROM workout_sessions WHERE user_id = $1 AND status IN ('proposed', 'in_progress', 'accepted')`,
				tgUser.UserID,
			).Scan(&existingCount)
			if err != nil {
				insertErrors[idx] = err
				return
			}

			if existingCount > 0 {
				insertErrors[idx] = errors.New("active session already exists")
				return
			}

			// Insert session
			var sess telegram.WorkoutSession
			proposalJSON, _ := json.Marshal(map[string]any{"title": "Race Session"})
			err = tx.QueryRow(ctx,
				`INSERT INTO workout_sessions (user_id, status, proposal_data, expires_at)
				 VALUES ($1, 'proposed', $2, NOW() + INTERVAL '4 hours')
				 RETURNING id, user_id, status, proposal_data, created_at, updated_at, expires_at`,
				tgUser.UserID, proposalJSON,
			).Scan(
				&sess.ID, &sess.UserID, &sess.Status, &sess.ProposalData,
				&sess.CreatedAt, &sess.UpdatedAt, &sess.ExpiresAt,
			)
			if err != nil {
				insertErrors[idx] = err
				return
			}

			if err := tx.Commit(ctx); err != nil {
				insertErrors[idx] = err
				return
			}
			insertedSessions[idx] = &sess
		}(i)
	}
	wg.Wait()

	successes := 0
	for i := 0; i < concurrency; i++ {
		if insertedSessions[i] != nil && insertErrors[i] == nil {
			successes++
		}
	}

	if successes != 1 {
		t.Errorf("expected EXACTLY 1 active session created, got %d", successes)
	}

	// Verify total active sessions in DB is strictly 1
	var totalActive int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM workout_sessions WHERE user_id = $1 AND status IN ('proposed', 'in_progress')`,
		tgUser.UserID,
	).Scan(&totalActive)

	if totalActive != 1 {
		t.Errorf("expected 1 active session row in PostgreSQL, got %d", totalActive)
	}
}

func TestPostgreSQL_IncrementalWorkoutAccumulation(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tgRepo := telegram.NewRepository(pool)
	workoutRepo := workout.NewRepository(pool)
	exerciseRepo := exercise.NewRepository(pool)

	tgUser, _ := tgRepo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{
		TelegramUserID: 777888,
	})

	// Seed telegram_updates 101 and 102 for foreign key provenance
	_, err := pool.Exec(ctx,
		`INSERT INTO telegram_updates (update_id, chat_id, telegram_user_id, status, raw_payload)
		 VALUES (101, 777888, 777888, 'completed', '{}'),
		        (102, 777888, 777888, 'completed', '{}')`)
	if err != nil {
		t.Fatalf("failed seeding telegram updates: %v", err)
	}

	// Seed leg curl exercise
	exLegCurl := &exercise.Exercise{
		Name:        "Seated Leg Curl",
		Type:        "strength",
		MuscleGroup: "Hamstrings",
	}
	if err := exerciseRepo.Create(ctx, exLegCurl); err != nil {
		t.Fatalf("failed creating exercise: %v", err)
	}

	// Update 1: First performed log creates workout and Leg Curl sets 1 & 2
	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed begin tx1: %v", err)
	}
	w := &workout.Workout{
		UserID:      tgUser.UserID,
		StartedAt:   time.Now(),
		WorkoutType: "strength",
		Source:      "telegram",
	}
	r12 := 12
	w23 := 23.0
	inputs1 := []workout.WorkoutExerciseInput{
		{
			ExerciseID: exLegCurl.ID,
			OrderIndex: 1,
			Sets: []workout.Set{
				{SetNumber: 1, Reps: &r12, WeightKg: &w23},
				{SetNumber: 2, Reps: &r12, WeightKg: &w23},
			},
		},
	}
	if err := workoutRepo.CreateTx(ctx, tx1, w, inputs1); err != nil {
		t.Fatalf("failed create workout: %v", err)
	}

	if _, err := tx1.Exec(ctx,
		`INSERT INTO workout_ingestion_increments (workout_id, telegram_update_id, operation, summary)
		 VALUES ($1, 101, 'exercise_added', 'Seated Leg Curl 2x12 23kg')`,
		w.ID,
	); err != nil {
		t.Fatalf("failed inserting increment 101: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("failed commit tx1: %v", err)
	}

	// Update 2: Appending a 3rd set of Leg Curl to the SAME workout
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed begin tx2: %v", err)
	}
	w27 := 27.0
	setsJSON, _ := json.Marshal([]workout.Set{
		{SetNumber: 1, Reps: &r12, WeightKg: &w23},
		{SetNumber: 2, Reps: &r12, WeightKg: &w23},
		{SetNumber: 3, Reps: &r12, WeightKg: &w27},
	})
	if _, err := tx2.Exec(ctx,
		`UPDATE workout_exercises SET sets_data = $1 WHERE workout_id = $2 AND exercise_id = $3`,
		setsJSON, w.ID, exLegCurl.ID,
	); err != nil {
		t.Fatalf("failed updating sets: %v", err)
	}
	if _, err := tx2.Exec(ctx,
		`INSERT INTO workout_ingestion_increments (workout_id, telegram_update_id, operation, summary)
		 VALUES ($1, 102, 'sets_appended', 'Seated Leg Curl set 3 12x27kg')`,
		w.ID,
	); err != nil {
		t.Fatalf("failed inserting increment 102: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("failed commit tx2: %v", err)
	}

	// Verify DB state: exactly 1 workout, 1 workout_exercise, 3 sets
	detail, err := workoutRepo.GetByID(ctx, w.ID, tgUser.UserID)
	if err != nil {
		t.Fatalf("failed getting workout detail (workout_id=%s, user_id=%s): %v", w.ID, tgUser.UserID, err)
	}

	if len(detail.Exercises) != 1 {
		t.Fatalf("expected 1 logical exercise, got %d", len(detail.Exercises))
	}
	if len(detail.Exercises[0].Sets) != 3 {
		t.Fatalf("expected 3 sets accumulated, got %d", len(detail.Exercises[0].Sets))
	}

	// Verify audit increments
	var increments int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workout_ingestion_increments WHERE workout_id=$1`, w.ID).Scan(&increments); err != nil {
		t.Fatal(err)
	}
	if increments != 2 {
		t.Fatalf("expected 2 increment audit entries, got %d", increments)
	}

}

func TestPostgreSQL_MemorySupersessionAndReversals(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tgRepo := telegram.NewRepository(pool)

	tgUser, _ := tgRepo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{
		TelegramUserID: 999111,
	})

	// Seed telegram_update 301 for provenance FK
	_, err := pool.Exec(ctx,
		`INSERT INTO telegram_updates (update_id, chat_id, telegram_user_id, status, raw_payload)
		 VALUES (301, 999111, 999111, 'completed', '{}')`)
	if err != nil {
		t.Fatalf("failed seeding telegram update 301: %v", err)
	}

	// 1. Initial conditional preference
	mem1 := &telegram.UserMemory{
		UserID:         tgUser.UserID,
		Category:       "preference",
		Scope:          "conditional",
		Key:            "avoid_graviton",
		Value:          json.RawMessage(`"Исключить гравитон"`),
		Confidence:     0.95,
		SourceText:     "пока не давай гравитон",
		SourceUpdateID: int64Ptr(301),
	}
	if err := tgRepo.SaveMemory(ctx, mem1); err != nil {
		t.Fatalf("failed saving initial memory (user_id=%s, key=%s): %v", tgUser.UserID, mem1.Key, err)
	}

	// Query active memories
	active, err := tgRepo.ListMemories(ctx, tgUser.UserID, nil, nil)
	if err != nil || len(active) != 1 {
		t.Fatalf("expected 1 active memory, got %d: %v", len(active), err)
	}

	// 2. Reversal message
	tx, _ := pool.Begin(ctx)
	err = tgRepo.SupersedeMemoryTx(ctx, tx, tgUser.UserID, "preference", "avoid_graviton", "")
	if err != nil {
		t.Fatalf("failed superseding memory: %v", err)
	}
	_ = tx.Commit(ctx)

	// Query active memories after reversal
	var activeCount int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM user_memories WHERE user_id = $1 AND superseded_by IS NULL AND (valid_until IS NULL OR valid_until > NOW())`,
		tgUser.UserID,
	).Scan(&activeCount)

	if activeCount != 0 {
		t.Errorf("expected 0 active memories after reversal, got %d", activeCount)
	}
}

func TestPostgreSQL_OutboxDeliveryWorkerStaleLeaseRecovery(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	tgRepo := telegram.NewRepository(pool)

	tgUser, _ := tgRepo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{
		TelegramUserID: 444333,
	})

	// Create delivery that got stuck in 'sending' state with expired lock (from dead worker)
	expiredLock := time.Now().Add(-5 * time.Minute)
	delivery := &telegram.TelegramDelivery{
		ChatID:   444333,
		UserID:   tgUser.UserID,
		Status:   telegram.DeliveryStatusSending,
		Attempts: 1,
		Payload:  []byte(`{"text":"Stuck message"}`),
	}
	if err := tgRepo.CreateDelivery(ctx, delivery); err != nil {
		t.Fatalf("failed creating delivery: %v", err)
	}

	// Manually set locked_at in the past
	_, _ = pool.Exec(ctx,
		`UPDATE telegram_deliveries SET status = 'sending', locked_at = $1, locked_by = 'dead-worker' WHERE id = $2`,
		expiredLock, delivery.ID,
	)

	// New worker claims pending deliveries
	claimed, err := tgRepo.ClaimPendingDelivery(ctx, delivery.ID, "new-worker-1", 3, 30*time.Second)
	if err != nil {
		t.Fatalf("failed claiming expired delivery: %v", err)
	}

	if claimed.ID != delivery.ID {
		t.Errorf("expected new worker to claim expired delivery %s, got %s", delivery.ID, claimed.ID)
	}
	if claimed.Attempts != 2 {
		t.Errorf("expected attempts incremented to 2, got %d", claimed.Attempts)
	}
}

func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}
