package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jasmine/internal/module/assistant"
)

func TestPostgreSQL_PersonalKnowledgeCheckpoint(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,timezone) VALUES('knowledge-checkpoint','Europe/Moscow') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO telegram_users(telegram_user_id,user_id) VALUES(982211,$1)`, userID); err != nil {
		t.Fatal(err)
	}
	k := assistant.NewPersonalKnowledge(pool, nil, "")
	profile := map[string]any{
		"person":   map[string]any{"name": "Eduard", "height_cm": 177.5},
		"timezone": "Europe/Moscow", "goal": "muscle_gain", "location": map[string]any{"home_city": "Сочи"},
		"training_preferences":  map[string]any{"preferred_training_time": "after_work"},
		"nutrition_preferences": map[string]any{"diet_preference": "high_protein"},
		"measurements":          []any{map[string]any{"analysis_date": "19.01.2026", "age": 23, "weight_kg": 71.5, "body_analysis": map[string]any{"fat_percentage": 20.8, "visceral_fat_rate": 5}}},
	}
	importProfile := func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := k.ImportProfileTx(ctx, tx, userID, nil, nil, profile); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	importProfile()
	importProfile()
	var measurements int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM body_measurements WHERE user_id=$1`, userID).Scan(&measurements); err != nil {
		t.Fatal(err)
	}
	if measurements != 1 {
		t.Fatalf("profile measurement duplicated: %d", measurements)
	}
	relevant, err := k.Relevant(ctx, userID, "кто я")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, f := range relevant {
		values[f.Namespace+"/"+f.Predicate] = string(f.Value)
	}
	for key, want := range map[string]string{"identity/name": `"Eduard"`, "identity/age": "23", "fitness/goal": `"muscle_gain"`, "fitness/preferred_training_time": `"after_work"`} {
		if values[key] != want {
			t.Fatalf("imported %s = %s, want %s", key, values[key], want)
		}
	}
	observe := func(update int64, message string) {
		if _, err := pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,raw_payload) VALUES($1,982211,982211,'{}'::jsonb)`, update); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Observe(ctx, userID, update, update+1000, message); err != nil {
			t.Fatal(err)
		}
	}
	observe(20001, "я живу в Сочи")
	observe(20002, "я теперь живу в Москве")
	var activeCity, historicalCity int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_memories WHERE user_id=$1 AND namespace='location' AND predicate='home_city' AND value='"Москва"'::jsonb AND valid_until IS NULL`, userID).Scan(&activeCity); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_memories WHERE user_id=$1 AND namespace='location' AND predicate='home_city' AND value='"Сочи"'::jsonb AND valid_until IS NOT NULL`, userID).Scan(&historicalCity); err != nil {
		t.Fatal(err)
	}
	if activeCity != 1 || historicalCity != 2 {
		t.Fatalf("city supersession active=%d history=%d", activeCity, historicalCity)
	}
	observe(20003, "на ноуте Arch")
	observe(20004, "у меня на ноутбуке Arch Linux")
	observe(20005, "на рабочем компе Windows")
	var devices int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_memories WHERE user_id=$1 AND namespace='device' AND predicate='operating_system' AND valid_until IS NULL`, userID).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if devices != 2 {
		t.Fatalf("canonical device scopes: %d", devices)
	}
	fitness, err := k.Relevant(ctx, userID, "сколько мне ставить на лег курле?")
	if err != nil {
		t.Fatal(err)
	}
	seenFitness, seenDevice := false, false
	for _, f := range fitness {
		if f.Namespace == "fitness" {
			seenFitness = true
		}
		if f.Namespace == "device" {
			seenDevice = true
		}
	}
	if !seenFitness || seenDevice {
		t.Fatalf("selective retrieval fitness=%v device=%v", seenFitness, seenDevice)
	}
	observe(20006, "забудь что я живу в Москве")
	importProfile()
	afterForget, err := k.Relevant(ctx, userID, "кто я")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range afterForget {
		if f.Namespace == "location" && f.Predicate == "home_city" {
			t.Fatalf("forgotten city returned: %+v", f)
		}
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM personal_fact_events WHERE user_id=$1 AND predicate='home_city'`, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 4 {
		t.Fatalf("city audit events: %d", events)
	}
	// A new profile import has lower authority than the later explicit conversation.
	observe(20007, "я живу в Москве")
	profile["person"].(map[string]any)["name"] = "Eduard"
	importProfile()
	active, err := k.Relevant(ctx, userID, "кто я")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(active)
	if !strings.Contains(string(encoded), "Москва") {
		t.Fatalf("explicit city lost after profile import: %s", encoded)
	}
}
