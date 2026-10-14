package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func profileFact(ns, key string, value any) FactProposal {
	return FactProposal{Namespace: ns, Subject: "self", Predicate: key, Value: value, Operation: "replace", Confidence: 1, Evidence: "profile.json"}
}
func profileNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
func profileFacts(data map[string]any) []FactProposal {
	facts := []FactProposal{}
	if person, ok := data["person"].(map[string]any); ok {
		for _, field := range []string{"name", "sex"} {
			if v, ok := person[field].(string); ok && strings.TrimSpace(v) != "" {
				facts = append(facts, profileFact("identity", field, strings.TrimSpace(v)))
			}
		}
		if v, ok := profileNumber(person["age"]); ok && v >= 1 && v <= 120 {
			facts = append(facts, profileFact("identity", "age", int(v)))
		}
		if v, ok := profileNumber(person["height_cm"]); ok && v >= 100 && v <= 250 {
			facts = append(facts, profileFact("identity", "height_cm", v))
		}
	}
	if person, ok := data["person"].(map[string]any); !ok || person["age"] == nil {
		if measurements, ok := data["measurements"].([]any); ok {
			var latest time.Time
			var age int
			for _, raw := range measurements {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				at, ok := profileMeasurementTime(m["analysis_date"], time.UTC)
				if !ok {
					at, ok = profileMeasurementTime(m["timestamp"], time.UTC)
				}
				if !ok || !at.After(latest) {
					continue
				}
				if n, ok := profileNumber(m["age"]); ok && n >= 1 && n <= 120 {
					latest, age = at, int(n)
				}
			}
			if age > 0 {
				facts = append(facts, profileFact("identity", "age", age))
			}
		}
	}
	if location, ok := data["location"].(map[string]any); ok {
		if v, ok := location["home_city"].(string); ok && strings.TrimSpace(v) != "" {
			facts = append(facts, profileFact("location", "home_city", v))
		}
	}
	if v, ok := data["home_city"].(string); ok && strings.TrimSpace(v) != "" {
		facts = append(facts, profileFact("location", "home_city", v))
	}
	if v, ok := data["timezone"].(string); ok && v != "" {
		facts = append(facts, profileFact("location", "timezone", v))
	}
	if v, ok := data["goal"].(string); ok && v != "" {
		facts = append(facts, profileFact("fitness", "goal", v))
	}
	if training, ok := data["training_preferences"].(map[string]any); ok {
		if v, ok := training["preferred_training_time"].(string); ok && v != "" {
			facts = append(facts, profileFact("fitness", "preferred_training_time", v))
		}
	}
	if nutrition, ok := data["nutrition_preferences"].(map[string]any); ok {
		if v, ok := nutrition["diet_preference"].(string); ok && v != "" {
			facts = append(facts, profileFact("nutrition", "diet_preference", v))
		}
	}
	return facts
}
func profileMeasurementTime(v any, loc *time.Location) (time.Time, bool) {
	value, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{"02.01.2006", "2006-01-02", time.RFC3339} {
		if at, err := time.ParseInLocation(layout, value, loc); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}
func profileMeasurementFields(m map[string]any) map[string]any {
	fields := map[string]any{}
	for key, col := range map[string]string{"weight_kg": "weight_kg", "bmi": "bmi"} {
		if v, ok := profileNumber(m[key]); ok {
			fields[col] = v
		}
	}
	if analysis, ok := m["body_analysis"].(map[string]any); ok {
		for key, col := range map[string]string{"body_mass_index": "bmi", "basal_metabolic_rate_kcal": "bmr_kcal", "fat_mass_kg": "fat_total_kg", "fat_percentage": "fat_percentage", "visceral_fat_rate": "visceral_level", "muscle_mass_kg": "muscle_total_kg", "muscle_index_kg_per_m2": "muscle_index", "water_mass_kg": "water_total_kg", "mineral_mass_kg": "minerals_total_kg", "protein_mass_kg": "protein_total_kg", "waist-hip_ratio": "waist_hip_ratio"} {
			if v, ok := profileNumber(analysis[key]); ok {
				fields[col] = v
			}
		}
	}
	return fields
}
func (k *PersonalKnowledge) ImportProfileTx(ctx context.Context, tx pgx.Tx, userID string, updateID, messageID *int64, data map[string]any) error {
	for _, proposal := range profileFacts(data) {
		if _, err := k.applyTx(ctx, tx, userID, updateID, messageID, "profile_import", proposal); err != nil {
			return err
		}
	}
	tz := "UTC"
	if v, ok := data["timezone"].(string); ok && v != "" {
		tz = v
	}
	loc := ResolveTimezone(tz, "UTC")
	measurements, _ := data["measurements"].([]any)
	for _, raw := range measurements {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		at, ok := profileMeasurementTime(m["analysis_date"], loc)
		if !ok {
			at, ok = profileMeasurementTime(m["timestamp"], loc)
		}
		if !ok {
			continue
		}
		fields := profileMeasurementFields(m)
		if len(fields) == 0 {
			continue
		}
		encoded, _ := json.Marshal(m)
		digest := sha256.Sum256(append([]byte(userID+"/"), encoded...))
		fingerprint := hex.EncodeToString(digest[:])
		// The profile remains the provenance source; time-series data belongs to body_measurements.
		_, err := tx.Exec(ctx, `INSERT INTO body_measurements(user_id,logged_at,weight_kg,bmi,bmr_kcal,fat_total_kg,fat_percentage,visceral_level,muscle_total_kg,muscle_index,water_total_kg,minerals_total_kg,protein_total_kg,waist_hip_ratio,source_profile_fingerprint)
          VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT(user_id,source_profile_fingerprint) WHERE source_profile_fingerprint IS NOT NULL DO NOTHING`,
			userID, at, fields["weight_kg"], fields["bmi"], fields["bmr_kcal"], fields["fat_total_kg"], fields["fat_percentage"], fields["visceral_level"], fields["muscle_total_kg"], fields["muscle_index"], fields["water_total_kg"], fields["minerals_total_kg"], fields["protein_total_kg"], fields["waist_hip_ratio"], fingerprint)
		if err != nil {
			return fmt.Errorf("import body measurement: %w", err)
		}
	}
	return nil
}
func (k *PersonalKnowledge) BackfillProfiles(ctx context.Context) error {
	rows, err := k.db.Query(ctx, `SELECT id,profile_data FROM users WHERE profile_data IS NOT NULL`)
	if err != nil {
		return err
	}
	type entry struct {
		id   string
		data json.RawMessage
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.data); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, entry := range entries {
		var data map[string]any
		dec := json.NewDecoder(strings.NewReader(string(entry.data)))
		dec.UseNumber()
		if err := dec.Decode(&data); err != nil {
			return err
		}
		tx, err := k.db.Begin(ctx)
		if err != nil {
			return err
		}
		if err := k.ImportProfileTx(ctx, tx, entry.id, nil, nil, data); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
