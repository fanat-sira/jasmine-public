package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"jasmine/internal/module/prompts"
	"jasmine/internal/module/telegram"
	"jasmine/internal/pkg/correlation"
)

type albumObservation struct {
	ImageIndex int                  `json:"image_index"`
	ImageType  ImageType            `json:"image_type"`
	Items      []FoodAnalysisItem   `json:"items,omitempty"`
	Label      *NutritionLabelDraft `json:"label,omitempty"`
	Evidence   []string             `json:"evidence,omitempty"`
}
type albumAssignment struct {
	ImageIndex   int    `json:"image_index"`
	MealType     string `json:"meal_type"`
	NameOverride string `json:"name_override,omitempty"`
}
type albumInterpretation struct {
	Assignments []albumAssignment `json:"assignments"`
}

func albumCaptionNames(caption string) []string {
	lines := strings.Split(caption, "\n")
	names := []string{}
	for _, line := range lines[1:] {
		if v := strings.TrimSpace(line); v != "" {
			names = append(names, v)
		}
	}
	return names
}
func validMealType(v string) bool {
	switch v {
	case "breakfast", "lunch", "dinner", "snack":
		return true
	}
	return false
}
func (a *ImageAnalyzer) InterpretAlbum(ctx context.Context, caption string, observations []albumObservation) ([]albumAssignment, error) {
	if len(observations) == 0 {
		return nil, fmt.Errorf("album has no observations")
	}
	names := albumCaptionNames(caption)
	fallback := make([]albumAssignment, len(observations))
	for i, obs := range observations {
		fallback[i] = albumAssignment{ImageIndex: obs.ImageIndex, MealType: "snack"}
		if i < len(names) {
			fallback[i].NameOverride = names[i]
		}
	}
	if a == nil || a.client == nil || a.mainModel == "" {
		return fallback, nil
	}
	input, _ := json.Marshal(map[string]any{"caption": caption, "observations": observations})
	prompt, err := prompts.Resolve(ctx, a.prompts, prompts.KeyAlbumInterpretation)
	if err != nil {
		return nil, err
	}
	aiCtx, cancel := context.WithTimeout(correlation.WithOperation(ctx, "album_interpretation"), 30*time.Second)
	defer cancel()
	raw, err := a.client.SendConversation(aiCtx, a.mainModel, prompt, nil, string(input))
	if err != nil {
		return nil, err
	}
	var result albumInterpretation
	if err := json.Unmarshal([]byte(stripImageFences(raw)), &result); err != nil {
		return nil, err
	}
	if len(result.Assignments) != len(observations) {
		return nil, fmt.Errorf("album assignments incomplete")
	}
	seen := map[int]bool{}
	for i, assigned := range result.Assignments {
		if assigned.ImageIndex < 0 || assigned.ImageIndex >= len(observations) || seen[assigned.ImageIndex] || !validMealType(assigned.MealType) {
			return nil, fmt.Errorf("invalid album assignment")
		}
		seen[assigned.ImageIndex] = true
		// A model cannot introduce food identity absent from the user's caption.
		if assigned.NameOverride != "" && !strings.Contains(strings.ToLower(caption), strings.ToLower(assigned.NameOverride)) {
			result.Assignments[i].NameOverride = ""
		}
		if result.Assignments[i].NameOverride == "" && assigned.ImageIndex < len(names) {
			result.Assignments[i].NameOverride = names[assigned.ImageIndex]
		}
	}
	sort.Slice(result.Assignments, func(i, j int) bool { return result.Assignments[i].ImageIndex < result.Assignments[j].ImageIndex })
	return result.Assignments, nil
}
func albumSaveRequested(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "запиши") || strings.Contains(t, "сохрани") || strings.Contains(t, "добавь в дневник")
}
func albumLabelItem(d *NutritionLabelDraft) (*FoodAnalysisItem, error) {
	per100, err := labelPer100(d)
	if err != nil {
		return nil, err
	}
	weight, ok := labelMealWeight(d, "")
	if !ok {
		return nil, fmt.Errorf("label package weight unknown")
	}
	name := "Продукт с фото"
	if d.ProductName != nil && strings.TrimSpace(*d.ProductName) != "" {
		name = strings.TrimSpace(*d.ProductName)
	}
	factor := weight / 100
	item := &FoodAnalysisItem{NameRU: name, WeightG: weight, Calories: per100.Calories * factor, ProteinG: per100.ProteinG * factor, FatG: per100.FatG * factor, CarbsG: per100.CarbsG * factor, Confidence: "label"}
	return item, nil
}
func (o *Orchestrator) IngestAlbum(ctx context.Context, userID string, chatID int64, album telegram.AlbumInput) (*telegram.IngestionResponse, error) {
	if o.db == nil || o.telegramRepo == nil || o.imageAnalyzer == nil {
		return nil, fmt.Errorf("album pipeline unavailable")
	}
	if len(album.Items) == 0 || len(album.Items) > telegram.MaxAlbumPhotos {
		return nil, fmt.Errorf("invalid album size")
	}
	at, err := o.imageLocalTime(ctx, userID, album.Caption)
	if err != nil {
		return nil, err
	}
	observations := make([]albumObservation, 0, len(album.Items))
	for i, member := range album.Items {
		turn := MultimodalTurn{Text: album.Caption, UpdateID: member.UpdateID, MessageID: member.MessageID, CorrelationID: correlation.GetCorrelationID(ctx), Attachment: ImageAttachment{FileID: member.Image.FileID, FileUniqueID: member.Image.FileUniqueID, MIMEType: member.Image.MIMEType, Bytes: member.Image.Bytes}}
		classification, err := o.imageAnalyzer.Classify(ctx, turn)
		if err != nil {
			return nil, err
		}
		obs := albumObservation{ImageIndex: i, ImageType: classification.Type, Evidence: classification.Evidence}
		switch classification.Type {
		case ImageFood:
			draft, err := o.imageAnalyzer.Food(ctx, turn)
			if err != nil {
				return nil, err
			}
			obs.Items = draft.Items
		case ImageNutritionLabel:
			label, err := o.imageAnalyzer.Label(ctx, turn)
			if err != nil {
				return nil, err
			}
			obs.Label = label
			item, err := albumLabelItem(label)
			if err == nil {
				obs.Items = []FoodAnalysisItem{*item}
			}
		}
		observations = append(observations, obs)
	}
	assignments, err := o.imageAnalyzer.InterpretAlbum(ctx, album.Caption, observations)
	if err != nil {
		return nil, err
	}
	byIndex := map[int]albumAssignment{}
	for _, a := range assignments {
		byIndex[a.ImageIndex] = a
	}
	save := albumSaveRequested(album.Caption)
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var existing int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM telegram_deliveries WHERE update_id=$1`, album.Items[0].UpdateID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return &telegram.IngestionResponse{Success: true, ReplyContext: &telegram.ReplyContext{EventType: "album_already_processed"}}, nil
	}
	refs := []telegram.DomainReference{}
	staged := 0
	persisted := 0
	for i, obs := range observations {
		assigned := byIndex[i]
		member := album.Items[i]
		groupID := ""
		if save && len(obs.Items) > 0 {
			if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&groupID); err != nil {
				return nil, err
			}
		}
		for itemIndex, item := range obs.Items {
			if assigned.NameOverride != "" && len(obs.Items) == 1 {
				item.NameRU = assigned.NameOverride
			}
			var entryID *string
			if save {
				var id string
				err := tx.QueryRow(ctx, `INSERT INTO meal_entries(user_id,observed_name,meal_type,weight_g,calories,proteins,fats,carbs,eaten_at,meal_group_id,source_update_id,source_message_id,source_item_index,correlation_id,source_file_unique_id)
                   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
                   ON CONFLICT(user_id,source_update_id,source_item_index) WHERE source_update_id IS NOT NULL DO UPDATE SET observed_name=meal_entries.observed_name RETURNING id`, userID, item.NameRU, assigned.MealType, item.WeightG, item.Calories, item.ProteinG, item.FatG, item.CarbsG, at, groupID, member.UpdateID, member.MessageID, itemIndex, correlation.GetCorrelationID(ctx), member.Image.FileUniqueID).Scan(&id)
				if err != nil {
					return nil, err
				}
				entryID = &id
				persisted++
			}
			itemJSON, _ := json.Marshal(item)
			_, err := tx.Exec(ctx, `INSERT INTO meal_observations(user_id,logical_turn_id,source_update_id,source_message_id,source_file_unique_id,image_index,item_index,item,meal_type,eaten_at,persisted_entry_id)
               VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11) ON CONFLICT(user_id,logical_turn_id,image_index,item_index) DO UPDATE SET persisted_entry_id=COALESCE(EXCLUDED.persisted_entry_id,meal_observations.persisted_entry_id)`, userID, "album:"+album.MediaGroupID, member.UpdateID, member.MessageID, member.Image.FileUniqueID, i, itemIndex, string(itemJSON), assigned.MealType, at, entryID)
			if err != nil {
				return nil, err
			}
			staged++
		}
		refs = append(refs, telegram.DomainReference{Type: "image_workflow", Kind: string(obs.ImageType), Extra: map[string]any{"image_index": i, "source_update_id": member.UpdateID, "source_message_id": member.MessageID, "file_unique_id": member.Image.FileUniqueID, "meal_type": assigned.MealType, "items": obs.Items, "label": obs.Label}})
	}
	if len(assignments) > 0 {
		last := assignments[len(assignments)-1]
		_, err = tx.Exec(ctx, `INSERT INTO meal_conversation_context(user_id,logical_turn_id,eaten_at,meal_type,source_update_id,active_until)
            VALUES($1,$5,$2,$3,$4,NOW()+INTERVAL '24 hours') ON CONFLICT(user_id) DO UPDATE SET meal_group_id=NULL,logical_turn_id=EXCLUDED.logical_turn_id,eaten_at=EXCLUDED.eaten_at,meal_type=EXCLUDED.meal_type,source_update_id=EXCLUDED.source_update_id,active_until=EXCLUDED.active_until,updated_at=NOW()`, userID, at, last.MealType, album.Items[0].UpdateID, "album:"+album.MediaGroupID)
		if err != nil {
			return nil, err
		}
	}
	reply := &telegram.ReplyContext{EventType: "album_analyzed", CurrentState: map[string]any{"media_group_id": album.MediaGroupID, "eaten_at": at, "assignments": assignments, "observations": observations, "staged_items": staged, "persisted_items": persisted}, ToolResults: refs}
	if save {
		reply.EventType = "album_meals_logged"
		reply.ActionsPerformed = []string{"meals.bulk_log"}
	}
	payload, err := json.Marshal(telegram.OutboxPayload{Text: "", ReplyContext: reply, ParseMode: "HTML", ReplyToMessageID: &album.Items[0].MessageID, DomainReferences: refs})
	if err != nil {
		return nil, err
	}
	delivery := &telegram.TelegramDelivery{UpdateID: &album.Items[0].UpdateID, ChatID: chatID, UserID: userID, CorrelationID: correlation.GetCorrelationID(ctx), Status: telegram.DeliveryStatusPending, Payload: payload}
	if err := o.telegramRepo.CreateDeliveryTx(ctx, tx, delivery); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &telegram.IngestionResponse{Success: true, ReplyContext: reply, DomainReferences: refs}, nil
}
