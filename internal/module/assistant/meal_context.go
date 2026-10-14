package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	"jasmine/internal/pkg/correlation"
	"github.com/jackc/pgx/v5"
)

func isMealContinuation(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	if len([]rune(t)) > 50 || mealTypeFromText(t) != "" {
		return false
	}
	return t == "" || t == "во" || t == "вот" || strings.HasPrefix(t, "это ") || strings.HasPrefix(t, "контейнер") || strings.HasPrefix(t, "там ") || strings.HasPrefix(t, "масла ")
}
func (o *Orchestrator) mealPhotoTimeAndType(ctx context.Context, userID, caption string) (time.Time, string, error) {
	at, err := o.imageLocalTime(ctx, userID, caption)
	if err != nil {
		return time.Time{}, "", err
	}
	mealType := imageMealType(caption, at)
	_, explicit, err := resolveImageTimeAt(at, caption)
	if err != nil {
		return time.Time{}, "", err
	}
	if explicit || !isMealContinuation(caption) || o.db == nil {
		return at, mealType, nil
	}
	var previous time.Time
	var previousType string
	err = o.db.QueryRow(ctx, `SELECT eaten_at,meal_type FROM meal_conversation_context WHERE user_id=$1 AND active_until>NOW()`, userID).Scan(&previous, &previousType)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, mealType, nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	return previous, previousType, nil
}
func (o *Orchestrator) setMealContextTx(ctx context.Context, tx pgx.Tx, userID, groupID, mealType string, at time.Time, updateID int64) error {
	var group any
	if groupID != "" {
		group = groupID
	}
	_, err := tx.Exec(ctx, `INSERT INTO meal_conversation_context(user_id,meal_group_id,eaten_at,meal_type,source_update_id,active_until)
      VALUES($1,$2,$3,$4,$5,NOW()+INTERVAL '24 hours')
      ON CONFLICT(user_id) DO UPDATE SET meal_group_id=EXCLUDED.meal_group_id,logical_turn_id=NULL,eaten_at=EXCLUDED.eaten_at,meal_type=EXCLUDED.meal_type,source_update_id=EXCLUDED.source_update_id,active_until=EXCLUDED.active_until,updated_at=NOW()`, userID, group, at, mealType, updateID)
	return err
}
func mealCorrectionCorrelation(ctx context.Context) string { return correlation.GetCorrelationID(ctx) }
