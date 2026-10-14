package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"jasmine/internal/clients/openrouter"
	telegramclient "jasmine/internal/clients/telegram"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
	"jasmine/internal/pkg/correlation"
	"jasmine/internal/pkg/observability"
)

type e2eReplyComposer struct{}

func (e2eReplyComposer) Compose(_ context.Context, _ int64, facts telegram.ReplyContext, _ []telegram.TelegramMessage) (string, error) {
	if facts.EventType == "" {
		return "", fmt.Errorf("missing reply event type")
	}
	return "reply:" + facts.EventType, nil
}

type mockTelegramAPIServer struct {
	mu           sync.Mutex
	sentMessages []map[string]any
}

func (m *mockTelegramAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.sentMessages = append(m.sentMessages, body)

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"ok": true,
		"result": map[string]any{
			"message_id": time.Now().UnixNano() % 1000000,
			"date":       time.Now().Unix(),
			"chat": map[string]any{
				"id":   body["chat_id"],
				"type": "private",
			},
			"text": body["text"],
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func TestPostgreSQL_ResolverFixtureMatching(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	exerciseRepo := exercise.NewRepository(pool)

	// Intentionally seed a realistic minimal exercise with optional nullable fields left NULL
	exBench := &exercise.Exercise{
		Name:        "Жим гантелей",
		NameEN:      "Dumbbell Bench Press",
		Type:        "strength",
		MuscleGroup: "Chest",
	}
	if err := exerciseRepo.Create(ctx, exBench); err != nil {
		t.Fatalf("failed creating bench exercise: %v", err)
	}

	// 1. Direct repository null-safety coverage: Search, GetByID, List
	searchRes, err := exerciseRepo.Search(ctx, "жим гантелей")
	if err != nil {
		t.Fatalf("repository search failed on nullable exercise: %v", err)
	}
	if len(searchRes) == 0 {
		t.Fatalf("expected at least 1 search result, got 0")
	}

	getRes, err := exerciseRepo.GetByID(ctx, exBench.ID)
	if err != nil {
		t.Fatalf("repository GetByID failed on nullable exercise: %v", err)
	}
	if getRes.Name != "Жим гантелей" {
		t.Errorf("expected name 'Жим гантелей', got %s", getRes.Name)
	}

	listRes, err := exerciseRepo.List(ctx)
	if err != nil {
		t.Fatalf("repository List failed on nullable exercise: %v", err)
	}
	if len(listRes) == 0 {
		t.Fatalf("expected at least 1 list result, got 0")
	}

	// 2. Resolver fixture matching: deterministic DB matching without LLM fallback
	resolver := exercise.NewResolver(exerciseRepo, nil, "test-model")
	res, err := resolver.Resolve(ctx, "жим гантелей")
	if err != nil {
		t.Fatalf("resolver failed: %v", err)
	}
	if res.Status != exercise.ResolutionExact {
		t.Fatalf("expected exact resolution status, got %s", res.Status)
	}
	if res.Exercise == nil || res.Exercise.ID != exBench.ID {
		t.Fatalf("expected exercise ID %s, got %+v", exBench.ID, res.Exercise)
	}
}

func TestPostgreSQL_FullApplicationE2EScenario(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Setup Mock Telegram Bot API Server
	tgMockServer := &mockTelegramAPIServer{}
	httpTgServer := httptest.NewServer(tgMockServer)
	defer httpTgServer.Close()

	tgClient := telegramclient.NewClientWithBaseURL("test-bot-token", httpTgServer.URL, httpTgServer.Client())

	// 2. Setup Mock OpenRouter Server with Prompt-Type / Operation-Based Routing
	openRouterServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[len(req.Messages)-1].Content
		}

		w.Header().Set("Content-Type", "application/json")
		var replyContent string

		// Deterministic Routing by System Prompt Signatures
		if strings.Contains(prompt, "specialized conversational workout correction extractor") || strings.Contains(prompt, "is_correction") {
			if strings.Contains(prompt, "24") || strings.Contains(prompt, "26") || strings.Contains(prompt, "не 24") {
				replyContent = `{"is_correction":true,"exercise_raw_name":"","changes":[{"field":"weight_kg","old_value":24.0,"new_value":26.0,"set_selector":"all"}],"confidence":0.95}`
			} else {
				replyContent = `{"is_correction":false,"confidence":0.0}`
			}
		} else if strings.Contains(prompt, "conservative conversational fitness memory extractor") {
			if strings.Contains(prompt, "гравитон") {
				replyContent = `[{"category":"preference","key":"avoid_graviton","value":"Исключить гравитон","scope":"conditional","confidence":0.95,"source_text":"пока не давай гравитон"}]`
			} else {
				replyContent = `[]`
			}
		} else if strings.Contains(prompt, "Modify the following proposed workout") {
			replyContent = `{"title":"Быстрая тренировка 30м","estimated_duration_min":30,"adaptation_note":"Сокращено до 30 минут","exercises":[{"exercise_name":"Seated Leg Curl","sets":[{"set_number":1,"reps":12,"weight_kg":27}]}]}`
		} else if strings.Contains(prompt, "fitness coach workout generator") || strings.Contains(prompt, "WorkoutGenerationRequest") {
			replyContent = `{"title":"Персональная тренировка","estimated_duration_min":45,"adaptation_note":"Исключил гравитон по запросу","exercises":[{"exercise_name":"Seated Leg Curl","sets":[{"set_number":1,"reps":12,"weight_kg":27},{"set_number":2,"reps":12,"weight_kg":27}]}]}`
		} else if strings.Contains(prompt, "specialized fitness data extraction parser") {
			userLog := prompt
			if at := strings.LastIndex(prompt, "User Log:\n"); at >= 0 {
				userLog = prompt[at+len("User Log:\n"):]
			}
			if strings.Contains(userLog, "жим гантелей") {
				replyContent = `{"workout_type":"strength","exercises":[{"raw_name":"жим гантелей","sets":[{"set_number":1,"reps":10,"weight_kg":24},{"set_number":2,"reps":10,"weight_kg":24},{"set_number":3,"reps":10,"weight_kg":24}]}]}`
			} else if strings.Contains(userLog, "хип траст") {
				replyContent = `{"workout_type":"strength","exercises":[{"raw_name":"хип траст","sets":[{"set_number":1,"reps":12,"weight_kg":10},{"set_number":2,"reps":12,"weight_kg":10},{"set_number":3,"reps":12,"weight_kg":10}]}]}`
			} else if strings.Contains(userLog, "ещё один подход") || strings.Contains(userLog, "12х27") {
				replyContent = `{"workout_type":"strength","exercises":[{"raw_name":"лег курл","sets":[{"set_number":3,"reps":12,"weight_kg":27}]}]}`
			} else if strings.Contains(userLog, "лег курл") {
				replyContent = `{"workout_type":"strength","exercises":[{"raw_name":"лег курл","sets":[{"set_number":1,"reps":12,"weight_kg":27},{"set_number":2,"reps":12,"weight_kg":27}]}]}`
			} else {
				replyContent = `{"workout_type":"strength","exercises":[]}`
			}
		} else {
			// Fail loudly on unexpected OpenRouter requests
			http.Error(w, fmt.Sprintf(`{"error":"unexpected OpenRouter request", "prompt_snippet": %q}`, prompt), http.StatusBadRequest)
			return
		}

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": replyContent,
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     50,
				"completion_tokens": 40,
				"total_tokens":      90,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer openRouterServer.Close()

	orClient := openrouter.NewClientWithBaseURL("test-or-key", "test-model", openRouterServer.URL, openRouterServer.Client())

	// 3. Assemble Repositories and Services
	tgRepo := telegram.NewRepository(pool)
	workoutRepo := workout.NewRepository(pool)
	exerciseRepo := exercise.NewRepository(pool)

	// Seed canonical exercises with Name and NameEN for deterministic matching
	exBench := &exercise.Exercise{
		Name:        "Жим гантелей",
		NameEN:      "Dumbbell Bench Press",
		Type:        "strength",
		MuscleGroup: "Chest",
	}
	if err := exerciseRepo.Create(ctx, exBench); err != nil {
		t.Fatalf("failed seeding bench exercise: %v", err)
	}

	exLegCurl := &exercise.Exercise{
		Name:        "Лег курл",
		NameEN:      "Seated Leg Curl",
		Type:        "strength",
		MuscleGroup: "Hamstrings",
	}
	if err := exerciseRepo.Create(ctx, exLegCurl); err != nil {
		t.Fatalf("failed seeding leg curl exercise: %v", err)
	}

	exHipThrust := &exercise.Exercise{
		Name:        "Хип траст",
		NameEN:      "Hip Thrust",
		Type:        "strength",
		MuscleGroup: "Glutes",
	}
	if err := exerciseRepo.Create(ctx, exHipThrust); err != nil {
		t.Fatalf("failed seeding hip thrust exercise: %v", err)
	}

	exGraviton := &exercise.Exercise{
		Name:        "Гравитон",
		NameEN:      "Assisted Pull-up (Graviton)",
		Type:        "strength",
		MuscleGroup: "Back",
	}
	if err := exerciseRepo.Create(ctx, exGraviton); err != nil {
		t.Fatalf("failed seeding graviton exercise: %v", err)
	}

	tgRenderer := telegram.NewDraftRenderer(tgClient, nil)
	tgDeliveryWorker := telegram.NewDeliveryWorker(tgRepo, tgClient, nil)
	tgDeliveryWorker.SetReplyComposer(e2eReplyComposer{})
	tgService := telegram.NewService(tgRepo, tgClient, tgRenderer, tgDeliveryWorker, nil)
	tgService.SetAllowedUserIDs([]int64{888123})

	exerciseResolver := exercise.NewResolver(exerciseRepo, orClient, "test-model")
	referentialResolver := workout.NewReferentialResolver(workoutRepo, exerciseResolver, tgRepo)
	correctionExtractor := workout.NewCorrectionExtractor(orClient, "test-model")
	correctionSvc := workout.NewCorrectionService(pool, workoutRepo, referentialResolver, correctionExtractor, tgRepo, tgRenderer)

	workoutExtractor := workout.NewExtractor(orClient, "test-model")
	workoutIngestion := workout.NewIngestionPipeline(pool, workoutRepo, exerciseResolver, workoutExtractor, tgRepo, tgRenderer)
	workoutIngestion.SetCorrectionService(correctionSvc)

	memoryExtractor := assistant.NewMemoryExtractor(orClient, "test-model")
	memoryExtractor.SetExerciseResolver(exerciseResolver)
	contextBuilder := assistant.NewContextBuilder(assistant.DefaultBudgets())
	candidateSelector := assistant.NewCandidateSelector(exerciseRepo)
	generator := assistant.NewWorkoutGenerator(orClient, "test-model")
	sessionService := assistant.NewSessionService(tgRepo, tgRenderer)

	orchestrator := assistant.NewOrchestrator(
		pool, orClient, "test-model", "test-model",
		workoutIngestion, correctionSvc, memoryExtractor, contextBuilder,
		candidateSelector, generator, sessionService,
		tgRepo, tgRenderer,
	)
	tgService.SetIngestionPipeline(orchestrator)

	llmRepo := observability.NewRepository(pool)
	llmObserver := observability.NewService(llmRepo)
	orClient.SetObserver(llmObserver)

	chatID := int64(888123)
	tgUserID := int64(888123)

	sendUpdate := func(updateID int64, msgID int64, text string) error {
		raw := []byte(fmt.Sprintf(`{
			"update_id": %d,
			"message": {
				"message_id": %d,
				"date": %d,
				"chat": {"id": %d, "type": "private"},
				"from": {"id": %d, "first_name": "Alex", "username": "alex_fit"},
				"text": %q
			}
		}`, updateID, msgID, time.Now().Unix(), chatID, tgUserID, text))
		return tgService.ProcessWebhookUpdate(ctx, raw)
	}

	assertUpdateCompleted := func(updateID int64, stepName string) {
		var status string
		var lastErr *string
		err := pool.QueryRow(ctx, `SELECT status, last_error FROM telegram_updates WHERE update_id = $1`, updateID).Scan(&status, &lastErr)
		if err != nil {
			t.Fatalf("[%s] failed reading update status for update_id=%d: %v", stepName, updateID, err)
		}
		if status != "completed" {
			errStr := "none"
			if lastErr != nil {
				errStr = *lastErr
			}
			t.Fatalf("[%s] update_id=%d not completed (status=%s, last_error=%s)", stepName, updateID, status, errStr)
		}
	}

	// -------------------------------------------------------------------------
	// Step 1: User sends first message -> Auto-provisioning & Ingestion of Workout #1
	// -------------------------------------------------------------------------
	if err := sendUpdate(1001, 501, "жим гантелей 3х10 24кг"); err != nil {
		t.Fatalf("step 1 failed: %v", err)
	}
	assertUpdateCompleted(1001, "Step 1: Standalone Workout Ingestion")

	tgUser, err := tgRepo.GetTelegramUser(ctx, tgUserID)
	if err != nil || tgUser == nil {
		t.Fatalf("expected telegram user to be provisioned: %v", err)
	}

	// Assert no pending clarification remains
	convState, err := tgRepo.GetConversationState(ctx, tgUser.UserID)
	if err == nil && convState != nil && convState.PendingClarification != nil {
		t.Fatalf("unexpected pending clarification in state: %+v", convState.PendingClarification)
	}

	// Assert exactly 1 workout created with 1 workout_exercise (3 sets of 24kg)
	var workout1ID string
	var workout1UpdateID *int64
	err = pool.QueryRow(ctx, `SELECT id, telegram_update_id FROM workouts WHERE user_id = $1`, tgUser.UserID).Scan(&workout1ID, &workout1UpdateID)
	if err != nil {
		t.Fatalf("failed querying workout #1: %v (user_id=%s)", err, tgUser.UserID)
	}
	if workout1UpdateID == nil || *workout1UpdateID != 1001 {
		t.Errorf("expected workout telegram_update_id=1001, got %v", workout1UpdateID)
	}

	var weExerciseID string
	var weSetsJSON []byte
	err = pool.QueryRow(ctx, `SELECT exercise_id, sets_data FROM workout_exercises WHERE workout_id = $1`, workout1ID).Scan(&weExerciseID, &weSetsJSON)
	if err != nil {
		t.Fatalf("failed querying workout_exercise #1: %v", err)
	}
	if weExerciseID != exBench.ID {
		t.Errorf("expected exercise ID %s, got %s", exBench.ID, weExerciseID)
	}
	var sets1 []workout.Set
	_ = json.Unmarshal(weSetsJSON, &sets1)
	if len(sets1) != 3 {
		t.Fatalf("expected 3 sets in workout #1, got %d", len(sets1))
	}
	for i, s := range sets1 {
		if s.Reps == nil || *s.Reps != 10 || s.WeightKg == nil || *s.WeightKg != 24.0 {
			t.Errorf("set %d mismatch: reps=%v, weight=%v", i+1, s.Reps, s.WeightKg)
		}
	}

	// -------------------------------------------------------------------------
	// Step 2: User corrects previous weight: "не 24, а 26"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1002, 502, "не 24, а 26"); err != nil {
		t.Fatalf("step 2 failed: %v", err)
	}
	assertUpdateCompleted(1002, "Step 2: Factual Correction")

	var corrCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM workout_corrections WHERE user_id = $1`, tgUser.UserID).Scan(&corrCount)
	if corrCount != 1 {
		t.Errorf("expected exactly 1 correction audit record, got %d (user_id=%s)", corrCount, tgUser.UserID)
	}

	// -------------------------------------------------------------------------
	// Step 3: User states preference: "пока не давай гравитон"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1003, 503, "пока не давай гравитон"); err != nil {
		t.Fatalf("step 3 failed: %v", err)
	}
	assertUpdateCompleted(1003, "Step 3: Conditional Memory")

	memories, err := tgRepo.ListMemories(ctx, tgUser.UserID, nil, nil)
	if err != nil {
		t.Fatalf("failed listing active memories: %v", err)
	}

	var gravitonMemories []telegram.UserMemory
	for _, m := range memories {
		if strings.HasPrefix(m.Key, "avoid_exercise:") && (strings.Contains(strings.ToLower(string(m.Value)), "гравитон") || m.Key == fmt.Sprintf("avoid_exercise:%s", exGraviton.ID)) {
			gravitonMemories = append(gravitonMemories, m)
		}
	}
	if len(gravitonMemories) != 1 {
		t.Fatalf("expected exactly 1 active memory for graviton avoidance, got %d (all active=%d)", len(gravitonMemories), len(memories))
	}
	if gravitonMemories[0].Scope != "conditional" {
		t.Errorf("expected conditional scope, got %s", gravitonMemories[0].Scope)
	}

	// -------------------------------------------------------------------------
	// Step 4: User requests workout: "что сегодня делать?"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1004, 504, "что сегодня делать?"); err != nil {
		t.Fatalf("step 4 failed: %v", err)
	}
	assertUpdateCompleted(1004, "Step 4: Workout Generation Request")

	activeSession, err := tgRepo.GetActiveWorkoutSession(ctx, tgUser.UserID)
	if err != nil || activeSession == nil {
		t.Fatalf("expected active proposed workout session: %v", err)
	}
	if activeSession.Status != "proposed" {
		t.Errorf("expected proposed status, got %s", activeSession.Status)
	}

	// -------------------------------------------------------------------------
	// Step 5: User requests modification: "сделай 30 минут"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1005, 505, "сделай 30 минут"); err != nil {
		t.Fatalf("step 5 failed: %v", err)
	}
	assertUpdateCompleted(1005, "Step 5: Proposal Modification")

	var revCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM workout_session_revisions WHERE session_id = $1`, activeSession.ID).Scan(&revCount)
	if revCount != 1 {
		t.Errorf("expected 1 session revision record, got %d", revCount)
	}

	// -------------------------------------------------------------------------
	// Step 6: User starts workout: "начинаем"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1006, 506, "начинаем"); err != nil {
		t.Fatalf("step 6 failed: %v", err)
	}
	assertUpdateCompleted(1006, "Step 6: Start Workout")

	activeSession, _ = tgRepo.GetActiveWorkoutSession(ctx, tgUser.UserID)
	if activeSession.Status != "in_progress" {
		t.Errorf("expected session status in_progress, got %s", activeSession.Status)
	}

	// -------------------------------------------------------------------------
	// Step 7: Incremental performed log: "лег курл 2х12 27"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1007, 507, "лег курл 2х12 27"); err != nil {
		t.Fatalf("step 7 failed: %v", err)
	}
	assertUpdateCompleted(1007, "Step 7: Performed Leg Curl")

	// -------------------------------------------------------------------------
	// Step 8: Incremental performed log: "хип траст 3х12 +10"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1008, 508, "хип траст 3х12 10"); err != nil {
		t.Fatalf("step 8 failed: %v", err)
	}
	assertUpdateCompleted(1008, "Step 8: Performed Hip Thrust")

	// -------------------------------------------------------------------------
	// Step 9: Incremental performed log: "ещё один подход лег курла 12х27"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1009, 509, "ещё один подход лег курла 12х27"); err != nil {
		t.Fatalf("step 9 failed: %v", err)
	}
	assertUpdateCompleted(1009, "Step 9: Performed Additional Set")

	// -------------------------------------------------------------------------
	// Step 10: User finishes workout: "закончил тренировку"
	// -------------------------------------------------------------------------
	if err := sendUpdate(1010, 510, "закончил тренировку"); err != nil {
		t.Fatalf("step 10 failed: %v", err)
	}
	assertUpdateCompleted(1010, "Step 10: Complete Workout Session")

	// -------------------------------------------------------------------------
	// Step 11: Deliveries processed by DeliveryWorker
	// -------------------------------------------------------------------------
	workerCtx, workerCancel := context.WithTimeout(ctx, 3*time.Second)
	defer workerCancel()
	_, _ = tgDeliveryWorker.ProcessPendingBatch(workerCtx, 20)

	tgMockServer.mu.Lock()
	sentCount := len(tgMockServer.sentMessages)
	tgMockServer.mu.Unlock()

	if sentCount < 6 {
		t.Errorf("expected at least 6 Telegram deliveries dispatched, got %d", sentCount)
	}

	// -------------------------------------------------------------------------
	// Step 12: Verify persisted correlation attribution without a web API.
	corr1 := correlation.GenerateUpdateCorrelationID(1001)
	var actualCorrelation string
	if err := pool.QueryRow(ctx, `SELECT correlation_id FROM telegram_updates WHERE update_id=1001`).Scan(&actualCorrelation); err != nil {
		t.Fatal(err)
	}
	if actualCorrelation != corr1 {
		t.Fatalf("correlation mismatch: %s", actualCorrelation)
	}

	// -------------------------------------------------------------------------
	// Step 13: Final DB Invariant Assertions
	// -------------------------------------------------------------------------
	// Assert completed session and bound applied_workout_id
	var completedStatus string
	var appliedWorkoutID *string
	err = pool.QueryRow(ctx,
		`SELECT status, applied_workout_id::text FROM workout_sessions WHERE id = $1`,
		activeSession.ID,
	).Scan(&completedStatus, &appliedWorkoutID)
	if err != nil {
		t.Fatalf("failed reading final session state: %v", err)
	}
	if completedStatus != "completed" {
		t.Errorf("expected final session status completed, got %s", completedStatus)
	}
	if appliedWorkoutID == nil || *appliedWorkoutID == "" {
		t.Errorf("expected applied_workout_id to be populated on completed session")
	}

	// Assert applied workout contains Leg Curl (3 sets) and Hip Thrust (3 sets)
	if appliedWorkoutID != nil {
		// Query PostgreSQL directly so this invariant cannot be masked by an
		// incomplete admin-repository projection.
		rows, err := pool.Query(ctx,
			`SELECT exercise_id::text, jsonb_array_length(sets_data)
			 FROM workout_exercises
			 WHERE workout_id = $1`,
			*appliedWorkoutID,
		)
		if err != nil {
			t.Fatalf("failed reading persisted workout exercises: %v", err)
		}
		setCounts := make(map[string]int)
		rowCount := 0
		for rows.Next() {
			var exerciseID string
			var setCount int
			if err := rows.Scan(&exerciseID, &setCount); err != nil {
				rows.Close()
				t.Fatalf("failed scanning persisted workout exercise: %v", err)
			}
			rowCount++
			setCounts[exerciseID] = setCount
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("failed iterating persisted workout exercises: %v", err)
		}
		rows.Close()
		if rowCount != 2 {
			t.Fatalf("expected 2 persisted workout_exercises rows, got %d", rowCount)
		}
		if len(setCounts) != 2 {
			t.Fatalf("expected 2 distinct persisted exercise IDs, got %d", len(setCounts))
		}
		if got := setCounts[exLegCurl.ID]; got != 3 {
			t.Errorf("expected 3 persisted Leg Curl sets, got %d", got)
		}
		if got := setCounts[exHipThrust.ID]; got != 3 {
			t.Errorf("expected 3 persisted Hip Thrust sets, got %d", got)
		}

		var increments int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workout_ingestion_increments WHERE workout_id=$1`, *appliedWorkoutID).Scan(&increments); err != nil {
			t.Fatal(err)
		}
		if len(setCounts) != 2 {
			t.Errorf("expected 2 distinct exercises, got %d", len(setCounts))
		}
		if increments < 3 {
			t.Errorf("expected at least 3 increment audit rows, got %d", increments)
		}

	}
}
