package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

type scriptedCoachPlanner struct{}

func (scriptedCoachPlanner) Plan(_ context.Context, turn assistant.CoachTurn, _ *assistant.CoachContext) (*assistant.CoachPlan, error) {
	names := map[string]string{"че сегодня делаем": "propose_workout", "го": "start_workout", "первый 12 на 27 легко": "log_set", "15": "log_set", "этот тренажер занят замени": "replace_exercise", "а почему тут 12 повторений": "fitness_question", "времени осталось 15 минут": "replan_active_workout", "че дальше": "get_current_workout_state", "все": "finish_workout", "что я делал": "get_exercise_history"}
	n := names[turn.Text]
	if n == "" {
		return &assistant.CoachPlan{}, nil
	}
	call := assistant.CoachToolCall{Name: n}
	if turn.Text == "первый 12 на 27 легко" {
		reps := 12
		set := 1
		kg := 27.0
		call.Reps = &reps
		call.SetNumber = &set
		call.WeightKg = &kg
		call.Difficulty = "easy"
	}
	if turn.Text == "15" {
		reps := 15
		call.Reps = &reps
	}
	return &assistant.CoachPlan{Calls: []assistant.CoachToolCall{call}}, nil
}

type scriptedJasmineCoachPlanner struct{}

func (scriptedJasmineCoachPlanner) Next(_ context.Context, event assistant.AssistantEvent, state *assistant.JasmineContext) (assistant.JasmineStep, error) {
	if len(state.Observations) > 0 {
		last := state.Observations[len(state.Observations)-1]
		if !last.Success {
			return assistant.JasmineStep{}, fmt.Errorf("coach tool failed: %s", last.Error)
		}
		return assistant.JasmineStep{Final: &assistant.JasmineFinal{Text: last.Facts}}, nil
	}
	names := map[string]string{
		"че сегодня делаем": "propose_workout", "го": "start_workout", "первый 12 на 27 легко": "log_set", "15": "log_set", "не 27 а 32": "correct_performance", "этот тренажер занят замени": "replace_exercise", "а почему тут 12 повторений": "fitness_question", "времени осталось 15 минут": "replan_active_workout", "че дальше": "get_current_workout_state", "все": "finish_workout", "что я делал": "get_exercise_history",
	}
	name := names[event.Text]
	if name == "" {
		return assistant.JasmineStep{}, fmt.Errorf("unexpected test turn %q", event.Text)
	}
	input := json.RawMessage(`{}`)
	if event.Text == "первый 12 на 27 легко" {
		input = json.RawMessage(`{"reps":12,"set_number":1,"weight_kg":27,"difficulty":"easy"}`)
	}
	if event.Text == "15" {
		input = json.RawMessage(`{"reps":15}`)
	}
	return assistant.JasmineStep{Call: &assistant.JasmineToolCall{Name: "coach." + name, Input: input}}, nil
}

