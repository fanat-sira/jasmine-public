package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

// imageAction commits the application-owned mutation, outbox delivery and update
// completion together. Vision output never receives a database handle.
func (o *Orchestrator) imageAction(ctx context.Context, userID string, chatID, updateID, messageID int64, refs *[]telegram.DomainReference, write func(pgx.Tx) error) (*telegram.IngestionResponse, error) {
	if o.db == nil || o.telegramRepo == nil {
		return nil, fmt.Errorf("image persistence is not configured")
	}
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := write(tx); err != nil {
		return nil, err
	}
	draftID := telegram.StableDraftID(updateID)
	event := "image_result"
	for _, ref := range *refs {
		switch ref.Type {
		case "meal":
			event = "meal_logged"
		case "body_measurement":
			event = "body_measurement_logged"
		case "workout":
			event = "machine_workout_logged"
		case "product":
			event = "product_saved"
		}
	}
	replyFacts := &telegram.ReplyContext{EventType: event, ActionsPerformed: []string{event}, ToolResults: *refs}
	payload, err := json.Marshal(telegram.OutboxPayload{DraftID: &draftID, Text: "", ReplyContext: replyFacts, ParseMode: "HTML", ReplyToMessageID: &messageID, DomainReferences: *refs})
	if err != nil {
		return nil, err
	}
	delivery := &telegram.TelegramDelivery{CorrelationID: correlation.GetCorrelationID(ctx), UpdateID: &updateID, ChatID: chatID, UserID: userID, Status: telegram.DeliveryStatusPending, Payload: payload}
	if err := o.telegramRepo.CreateDeliveryTx(ctx, tx, delivery); err != nil {
		return nil, err
	}
	summary, err := json.Marshal(map[string]any{"action": "image_logged", "delivery_id": delivery.ID})
	if err != nil {
		return nil, err
	}
	if err := o.telegramRepo.CompleteUpdateTx(ctx, tx, updateID, summary); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &telegram.IngestionResponse{Success: true, ReplyContext: replyFacts, DomainReferences: *refs}, nil
}

func imageMealType(caption string, localTime time.Time) string {
	t := strings.ToLower(caption)
	switch {
	case strings.Contains(t, "завтрак"), strings.Contains(t, "breakfast"):
		return "breakfast"
	case strings.Contains(t, "обед"), strings.Contains(t, "lunch"):
		return "lunch"
	case strings.Contains(t, "ужин"), strings.Contains(t, "dinner"):
		return "dinner"
	case strings.Contains(t, "перекус"), strings.Contains(t, "snack"):
		return "snack"
	}
	h := localTime.Hour()
	if h < 11 {
		return "breakfast"
	}
	if h < 17 {
		return "lunch"
	}
	if h < 22 {
		return "dinner"
	}
	return "snack"
}

