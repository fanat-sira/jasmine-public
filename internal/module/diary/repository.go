package diary

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("meal entry not found")

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const diaryTimezone = `(SELECT COALESCE(NULLIF(NULLIF(u.timezone,'UTC'),''),(SELECT NULLIF(tu.timezone,'') FROM telegram_users tu WHERE tu.user_id=u.id LIMIT 1),'UTC') FROM users u WHERE u.id=$1)`

const entryCols = `me.id, me.user_id, COALESCE(me.product_id::text,'') AS product_id, COALESCE(NULLIF(me.observed_name,''),p.name,'') AS product_name, me.meal_type, me.weight_g, me.calories, me.proteins, me.fats, me.carbs, me.eaten_at, me.created_at`

func scanEntry(row pgx.Row) (*MealEntry, error) {
	e := &MealEntry{}
	err := row.Scan(
		&e.ID, &e.UserID, &e.ProductID, &e.ProductName, &e.MealType,
		&e.WeightG, &e.Calories, &e.Proteins, &e.Fats, &e.Carbs,
		&e.EatenAt, &e.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (r *Repository) Add(ctx context.Context, e *MealEntry) error {
	return r.db.QueryRow(ctx,
		`INSERT INTO meal_entries
		     (user_id, product_id, meal_type, weight_g, calories, proteins, fats, carbs, eaten_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		e.UserID, e.ProductID, e.MealType, e.WeightG,
		e.Calories, e.Proteins, e.Fats, e.Carbs, e.EatenAt,
	).Scan(&e.ID, &e.CreatedAt)
}

func (r *Repository) ListByDate(ctx context.Context, userID, date string) ([]MealEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+entryCols+`
		 FROM meal_entries me
		 LEFT JOIN products p ON p.id = me.product_id
		 WHERE me.user_id = $1 AND me.deleted_at IS NULL
		   AND DATE(me.eaten_at AT TIME ZONE `+diaryTimezone+`) = $2::date
		 ORDER BY me.eaten_at`,
		userID, date,
	)
	if err != nil {
		return nil, fmt.Errorf("diary list: %w", err)
	}
	defer rows.Close()

	var out []MealEntry
	for rows.Next() {
		e := MealEntry{}
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.ProductID, &e.ProductName, &e.MealType,
			&e.WeightG, &e.Calories, &e.Proteins, &e.Fats, &e.Carbs,
			&e.EatenAt, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("diary list scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) SumByDate(ctx context.Context, userID, date string) (Macros, error) {
	var m Macros
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(calories),0), COALESCE(SUM(proteins),0),
		        COALESCE(SUM(fats),0),     COALESCE(SUM(carbs),0)
		 FROM meal_entries
		 WHERE user_id = $1 AND deleted_at IS NULL
		   AND DATE(eaten_at AT TIME ZONE `+diaryTimezone+`) = $2::date`,
		userID, date,
	).Scan(&m.Calories, &m.Proteins, &m.Fats, &m.Carbs)
	return m, err
}

func (r *Repository) Delete(ctx context.Context, id, userID string) error {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM meal_entries WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("diary delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