func TestPostgreSQL_CoachFullConversation(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('coach') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exRepo := exercise.NewRepository(pool)
	leg := &exercise.Exercise{Name: "Seated Leg Curl", Type: "strength", MuscleGroup: "Hamstrings"}
	if err := exRepo.Create(ctx, leg); err != nil {
		t.Fatal(err)
	}
	step := &exercise.Exercise{Name: "Z Leg Step-up", Type: "strength", MuscleGroup: "Legs"}
	if err := exRepo.Create(ctx, step); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, eq string }{{leg.ID, "machine"}, {step.ID, "dumbbell"}} {
		if _, err := pool.Exec(ctx, `UPDATE exercises SET category='lower',tracking_type='weight_reps',equipment=ARRAY[$2]::text[] WHERE id=$1`, row.id, row.eq); err != nil {
			t.Fatal(err)
		}
	}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := fmt.Sprintf(`{"title":"Сегодня ноги","estimated_duration_min":40,"exercises":[{"exercise_id":%q,"exercise_name":"Seated Leg Curl","rest_sec":75,"sets":[{"set_number":1,"reps":12,"weight_kg":27},{"set_number":2,"reps":12,"weight_kg":27},{"set_number":3,"reps":12,"weight_kg":27}]}]}`, leg.ID)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": payload}}}})
	}))
	defer modelServer.Close()
	ai := openrouter.NewClientWithBaseURL("mock", "mock", modelServer.URL, modelServer.Client())
	tgRepo := telegram.NewRepository(pool)
	orch := assistant.NewOrchestrator(pool, ai, "mock", "mock", nil, nil, nil, assistant.NewContextBuilder(assistant.DefaultBudgets()), assistant.NewCandidateSelector(exRepo), assistant.NewWorkoutGenerator(ai, "mock"), assistant.NewSessionService(tgRepo, nil), tgRepo, nil)
	jasmine, err := assistant.NewJasmineRuntimeWithLegacy(orch)
	if err != nil {
		t.Fatal(err)
	}
	jasmine.SetPlanner(scriptedJasmineCoachPlanner{})
	var invoked []string
	jasmine.SetTraceSink(func(_ context.Context, trace assistant.JasmineTrace) {
		if trace.Phase == "tool" {
			invoked = append(invoked, trace.Tool)
		}
	})
	for _, spec := range jasmine.ToolSpecs() {
		if spec.Name == "legacy.handle_message" {
			t.Fatal("generic legacy Coach fallback remains registered")
		}
	}
	chatID := int64(5588)
	var sessionID, workoutID string
	steps := []struct {
		text, status    string
		sets, revisions int
	}{
		{"че сегодня делаем", "proposed", 0, 0},
		{"го", "in_progress", 0, 0},
		{"первый 12 на 27 легко", "in_progress", 1, 0},
		{"15", "in_progress", 2, 0},
		{"не 27 а 32", "in_progress", 2, 0},
		{"этот тренажер занят замени", "in_progress", 2, 1},
		{"а почему тут 12 повторений", "in_progress", 2, 1},
		{"времени осталось 15 минут", "in_progress", 2, 2},
		{"че дальше", "in_progress", 2, 2},
		{"все", "completed", 2, 2},
	}
	for i, stepData := range steps {
		updateID := int64(9000 + i)
		messageID := int64(100 + i)
		raw := []byte(`{}`)
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,status,raw_payload) VALUES($1,$2,$2,'processing',$3)`, updateID, chatID, raw); err != nil {
			t.Fatalf("%d seed update: %v", i, err)
		}
		res, err := jasmine.IngestMessage(ctx, userID, chatID, updateID, messageID, nil, stepData.text)
		if err != nil {
			t.Fatalf("%d %q: %v", i, stepData.text, err)
		}
		readOnly := i == 6 || i == 8
		if res == nil || (readOnly && res.ReplyContext == nil) || (!readOnly && !res.Success) {
			t.Fatalf("%d %q: response %+v", i, stepData.text, res)
		}
		if i == 0 {
			s, err := tgRepo.GetActiveWorkoutSession(ctx, userID)
			if err != nil {
				t.Fatal(err)
			}
			sessionID = s.ID
		}
		var status string
		var applied *string
		var proposal []byte
		if err := pool.QueryRow(ctx, `SELECT status,applied_workout_id,proposal_data FROM workout_sessions WHERE id=$1`, sessionID).Scan(&status, &applied, &proposal); err != nil {
			t.Fatal(err)
		}
		if status != stepData.status {
			t.Fatalf("%d status %s != %s", i, status, stepData.status)
		}
		if applied != nil {
			workoutID = *applied
		}
		var sets int
		if workoutID != "" {
			if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(jsonb_array_length(sets_data)),0)::int FROM workout_exercises WHERE workout_id=$1`, workoutID).Scan(&sets); err != nil {
				t.Fatal(err)
			}
		}
		if sets != stepData.sets {
			t.Fatalf("%d performed sets %d != %d", i, sets, stepData.sets)
		}
		var revisions int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workout_session_revisions WHERE session_id=$1`, sessionID).Scan(&revisions); err != nil {
			t.Fatal(err)
		}
		if revisions != stepData.revisions {
			t.Fatalf("%d revisions %d != %d", i, revisions, stepData.revisions)
		}
		if i == 3 {
			var raw []byte
			if err := pool.QueryRow(ctx, `SELECT sets_data FROM workout_exercises WHERE workout_id=$1 AND exercise_id=$2`, workoutID, leg.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var performed []workout.Set
			if err := json.Unmarshal(raw, &performed); err != nil {
				t.Fatal(err)
			}
			if len(performed) != 2 || *performed[1].Reps != 15 || *performed[1].WeightKg != 27 {
				t.Fatalf("inherited load or reps wrong: %+v", performed)
			}
		}
		if i == 4 {
			var raw []byte
			if err := pool.QueryRow(ctx, `SELECT sets_data FROM workout_exercises WHERE workout_id=$1 AND exercise_id=$2`, workoutID, leg.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var performed []workout.Set
			if err := json.Unmarshal(raw, &performed); err != nil {
				t.Fatal(err)
			}
			if len(performed) != 2 || *performed[1].WeightKg != 32 {
				t.Fatalf("correction not applied: %+v", performed)
			}
		}
		if i == 5 || i == 7 {
			var planned assistant.WorkoutProposalDraft
			if err := json.Unmarshal(proposal, &planned); err != nil {
				t.Fatal(err)
			}
			if len(planned.Exercises) < 2 || planned.Exercises[0].ExerciseID != leg.ID || len(planned.Exercises[0].Sets) != 2 || planned.Exercises[1].ExerciseID != step.ID {
				t.Fatalf("completed work or replacement lost: %+v", planned)
			}
			if i == 7 && planned.EstimatedDurationMin != 15 {
				t.Fatalf("replanned duration %d", planned.EstimatedDurationMin)
			}
		}
		if i == 6 && len(res.ReplyContext.ActionsPerformed) != 0 {
			t.Fatalf("question claimed an action: %+v", res.ReplyContext)
		}
		if i == 9 {
			if res.ReplyContext == nil || res.ReplyContext.EventType != "coach_result" {
				t.Fatalf("missing structured finish result: %+v", res)
			}
			performed, ok := res.ReplyContext.CurrentState["performed_workout"].(*workout.Workout)
			if !ok || len(performed.Exercises) != 1 || len(performed.Exercises[0].Sets) != 2 {
				t.Fatalf("finish result lacks two performed sets: %+v", res.ReplyContext.CurrentState)
			}
		}
		if i == 6 || i == 8 { // Telegram Service completes read-only turns after runtime returns.
			if err := tgRepo.CompleteUpdate(ctx, updateID, []byte(`{"status":"read"}`)); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 || i == 3 || i == 4 {
			var increments int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM workout_ingestion_increments WHERE telegram_update_id=$1`, updateID).Scan(&increments); err != nil {
				t.Fatal(err)
			}
			if increments != 1 {
				t.Fatalf("%d domain mutations for one Coach action: %d", i, increments)
			}
		}
		if i != 6 && i != 8 {
			var deliveries int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM telegram_deliveries WHERE update_id=$1`, updateID).Scan(&deliveries); err != nil {
				t.Fatal(err)
			}
			if deliveries != 1 {
				t.Fatalf("%d deliveries for one Coach action: %d", i, deliveries)
			}
		}
		var completed string
		if err := pool.QueryRow(ctx, `SELECT status FROM telegram_updates WHERE update_id=$1`, updateID).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed != "completed" {
			t.Fatalf("%d update status %s", i, completed)
		}
	}
	var volume float64
	if err := pool.QueryRow(ctx, `SELECT total_volume FROM workouts WHERE id=$1`, workoutID).Scan(&volume); err != nil {
		t.Fatal(err)
	}
	if volume != 804 {
		t.Fatalf("volume %.1f", volume)
	}
	updateID := int64(9100)
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,status,raw_payload) VALUES($1,$2,$2,'processing','{}')`, updateID, chatID); err != nil {
		t.Fatal(err)
	}
	history, err := jasmine.IngestMessage(ctx, userID, chatID, updateID, 200, nil, "что я делал")
	if err != nil {
		t.Fatal(err)
	}
	if history == nil || history.ReplyContext == nil {
		t.Fatalf("history response %+v", history)
	}
	data, err := json.Marshal(history.ReplyContext.CurrentState)
	if err != nil || !strings.Contains(string(data), "804") {
		t.Fatalf("history observation lacks verified volume: %s %v", data, err)
	}
	for _, name := range invoked {
		if !strings.HasPrefix(name, "coach.") {
			t.Fatalf("legacy tool executed in Coach scenario: %s", name)
		}
	}
	_ = time.Now
}

