package observability

import (
	"context"

	"jasmine/internal/clients/openrouter"
)

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }
func (s *Service) RecordLLMCall(ctx context.Context, record openrouter.LLMCallRecord) {
	if s.repo == nil {
		return
	}
	_ = s.repo.CreateLLMLog(ctx, LLMCallLog{
		CorrelationID:         record.CorrelationID,
		UserID:                record.UserID,
		Operation:             record.Operation,
		Provider:              record.Provider,
		Model:                 record.Model,
		LatencyMs:             record.LatencyMs,
		Success:               record.Success,
		ErrorMessage:          record.ErrorMessage,
		RetryCount:            record.RetryCount,
		InputTokens:           record.InputTokens,
		OutputTokens:          record.OutputTokens,
		StructuredOutputValid: record.StructuredOutputValid,
		CreatedAt:             record.CreatedAt,
	})
}
