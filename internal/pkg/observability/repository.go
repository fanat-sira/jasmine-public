package observability

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// CreateLLMLog inserts an LLM call audit entry.
func (r *Repository) CreateLLMLog(ctx context.Context, log LLMCallLog) error {
	var errClean *string
	if log.ErrorMessage != nil {
		c := SanitizeString(*log.ErrorMessage)
		errClean = &c
	}

	_, err := r.db.Exec(ctx,
		`INSERT INTO llm_call_logs (
		    correlation_id, user_id, operation, provider, model, latency_ms,
		    success, error_message, retry_count, input_tokens, output_tokens,
		    structured_output_valid, created_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		log.CorrelationID, log.UserID, log.Operation, log.Provider, log.Model, log.LatencyMs,
		log.Success, errClean, log.RetryCount, log.InputTokens, log.OutputTokens,
		log.StructuredOutputValid, log.CreatedAt,
	)
	return err
}