func TestPostgreSQL_CoachTransientConditionShapesProposal(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('condition') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exRepo := exercise.NewRepository(pool)
	for _, fixture := range []struct{ name, category string }{{"Barbell Squat", "lower"}, {"Upper Push", "upper"}} {
		e := &exercise.Exercise{Name: fixture.name, Type: "strength", MuscleGroup: fixture.category}
		if err := exRepo.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE exercises SET category=$2,tracking_type='weight_reps',equipment=ARRAY['dumbbell']::text[] WHERE id=$1`, e.ID, fixture.category); err != nil {
			t.Fatal(err)
		}
	}
	repo := telegram.NewRepository(pool)
	orch := assistant.NewOrchestrator(pool, nil, "", "", nil, nil, nil, assistant.NewContextBuilder(assistant.DefaultBudgets()), assistant.NewCandidateSelector(exRepo), assistant.NewWorkoutGenerator(nil, ""), assistant.NewSessionService(repo, nil), repo, nil)
	updateID := int64(9200)
	chatID := int64(998)
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,status,raw_payload) VALUES($1,$2,$2,'processing','{}')`, updateID, chatID); err != nil {
		t.Fatal(err)
	}
	res, err := orch.ProcessMessage(ctx, userID, chatID, updateID, 301, nil, "ноги сегодня убитые давай минут на 40 без приседа")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.Success {
		t.Fatalf("response %+v", res)
	}
	session, err := repo.GetActiveWorkoutSession(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var proposal assistant.WorkoutProposalDraft
	if err := json.Unmarshal(session.ProposalData, &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.EstimatedDurationMin != 40 || len(proposal.Exercises) == 0 {
		t.Fatalf("duration or exercise: %+v", proposal)
	}
	for _, ex := range proposal.Exercises {
		if strings.Contains(strings.ToLower(ex.ExerciseName), "squat") {
			t.Fatalf("squat included despite condition: %+v", proposal)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_memories WHERE user_id=$1 AND category='transient_state' AND valid_until>NOW()`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("transient memory count %d", count)
	}
}

type correctionCoachPlanner struct{}

func (correctionCoachPlanner) Plan(_ context.Context, turn assistant.CoachTurn, _ *assistant.CoachContext) (*assistant.CoachPlan, error) {
	if turn.Text == "первый 12 на 27 легко" {
		r := 12
		kg := 27.0
		return &assistant.CoachPlan{Calls: []assistant.CoachToolCall{{Name: "log_set", Reps: &r, WeightKg: &kg, Difficulty: "easy"}}}, nil
	}
	if turn.Text == "не 12 а 10" || turn.Text == "не 27 а 30" {
		return &assistant.CoachPlan{Calls: []assistant.CoachToolCall{{Name: "correct_performance"}}}, nil
	}
	return &assistant.CoachPlan{}, nil
}
func TestPostgreSQL_CoachContextualCorrectionAudit(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username) VALUES('correct') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exRepo := exercise.NewRepository(pool)
	leg := &exercise.Exercise{Name: "Seated Leg Curl", Type: "strength", MuscleGroup: "Hamstrings"}
	if err := exRepo.Create(ctx, leg); err != nil {
		t.Fatal(err)
	}
	reps := 12
	kg := 27.0
	proposal := assistant.WorkoutProposalDraft{Title: "Тест", EstimatedDurationMin: 30, Exercises: []assistant.ProposedExerciseDraft{{ExerciseID: leg.ID, ExerciseName: leg.Name, Sets: []assistant.ProposedSetDraft{{SetNumber: 1, Reps: &reps, WeightKg: &kg}, {SetNumber: 2, Reps: &reps, WeightKg: &kg}}}}}
	b, _ := json.Marshal(proposal)
	repo := telegram.NewRepository(pool)
	session := &telegram.WorkoutSession{UserID: userID, Status: "in_progress", ProposalData: b, ExpiresAt: time.Now().Add(time.Hour)}
	if err := repo.CreateWorkoutSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	orch := assistant.NewOrchestrator(pool, nil, "", "", nil, nil, nil, assistant.NewContextBuilder(assistant.DefaultBudgets()), assistant.NewCandidateSelector(exRepo), assistant.NewWorkoutGenerator(nil, ""), assistant.NewSessionService(repo, nil), repo, nil)
	orch.SetCoachPlanner(correctionCoachPlanner{})
	for i, text := range []string{"первый 12 на 27 легко", "не 12 а 10", "не 27 а 30"} {
		updateID := int64(9300 + i)
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,status,raw_payload) VALUES($1,777,777,'processing','{}')`, updateID); err != nil {
			t.Fatal(err)
		}
		res, err := orch.ProcessMessage(ctx, userID, 777, updateID, int64(400+i), nil, text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if res == nil || !res.Success {
			t.Fatalf("%s: %+v", text, res)
		}
	}
	session, err := repo.GetWorkoutSession(ctx, session.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.AppliedWorkoutID == nil {
		t.Fatal("no applied workout")
	}
	var raw []byte
	var volume float64
	if err := pool.QueryRow(ctx, `SELECT sets_data FROM workout_exercises WHERE workout_id=$1`, *session.AppliedWorkoutID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT total_volume FROM workouts WHERE id=$1`, *session.AppliedWorkoutID).Scan(&volume); err != nil {
		t.Fatal(err)
	}
	var sets []workout.Set
	if err := json.Unmarshal(raw, &sets); err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || *sets[0].Reps != 10 || *sets[0].WeightKg != 30 || volume != 300 {
		t.Fatalf("corrected sets %+v, volume %v", sets, volume)
	}
	var before, after float64
	if err := pool.QueryRow(ctx, `SELECT before_volume,after_volume FROM workout_corrections WHERE workout_id=$1 ORDER BY created_at LIMIT 1`, *session.AppliedWorkoutID).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if before != 324 || after != 270 {
		t.Fatalf("audit %.0f -> %.0f", before, after)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workout_corrections WHERE workout_id=$1`, *session.AppliedWorkoutID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("correction audits %d", auditCount)
	}
}
