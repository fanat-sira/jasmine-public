package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jasmine/internal/clients/openrouter"
	telegramclient "jasmine/internal/clients/telegram"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/diary"
	"jasmine/internal/module/exercise"
	"jasmine/internal/module/telegram"
	"jasmine/internal/module/workout"
)

func TestPostgreSQL_TelegramImageFlows(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var imageKind atomic.Value
	imageKind.Store("food")
	var aiCalls atomic.Int64
	var inAlbum atomic.Bool
	var albumClassifications atomic.Int64
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_id": "photo", "file_unique_id": "stable-photo", "file_path": "photo.jpg", "file_size": 128}})
		case strings.Contains(r.URL.Path, "/file/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 999, "date": time.Now().Unix()}})
		}
	}))
	defer tgServer.Close()
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiCalls.Add(1)
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("AI request: %v", err)
		}
		prompt := ""
		for _, message := range req.Messages {
			var plain string
			if json.Unmarshal(message.Content, &plain) == nil {
				prompt += plain
				continue
			}
			var parts []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(message.Content, &parts) == nil {
				for _, part := range parts {
					prompt += part.Text
				}
			}
		}
		kind := imageKind.Load().(string)
		reply := ""
		switch {
		case strings.Contains(prompt, "You estimate a corrected food portion"):
			reply = `{"weight_g":500,"calories":650,"protein_g":30,"fat_g":24,"carbs_g":78}`
		case strings.Contains(prompt, "Classify a Telegram food album"):
			reply = `{"assignments":[{"image_index":0,"meal_type":"breakfast","name_override":""},{"image_index":1,"meal_type":"lunch","name_override":""},{"image_index":2,"meal_type":"snack","name_override":""},{"image_index":3,"meal_type":"snack","name_override":""},{"image_index":4,"meal_type":"dinner","name_override":""}]}`
		case strings.Contains(prompt, "визуальный маршрутизатор"):
			if inAlbum.Load() && albumClassifications.Add(1) == 1 {
				kind = "nutrition_label"
			}
			reply = `{"type":"` + kind + `","confidence":0.98,"evidence":["visible"],"requires_clarification":false}`
		case strings.Contains(prompt, "эксперт-диетолог"):
			reply = `{"items":[{"name_ru":"Яйцо","name_en":"Egg","weight_g":100,"calories":150,"protein_g":12,"fat_g":10,"carbs_g":1,"confidence":"high"}],"total":{"calories":9999,"protein_g":999,"fat_g":999,"carbs_g":999},"notes":"оценка"}`
		case strings.Contains(prompt, "пищевую этикетку"):
			reply = `{"product_name":"Тестовый батончик","brand":"Тест","barcode":"4601234567890","serving_size_g":50,"values_basis":"per_serving","calories":105,"protein_g":9,"fat_g":4,"carbs_g":8,"ingredients":[],"needs_web_lookup":false,"confidence":0.96}`
		case strings.Contains(prompt, "фото весов"):
			reply = `{"weight_kg":72.4,"body_fat_percent":null,"muscle_mass_kg":null,"water_percent":null,"bmi":null,"confidence":0.99}`
		case strings.Contains(prompt, "экран спортивного тренажёра"):
			reply = `{"duration_text":"00:45:00","distance_value":3.6,"distance_unit":"mi","calories":382,"confidence":0.96}`
		default:
			t.Errorf("unexpected AI prompt: %s", prompt[:min(80, len(prompt))])
			reply = `{}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}})
	}))
	defer aiServer.Close()
	tgClient := telegramclient.NewClientWithBaseURL("test-token", tgServer.URL, tgServer.Client())
	aiClient := openrouter.NewClientWithBaseURL("test-key", "vision-model", aiServer.URL, aiServer.Client())
	tgRepo := telegram.NewRepository(pool)
	exRepo := exercise.NewRepository(pool)
	ex := &exercise.Exercise{Name: "Эллипс", NameEN: "Elliptical", Type: "cardio", MuscleGroup: "Full body"}
	if err := exRepo.Create(ctx, ex); err != nil {
		t.Fatal(err)
	}
	resolver := exercise.NewResolver(exRepo, nil, "vision-model")
	ingestion := workout.NewIngestionPipeline(pool, workout.NewRepository(pool), resolver, nil, tgRepo, nil)
	orch := assistant.NewOrchestrator(pool, aiClient, "vision-model", "vision-model", ingestion, nil, nil, nil, nil, nil, nil, tgRepo, nil)
	orch.SetImageAnalyzer(assistant.NewImageAnalyzer(aiClient, "vision-model", "vision-model", "vision-model"))
	svc := telegram.NewService(tgRepo, tgClient, nil, nil, nil)
	svc.SetAllowedUserIDs([]int64{445566})
	svc.SetIngestionPipeline(orch)
	svc.SetImageIngestionPipeline(orch)
	svc.SetAlbumIngestionPipeline(orch)
	sendPhoto := func(updateID, messageID int64, caption string) {
		payload := map[string]any{"update_id": updateID, "message": map[string]any{"message_id": messageID, "date": time.Now().Unix(), "chat": map[string]any{"id": 445566, "type": "private"}, "from": map[string]any{"id": 445566, "first_name": "Image Test"}, "caption": caption, "photo": []any{map[string]any{"file_id": "small", "file_unique_id": "small", "width": 100, "height": 100}, map[string]any{"file_id": "photo", "file_unique_id": "stable-photo", "width": 1000, "height": 1000}}}}
		raw, _ := json.Marshal(payload)
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatal(err)
		}
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatal("replay: ", err)
		}
	}
	sendText := func(updateID, messageID int64, text string) {
		payload := map[string]any{"update_id": updateID, "message": map[string]any{"message_id": messageID, "date": time.Now().Unix(), "chat": map[string]any{"id": 445566, "type": "private"}, "from": map[string]any{"id": 445566, "first_name": "Image Test"}, "text": text}}
		raw, _ := json.Marshal(payload)
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatal(err)
		}
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	check := func(query string, want int) {
		var n int
		if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s: got %d want %d", query, n, want)
		}
	}
	imageKind.Store("food")
	sendPhoto(81001, 91001, "ужин вчера")
	check(`SELECT count(*) FROM meal_entries WHERE source_update_id=81001`, 1)
	check(`SELECT count(*) FROM products`, 0)
	var eaterID, mealDate string
	if err := pool.QueryRow(ctx, `SELECT user_id::text,(eaten_at AT TIME ZONE 'UTC')::date::text FROM meal_entries WHERE source_update_id=81001`).Scan(&eaterID, &mealDate); err != nil {
		t.Fatal(err)
	}
	entries, err := diary.NewRepository(pool).ListByDate(ctx, eaterID, mealDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ProductName != "Яйцо" || entries[0].ProductID != "" {
		t.Fatalf("image meal is not visible in diary: %+v", entries)
	}
	check(`SELECT count(*) FROM telegram_deliveries WHERE update_id=81001`, 1)
	var calories float64
	if err := pool.QueryRow(ctx, `SELECT calories FROM meal_entries WHERE source_update_id=81001`).Scan(&calories); err != nil {
		t.Fatal(err)
	}
	if calories != 150 {
		t.Fatalf("model total was trusted: %v", calories)
	}
	imageKind.Store("scale")
	sendPhoto(81002, 91002, "сегодня")
	check(`SELECT count(*) FROM body_measurements WHERE source_update_id=81002`, 1)
	imageKind.Store("gym_machine_display")
	sendPhoto(81003, 91003, "")
	check(`SELECT count(*) FROM workout_ingestion_increments WHERE telegram_update_id=81003`, 0)
	var pendingType string
	if err := pool.QueryRow(ctx, `SELECT pending_clarification->>'type' FROM conversation_states WHERE chat_id=445566`).Scan(&pendingType); err != nil {
		t.Fatal(err)
	}
	if pendingType != "machine_display_needs_machine" {
		t.Fatal(pendingType)
	}
	before := aiCalls.Load()
	sendText(81004, 91004, "эллипс")
	if aiCalls.Load() != before {
		t.Fatal("clarification reprocessed image")
	}
	check(`SELECT count(*) FROM workout_ingestion_increments WHERE telegram_update_id=81003 AND operation='image_machine_metrics'`, 1)
	check(`SELECT count(*) FROM telegram_deliveries WHERE update_id=81004`, 1)
	var distance float64
	if err := pool.QueryRow(ctx, `SELECT (sets_data->0->>'distance_km')::float8 FROM workout_exercises LIMIT 1`).Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if distance < 5.79 || distance > 5.80 {
		t.Fatalf("mile conversion: %v", distance)
	}
	check(`SELECT count(*) FROM telegram_updates WHERE status='completed' AND update_id BETWEEN 81001 AND 81004`, 4)
	var workoutID string
	if err := pool.QueryRow(ctx, `SELECT workout_id::text FROM workout_ingestion_increments WHERE telegram_update_id=81003`).Scan(&workoutID); err != nil {
		t.Fatal(err)
	}
	var duration int
	if err := pool.QueryRow(ctx, `SELECT duration_min FROM workouts WHERE id=$1`, workoutID).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	if duration != 45 {
		t.Fatalf("machine duration was applied twice: %d", duration)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workout_sessions(user_id,status,proposal_data,applied_workout_id,expires_at) SELECT user_id,'in_progress','{}'::jsonb,$1,NOW()+INTERVAL '3 hours' FROM telegram_users WHERE telegram_user_id=445566`, workoutID); err != nil {
		t.Fatal(err)
	}
	imageKind.Store("gym_machine_display")
	sendPhoto(81005, 91005, "эллипс")
	check(`SELECT count(*) FROM workout_ingestion_increments WHERE workout_id=(SELECT workout_id FROM workout_ingestion_increments WHERE telegram_update_id=81003) AND operation='image_machine_metrics'`, 2)
	sendPhoto(81006, 91006, "эллипс вчера")
	check(`SELECT count(*) FROM workouts WHERE user_id=(SELECT user_id FROM telegram_users WHERE telegram_user_id=445566)`, 2)
	var historical bool
	if err := pool.QueryRow(ctx, `SELECT started_at::date < CURRENT_DATE FROM workouts WHERE telegram_update_id=81006`).Scan(&historical); err != nil {
		t.Fatal(err)
	}
	if !historical {
		t.Fatal("historical gym photo recorded on current date")
	}
	imageKind.Store("food")
	beforeAlbum := aiCalls.Load()
	inAlbum.Store(true)
	for i := int64(0); i < 5; i++ {
		caption := ""
		if i == 0 {
			caption = "Это вчера за весь день\nЙогурт\nРис с курицей овощами и котлетами\nБанан\nПротеиновый коктейльчик\nИ пюре со свиными ребрышками"
		}
		payload := map[string]any{"update_id": 81007 + i, "message": map[string]any{"message_id": 91007 + i, "media_group_id": "album-1", "caption": caption, "date": time.Now().Unix(), "chat": map[string]any{"id": 445566, "type": "private"}, "from": map[string]any{"id": 445566, "first_name": "Image Test"}, "photo": []any{map[string]any{"file_id": "photo", "file_unique_id": "album-photo", "width": 1000, "height": 1000}}}}
		raw, _ := json.Marshal(payload)
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatal(err)
		}
		if err := svc.ProcessWebhookUpdate(ctx, raw); err != nil {
			t.Fatalf("album replay: %v", err)
		}
	}
	if aiCalls.Load() != beforeAlbum {
		t.Fatal("album was analyzed before quiet window")
	}
	if _, err := pool.Exec(ctx, `UPDATE telegram_media_groups SET last_seen_at=NOW()-INTERVAL '3 seconds' WHERE media_group_id='album-1'`); err != nil {
		t.Fatal(err)
	}
	if processed, err := svc.ProcessReadyMediaGroups(ctx, 10); err != nil || processed != 1 {
		t.Fatalf("album processing: %d %v", processed, err)
	}
	inAlbum.Store(false)
	if aiCalls.Load() <= beforeAlbum {
		t.Fatal("album photos were not analyzed")
	}
	check(`SELECT count(*) FROM telegram_deliveries WHERE update_id BETWEEN 81007 AND 81011`, 1)
	check(`SELECT count(*) FROM meal_observations WHERE logical_turn_id='album:album-1'`, 5)
	check(`SELECT count(*) FROM meal_observations WHERE logical_turn_id='album:album-1' AND item->>'name_ru'='Йогурт'`, 1)
	var albumDay string
	if err := pool.QueryRow(ctx, `SELECT (eaten_at AT TIME ZONE 'UTC')::date::text FROM meal_observations WHERE logical_turn_id='album:album-1' LIMIT 1`).Scan(&albumDay); err != nil {
		t.Fatal(err)
	}
	if albumDay != time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatalf("album day: %s", albumDay)
	}
	insertUpdate := func(id int64) {
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,raw_payload) VALUES($1,445566,445566,'{}'::jsonb)`, id); err != nil {
			t.Fatal(err)
		}
	}
	insertUpdate(81012)
	corrected, err := orch.ApplyMealMutation(ctx, assistant.AssistantEvent{UserID: eaterID, ChatID: 445566, UpdateID: 81012, MessageID: 91012, Text: "это именно ребра свиные"}, assistant.MealMutation{Operation: "correct_item_identity", NewName: "ребра свиные"})
	if err != nil || corrected.NeedsClarify {
		t.Fatalf("album identity correction: %+v %v", corrected, err)
	}
	check(`SELECT count(*) FROM meal_observation_corrections WHERE source_update_id=81012`, 1)
	var correctedName string
	if err := pool.QueryRow(ctx, `SELECT item->>'name_ru' FROM meal_observations WHERE logical_turn_id='album:album-1' ORDER BY image_index DESC LIMIT 1`).Scan(&correctedName); err != nil {
		t.Fatal(err)
	}
	if correctedName != "ребра свиные" {
		t.Fatalf("corrected observation: %q", correctedName)
	}
	imageKind.Store("food")
	sendPhoto(81013, 91013, "Во")
	var continuationDay, continuationType string
	if err := pool.QueryRow(ctx, `SELECT (eaten_at AT TIME ZONE 'UTC')::date::text,meal_type FROM meal_entries WHERE source_update_id=81013`).Scan(&continuationDay, &continuationType); err != nil {
		t.Fatal(err)
	}
	if continuationDay != albumDay || continuationType != "dinner" {
		t.Fatalf("continuation saved on %s/%s, want yesterday dinner", continuationDay, continuationType)
	}
	insertUpdate(81014)
	saved, err := orch.ApplyMealMutation(ctx, assistant.AssistantEvent{UserID: eaterID, ChatID: 445566, UpdateID: 81014, MessageID: 91014, Text: "запиши все за вчера"}, assistant.MealMutation{Operation: "bulk_save", DateText: "вчера"})
	if err != nil || saved.NeedsClarify {
		t.Fatalf("bulk save: %+v %v", saved, err)
	}
	check(`SELECT count(*) FROM meal_entries WHERE source_update_id BETWEEN 81007 AND 81011`, 5)
	check(`SELECT count(*) FROM meal_entries WHERE observed_name='ребра свиные'`, 1)
	insertUpdate(81015)
	savedAgain, err := orch.ApplyMealMutation(ctx, assistant.AssistantEvent{UserID: eaterID, ChatID: 445566, UpdateID: 81015, MessageID: 91015, Text: "запиши все за вчера"}, assistant.MealMutation{Operation: "bulk_save", DateText: "вчера"})
	if err != nil || savedAgain.NeedsClarify {
		t.Fatalf("repeated bulk save: %+v %v", savedAgain, err)
	}
	check(`SELECT count(*) FROM meal_entries WHERE source_update_id BETWEEN 81007 AND 81011`, 5)
	insertUpdate(81016)
	volume := 650.0
	refined, err := orch.ApplyMealMutation(ctx, assistant.AssistantEvent{UserID: eaterID, ChatID: 445566, UpdateID: 81016, MessageID: 91016, Text: "контейнер 0.6-0.7"}, assistant.MealMutation{Operation: "refine_portion", VolumeML: &volume})
	if err != nil || refined.NeedsClarify {
		t.Fatalf("portion refinement: %+v %v", refined, err)
	}
	check(`SELECT count(*) FROM meal_entry_corrections WHERE source_update_id=81016`, 1)
	var refinedCalories float64
	if err := pool.QueryRow(ctx, `SELECT calories FROM meal_entries WHERE source_update_id=81013`).Scan(&refinedCalories); err != nil {
		t.Fatal(err)
	}
	if refinedCalories != 650 {
		t.Fatalf("refined calories: %v", refinedCalories)
	}
	if _, err := orch.ApplyMealMutation(ctx, assistant.AssistantEvent{UserID: eaterID, ChatID: 445566, UpdateID: 81016, MessageID: 91016, Text: "контейнер 0.6-0.7"}, assistant.MealMutation{Operation: "refine_portion", VolumeML: &volume}); err != nil {
		t.Fatal(err)
	}
	check(`SELECT count(*) FROM meal_entry_corrections WHERE source_update_id=81016`, 1)

	imageKind.Store("unknown")
	sendPhoto(81019, 91019, "")
	var unknownType string
	if err := pool.QueryRow(ctx, `SELECT pending_clarification->>'type' FROM conversation_states WHERE chat_id=445566`).Scan(&unknownType); err != nil {
		t.Fatal(err)
	}
	if unknownType != "image_unknown_needs_intent" {
		t.Fatalf("unknown image did not save clarification: %s", unknownType)
	}
	sendText(81020, 91020, "что это?")
	check(`SELECT count(*) FROM conversation_states WHERE chat_id=445566 AND pending_clarification IS NOT NULL`, 0)
	imageKind.Store("nutrition_label")
	sendPhoto(81021, 91021, "сохрани продукт")
	check(`SELECT count(*) FROM products WHERE source_update_id=81021 AND nutrition_source='printed_label'`, 1)
	var per100 float64
	if err := pool.QueryRow(ctx, `SELECT calories FROM products WHERE source_update_id=81021`).Scan(&per100); err != nil {
		t.Fatal(err)
	}
	if per100 != 210 {
		t.Fatalf("serving normalization: %v", per100)
	}
	check(`SELECT count(*) FROM telegram_deliveries WHERE update_id=81021`, 1)
	if _, err := pool.Exec(ctx, `UPDATE users SET timezone='Pacific/Honolulu' WHERE id=$1`, eaterID); err != nil {
		t.Fatal(err)
	}
	imageKind.Store("food")
	sendPhoto(81022, 91022, "завтрак вчера")
	loc, _ := time.LoadLocation("Pacific/Honolulu")
	wantDate := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	var localDate, mealType string
	if err := pool.QueryRow(ctx, `SELECT (eaten_at AT TIME ZONE 'Pacific/Honolulu')::date::text,meal_type FROM meal_entries WHERE source_update_id=81022`).Scan(&localDate, &mealType); err != nil {
		t.Fatal(err)
	}
	if localDate != wantDate || mealType != "breakfast" {
		t.Fatalf("local date/meal mapping: %s %s want %s breakfast", localDate, mealType, wantDate)
	}
	dated, err := diary.NewRepository(pool).ListByDate(ctx, eaterID, wantDate)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range dated {
		if e.MealType == "breakfast" && e.ProductName == "Яйцо" {
			found = true
		}
	}
	if !found {
		t.Fatal("image meal not listed on user-local date")
	}
}