var imageDayMonth = regexp.MustCompile(`(?i)(\d{1,2})\s+(января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря)`)
var imageMonths = map[string]time.Month{"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12}
var imageWeekdays = map[string]time.Weekday{"понедельник": time.Monday, "вторник": time.Tuesday, "среду": time.Wednesday, "четверг": time.Thursday, "пятницу": time.Friday, "субботу": time.Saturday, "воскресенье": time.Sunday}

func resolveImageTimeAt(now time.Time, caption string) (time.Time, bool, error) {
	lower := strings.ToLower(caption)
	at := now
	explicit := false
	switch {
	case strings.Contains(lower, "позавчера"):
		at = at.AddDate(0, 0, -2)
		explicit = true
	case strings.Contains(lower, "вчера"):
		at = at.AddDate(0, 0, -1)
		explicit = true
	default:
		if match := imageDayMonth.FindStringSubmatch(lower); len(match) == 3 {
			day, _ := strconv.Atoi(match[1])
			month := imageMonths[match[2]]
			candidate := time.Date(now.Year(), month, day, now.Hour(), now.Minute(), now.Second(), 0, now.Location())
			if candidate.Month() != month || candidate.Day() != day {
				return time.Time{}, false, fmt.Errorf("invalid image date")
			}
			if candidate.After(now.Add(24 * time.Hour)) {
				candidate = candidate.AddDate(-1, 0, 0)
			}
			at = candidate
			explicit = true
		} else {
			for word, day := range imageWeekdays {
				if strings.Contains(lower, word) {
					delta := (int(now.Weekday()) - int(day) + 7) % 7
					at = at.AddDate(0, 0, -delta)
					explicit = true
					break
				}
			}
		}
	}
	if strings.Contains(lower, "утром") {
		at = time.Date(at.Year(), at.Month(), at.Day(), 9, 0, 0, 0, at.Location())
	}
	if strings.Contains(lower, "вечером") {
		at = time.Date(at.Year(), at.Month(), at.Day(), 19, 0, 0, 0, at.Location())
	}
	return at, explicit, nil
}
func (o *Orchestrator) imageLocalTime(ctx context.Context, userID, caption string) (time.Time, error) {
	var tz string
	if o.db != nil {
		err := o.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),NULLIF(tu.timezone,''),'UTC') FROM users u LEFT JOIN telegram_users tu ON tu.user_id=u.id WHERE u.id=$1`, userID).Scan(&tz)
		if err != nil {
			return time.Time{}, err
		}
	}
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	at, _, err := resolveImageTimeAt(time.Now().In(loc), caption)
	return at, err
}

func (o *Orchestrator) saveImageMeal(ctx context.Context, userID string, chatID, updateID, messageID int64, caption string, image telegram.ImageInput, d *FoodAnalysisDraft, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	at, mealType, err := o.mealPhotoTimeAndType(ctx, userID, caption)
	if err != nil {
		return nil, err
	}
	return o.imageAction(ctx, userID, chatID, updateID, messageID, &refs, func(tx pgx.Tx) error {
		var groupID string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&groupID); err != nil {
			return err
		}
		refs = append(refs, telegram.DomainReference{Type: "meal", ID: groupID, Extra: map[string]any{"meal_type": mealType, "eaten_at": at, "nutrition": d.Total, "items": d.Items}})
		for i, item := range d.Items {
			name := strings.TrimSpace(item.NameRU)
			if name == "" {
				name = strings.TrimSpace(item.NameEN)
			}
			if name == "" {
				return fmt.Errorf("food item has no name")
			}
			_, err := tx.Exec(ctx, `INSERT INTO meal_entries (user_id,observed_name,meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,meal_group_id,source_update_id,source_message_id,source_item_index,correlation_id,source_file_unique_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, userID, name, mealType, item.WeightG, item.Calories, item.ProteinG, item.FatG, item.CarbsG, at, groupID, updateID, messageID, i, correlation.GetCorrelationID(ctx), image.FileUniqueID)
			if err != nil {
				return err
			}
		}
		return o.setMealContextTx(ctx, tx, userID, groupID, mealType, at, updateID)
	})
}

func (o *Orchestrator) saveImageScale(ctx context.Context, userID string, chatID, updateID, messageID int64, caption string, image telegram.ImageInput, d *ScaleMeasurementDraft, refs []telegram.DomainReference) (*telegram.IngestionResponse, error) {
	at, err := o.imageLocalTime(ctx, userID, caption)
	if err != nil {
		return nil, err
	}
	return o.imageAction(ctx, userID, chatID, updateID, messageID, &refs, func(tx pgx.Tx) error {
		var measurementID string
		err := tx.QueryRow(ctx, `INSERT INTO body_measurements (user_id,logged_at,weight_kg,bmi,fat_percentage,muscle_total_kg,water_percentage,visceral_level,source_update_id,source_message_id,correlation_id,source_file_unique_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, userID, at, d.WeightKG, d.BMI, d.BodyFatPercent, d.MuscleMassKG, d.WaterPercent, d.VisceralLevel, updateID, messageID, correlation.GetCorrelationID(ctx), image.FileUniqueID).Scan(&measurementID)
		if err != nil {
			return err
		}
		refs = append(refs, telegram.DomainReference{Type: "body_measurement", ID: measurementID, Extra: map[string]any{"weight_kg": d.WeightKG, "logged_at": at}})
		_, err = tx.Exec(ctx, `UPDATE users SET weight_kg=$2,updated_at=NOW() WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM body_measurements WHERE user_id=$1 AND logged_at>$3)`, userID, d.WeightKG, at)
		return err
	})
}
