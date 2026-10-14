package assistant

import (
	"context"
	"strings"
	"time"

	"jasmine/internal/module/telegram"
)

func isMealStatusQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	food := strings.Contains(t, "пищ") || strings.Contains(t, "дневник") || strings.Contains(t, "завтрак") || strings.Contains(t, "обед") || strings.Contains(t, "ужин") || strings.Contains(t, "еду")
	return food && (strings.Contains(t, "пошло") || strings.Contains(t, "запис") || strings.Contains(t, "сохранил"))
}

func (a *CoachAgent) answerMealStatus(ctx context.Context, c *CoachContext, userID string, chatID, updateID, messageID int64) (*telegram.IngestionResponse, error) {
	state := map[string]any{"photo_found": c.LatestImageFileID != ""}
	if c.LatestImageFileID != "" {
		day := time.Now().In(ResolveTimezone(c.Timezone, "UTC")).Format("2006-01-02")
		state["date"] = day
		var saved bool
		err := a.owner.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM meal_entries WHERE user_id=$1 AND source_file_unique_id=$2 AND DATE(eaten_at AT TIME ZONE $3::text)=$4::date)`, userID, c.LatestImageFileID, c.Timezone, day).Scan(&saved)
		if err != nil {
			return nil, err
		}
		state["saved"] = saved
	}
	tx, err := a.owner.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	refs := []telegram.DomainReference{}
	if c.LatestLabelDraft != nil && c.LatestImageFileID != "" {
		refs = append(refs, telegram.DomainReference{Type: "image_workflow", Kind: string(ImageNutritionLabel), Extra: map[string]any{"file_unique_id": c.LatestImageFileID, "draft": c.LatestLabelDraft, "caption": c.LatestLabelCaption, "source_message_id": c.LatestLabelMessageID}})
	}
	res, err := a.owner.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", refs, &telegram.ReplyContext{EventType: "meal_status", CurrentState: state, ToolResults: refs})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

func isMealAffirmation(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "да", "ну да", "ага", "угу", "давай", "ок", "окей", "конечно":
		return true
	default:
		return false
	}
}

func (a *CoachAgent) tryLabelMealFollowup(ctx context.Context, c *CoachContext, userID string, chatID, updateID, messageID int64, text string) (*telegram.IngestionResponse, bool, error) {
	affirmed := c.LatestLabelReply && isMealAffirmation(text)
	weightReply := c.LatestLabelReply && labelCaptionGrams.MatchString(text)
	if !wantsLogLabelMeal(text) && !affirmed && !weightReply {
		return nil, false, nil
	}
	if c.LatestLabelDraft == nil || c.LatestImageFileID == "" || c.LatestLabelMessageID == 0 {
		if affirmed {
			res, err := a.sendPlain(ctx, userID, chatID, updateID, messageID, &telegram.ReplyContext{EventType: "meal_photo_missing", Clarification: []string{"photo_needed_to_log_meal"}})
			return res, true, err
		}
		return nil, false, nil
	}
	day := time.Now().In(ResolveTimezone(c.Timezone, "UTC")).Format("2006-01-02")
	var alreadySaved bool
	err := a.owner.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM meal_entries WHERE user_id=$1 AND source_file_unique_id=$2 AND DATE(eaten_at AT TIME ZONE $3::text)=$4::date)`, userID, c.LatestImageFileID, c.Timezone, day).Scan(&alreadySaved)
	if err != nil {
		return nil, true, err
	}
	if alreadySaved {
		res, err := a.sendPlain(ctx, userID, chatID, updateID, messageID, &telegram.ReplyContext{EventType: "meal_already_saved", CurrentState: map[string]any{"date": day}})
		return res, true, err
	}
	caption := text
	if weightReply {
		caption = c.LatestLabelCaption + " " + text
	}
	if affirmed {
		caption = c.LatestLabelCaption
	}
	refs := []telegram.DomainReference{{Type: "image_workflow", Kind: string(ImageNutritionLabel), Extra: map[string]any{"file_unique_id": c.LatestImageFileID, "draft": c.LatestLabelDraft, "caption": c.LatestLabelCaption, "source_message_id": c.LatestLabelMessageID}}}
	res, err := a.owner.saveLabelMeal(ctx, userID, chatID, updateID, messageID, c.LatestLabelMessageID, caption, telegram.ImageInput{FileUniqueID: c.LatestImageFileID}, c.LatestLabelDraft, refs)
	return res, true, err
}

func (a *CoachAgent) sendPlain(ctx context.Context, userID string, chatID, updateID, messageID int64, facts *telegram.ReplyContext) (*telegram.IngestionResponse, error) {
	tx, err := a.owner.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	res, err := a.owner.deliverStandAloneResponseTx(ctx, tx, userID, chatID, updateID, messageID, "", nil, facts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}
