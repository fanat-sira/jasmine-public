package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jasmine/internal/pkg/correlation"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrUpdateConflict = errors.New("telegram update cannot be claimed")
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// FindOrCreateTelegramUser finds an existing Telegram user link or provisions a new domain user and link.
func (r *Repository) FindOrCreateTelegramUser(ctx context.Context, tgUser *TelegramUser) (*TelegramUser, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Serialize provisioning by immutable Telegram ID, including the first update.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, tgUser.TelegramUserID); err != nil {
		return nil, fmt.Errorf("lock telegram identity: %w", err)
	}
	var existing TelegramUser
	err = tx.QueryRow(ctx,
		`SELECT telegram_user_id, user_id, username, first_name, last_name, language_code, created_at, updated_at
		 FROM telegram_users
		 WHERE telegram_user_id = $1`,
		tgUser.TelegramUserID,
	).Scan(
		&existing.TelegramUserID,
		&existing.UserID,
		&existing.Username,
		&existing.FirstName,
		&existing.LastName,
		&existing.LanguageCode,
		&existing.CreatedAt,
		&existing.UpdatedAt,
	)

	if err == nil {
		// Update profile fields if changed
		if (tgUser.Username != nil && (existing.Username == nil || *existing.Username != *tgUser.Username)) ||
			(tgUser.FirstName != nil && (existing.FirstName == nil || *existing.FirstName != *tgUser.FirstName)) ||
			(tgUser.LastName != nil && (existing.LastName == nil || *existing.LastName != *tgUser.LastName)) ||
			(tgUser.LanguageCode != "" && existing.LanguageCode != tgUser.LanguageCode) {
			if _, err := tx.Exec(ctx,
				`UPDATE telegram_users
				 SET username = COALESCE($2, username),
				     first_name = COALESCE($3, first_name),
				     last_name = COALESCE($4, last_name),
				     language_code = CASE WHEN $5 <> '' THEN $5 ELSE language_code END,
				     updated_at = NOW()
				 WHERE telegram_user_id = $1`,
				tgUser.TelegramUserID, tgUser.Username, tgUser.FirstName, tgUser.LastName, tgUser.LanguageCode,
			); err != nil {
				return nil, fmt.Errorf("update telegram user profile: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit tx: %w", err)
		}
		return &existing, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("query telegram_users: %w", err)
	}

	// 1. Provision a domain identity from the Telegram ID, never its username.
	username := fmt.Sprintf("tg_%d", tgUser.TelegramUserID)
	var userID string
	err = tx.QueryRow(ctx, `INSERT INTO users (username, units, default_rest_sec)
 VALUES ($1, 'metric', 120) ON CONFLICT (username) DO NOTHING RETURNING id`, username).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Preserve unrelated legacy users instead of adopting them on a name match.
		err = tx.QueryRow(ctx, `INSERT INTO users (username,units,default_rest_sec)
   VALUES ($1 || '_' || substr(gen_random_uuid()::text,1,8),'metric',120) RETURNING id`, username).Scan(&userID)
	}
	if err != nil {
		return nil, fmt.Errorf("insert telegram domain user: %w", err)
	}

	lang := tgUser.LanguageCode
	if lang == "" {
		lang = "ru"
	}
	out := &TelegramUser{
		TelegramUserID: tgUser.TelegramUserID,
		UserID:         userID,
		Username:       tgUser.Username,
		FirstName:      tgUser.FirstName,
		LastName:       tgUser.LastName,
		LanguageCode:   lang,
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO telegram_users (telegram_user_id, user_id, username, first_name, last_name, language_code)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING created_at, updated_at`,
		out.TelegramUserID, out.UserID, out.Username, out.FirstName, out.LastName, out.LanguageCode,
	).Scan(&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert telegram_users: %w", err)
	}

	// 3. Provision initial conversation state
	_, err = tx.Exec(ctx,
		`INSERT INTO conversation_states (user_id, chat_id, last_interaction_at, updated_at)
		 VALUES ($1, $2, NOW(), NOW())
		 ON CONFLICT (user_id) DO NOTHING`,
		userID, tgUser.TelegramUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert conversation_states: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return out, nil
}

func (r *Repository) GetTelegramUser(ctx context.Context, telegramUserID int64) (*TelegramUser, error) {
	u := &TelegramUser{}
	err := r.db.QueryRow(ctx,
		`SELECT telegram_user_id, user_id, username, first_name, last_name, language_code, created_at, updated_at
		 FROM telegram_users
		 WHERE telegram_user_id = $1`,
		telegramUserID,
	).Scan(
		&u.TelegramUserID, &u.UserID, &u.Username,
		&u.FirstName, &u.LastName, &u.LanguageCode,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get telegram user: %w", err)
	}
	return u, nil
}

func (r *Repository) GetTelegramUserByUserID(ctx context.Context, userID string) (*TelegramUser, error) {
	u := &TelegramUser{}
	err := r.db.QueryRow(ctx,
		`SELECT telegram_user_id, user_id, username, first_name, last_name, language_code, created_at, updated_at
		 FROM telegram_users
		 WHERE user_id = $1`,
		userID,
	).Scan(
		&u.TelegramUserID, &u.UserID, &u.Username,
		&u.FirstName, &u.LastName, &u.LanguageCode,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get telegram user by user_id: %w", err)
	}
	return u, nil
}

// SaveUpdate records an incoming update if not yet present.
func (r *Repository) SaveUpdate(ctx context.Context, update *TelegramUpdate) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO telegram_updates (update_id, correlation_id, chat_id, telegram_user_id, status, attempts, raw_payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (update_id) DO UPDATE
		     SET correlation_id = COALESCE(telegram_updates.correlation_id, EXCLUDED.correlation_id)`,
		update.UpdateID, update.CorrelationID, update.ChatID, update.TelegramUserID, update.Status, update.Attempts, update.RawPayload,
	)
	if err != nil {
		return fmt.Errorf("save update: %w", err)
	}
	return nil
}

// ClaimUpdate atomically claims an update for processing.
func (r *Repository) ClaimUpdate(ctx context.Context, updateID int64, lockedBy string, maxAttempts int, lockTimeout time.Duration) (*TelegramUpdate, error) {
	timeoutThreshold := time.Now().Add(-lockTimeout)
	u := &TelegramUpdate{}
	err := r.db.QueryRow(ctx,
		`UPDATE telegram_updates
		 SET status = $1,
		     attempts = attempts + 1,
		     locked_at = NOW(),
		     locked_by = $2
		 WHERE update_id = $3
		   AND (
		       status = $4
		       OR (status = $5 AND attempts < $6)
		       OR (status = $1 AND (locked_at IS NULL OR locked_at < $7))
		   )
		 RETURNING update_id, correlation_id, chat_id, telegram_user_id, status, attempts, locked_at, locked_by, raw_payload, response_payload, last_error, completed_at, created_at`,
		UpdateStatusProcessing, lockedBy, updateID,
		UpdateStatusReceived, UpdateStatusFailed, maxAttempts, timeoutThreshold,
	).Scan(
		&u.UpdateID, &u.CorrelationID, &u.ChatID, &u.TelegramUserID, &u.Status, &u.Attempts,
		&u.LockedAt, &u.LockedBy, &u.RawPayload, &u.ResponsePayload,
		&u.LastError, &u.CompletedAt, &u.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUpdateConflict
	}
	if err != nil {
		return nil, fmt.Errorf("claim update: %w", err)
	}
	return u, nil
}

// CompleteUpdate marks an update as completed with the given response payload.
func (r *Repository) CompleteUpdate(ctx context.Context, updateID int64, responsePayload json.RawMessage) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE telegram_updates
		 SET status = $1,
		     response_payload = $2,
		     completed_at = NOW(),
		     locked_by = NULL
		 WHERE update_id = $3`,
		UpdateStatusCompleted, responsePayload, updateID,
	)
	if err != nil {
		return fmt.Errorf("complete update: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// CompleteUpdateTx marks an update as completed inside an existing database transaction.
func (r *Repository) CompleteUpdateTx(ctx context.Context, tx pgx.Tx, updateID int64, responsePayload json.RawMessage) error {
	tag, err := tx.Exec(ctx,
		`UPDATE telegram_updates
		 SET status = $1,
		     response_payload = $2,
		     completed_at = NOW(),
		     locked_by = NULL
		 WHERE update_id = $3`,
		UpdateStatusCompleted, responsePayload, updateID,
	)
	if err != nil {
		return fmt.Errorf("complete update tx: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// FailUpdate marks an update as failed with an error message.
func (r *Repository) FailUpdate(ctx context.Context, updateID int64, errMsg string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE telegram_updates
		 SET status = $1,
		     last_error = $2,
		     locked_by = NULL
		 WHERE update_id = $3`,
		UpdateStatusFailed, errMsg, updateID,
	)
	if err != nil {
		return fmt.Errorf("fail update: %w", err)
	}
	return nil
}

// GetUpdate returns the telegram update by ID.
func (r *Repository) GetUpdate(ctx context.Context, updateID int64) (*TelegramUpdate, error) {
	u := &TelegramUpdate{}
	err := r.db.QueryRow(ctx,
		`SELECT update_id, chat_id, telegram_user_id, status, attempts, locked_at, locked_by, raw_payload, response_payload, last_error, completed_at, created_at
		 FROM telegram_updates
		 WHERE update_id = $1`,
		updateID,
	).Scan(
		&u.UpdateID, &u.ChatID, &u.TelegramUserID, &u.Status, &u.Attempts,
		&u.LockedAt, &u.LockedBy, &u.RawPayload, &u.ResponsePayload,
		&u.LastError, &u.CompletedAt, &u.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get update: %w", err)
	}
	return u, nil
}

// SaveMessage records an inbound or outbound Telegram message.
func (r *Repository) SaveMessage(ctx context.Context, msg *TelegramMessage) error {
	refsJSON, err := json.Marshal(msg.DomainReferences)
	if err != nil {
		return fmt.Errorf("marshal domain references: %w", err)
	}

	_, err = r.db.Exec(ctx,
		`INSERT INTO telegram_messages (
		     chat_id, message_id, correlation_id, telegram_user_id, user_id, direction, reply_to_message_id, text, raw_payload, domain_references, created_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE(NULLIF($11, '0001-01-01 00:00:00+00'::timestamptz), NOW()))
		 ON CONFLICT (chat_id, message_id) DO UPDATE
		     SET text = EXCLUDED.text,
		         raw_payload = EXCLUDED.raw_payload,
		         domain_references = EXCLUDED.domain_references,
		         correlation_id = COALESCE(telegram_messages.correlation_id, EXCLUDED.correlation_id)`,
		msg.ChatID, msg.MessageID, msg.CorrelationID, msg.TelegramUserID, msg.UserID, msg.Direction,
		msg.ReplyToMessageID, msg.Text, msg.RawPayload, refsJSON, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}

// GetMessage retrieves a message by chat_id and message_id.
func (r *Repository) GetMessage(ctx context.Context, chatID, messageID int64) (*TelegramMessage, error) {
	m := &TelegramMessage{}
	var refsJSON []byte
	err := r.db.QueryRow(ctx,
		`SELECT chat_id, message_id, COALESCE(correlation_id,''), telegram_user_id, user_id, direction, reply_to_message_id, text, raw_payload, domain_references, created_at
		 FROM telegram_messages
		 WHERE chat_id = $1 AND message_id = $2`,
		chatID, messageID,
	).Scan(
		&m.ChatID, &m.MessageID, &m.CorrelationID, &m.TelegramUserID, &m.UserID, &m.Direction,
		&m.ReplyToMessageID, &m.Text, &m.RawPayload, &refsJSON, &m.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	if len(refsJSON) > 0 {
		if err := json.Unmarshal(refsJSON, &m.DomainReferences); err != nil {
			return nil, fmt.Errorf("unmarshal domain references: %w", err)
		}
	}
	if m.DomainReferences == nil {
		m.DomainReferences = []DomainReference{}
	}
	return m, nil
}

// GetRecentMessages returns the last N messages in a chat ordered oldest to newest.
func (r *Repository) GetRecentMessages(ctx context.Context, chatID int64, limit int, upToMessageID ...int64) ([]TelegramMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	var cutoff *int64
	if len(upToMessageID) > 0 {
		cutoff = &upToMessageID[0]
	}
	rows, err := r.db.Query(ctx,
		`SELECT chat_id, message_id, COALESCE(correlation_id,''), telegram_user_id, user_id, direction, reply_to_message_id, text, raw_payload, domain_references, created_at
		 FROM telegram_messages
		 WHERE chat_id = $1 AND ($3::bigint IS NULL OR message_id <= $3)
		 ORDER BY created_at DESC, message_id DESC
		 LIMIT $2`,
		chatID, limit, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent messages: %w", err)
	}
	defer rows.Close()

	var descList []TelegramMessage
	for rows.Next() {
		var m TelegramMessage
		var refsJSON []byte
		if err := rows.Scan(
			&m.ChatID, &m.MessageID, &m.CorrelationID, &m.TelegramUserID, &m.UserID, &m.Direction,
			&m.ReplyToMessageID, &m.Text, &m.RawPayload, &refsJSON, &m.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if len(refsJSON) > 0 {
			if err := json.Unmarshal(refsJSON, &m.DomainReferences); err != nil {
				return nil, fmt.Errorf("unmarshal message domain references: %w", err)
			}
		}
		if m.DomainReferences == nil {
			m.DomainReferences = []DomainReference{}
		}
		descList = append(descList, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	// Reverse to chronological order (oldest to newest)
	out := make([]TelegramMessage, len(descList))
	for i, msg := range descList {
		out[len(descList)-1-i] = msg
	}
	return out, nil
}

// ListMessages is an alias for GetRecentMessages.
func (r *Repository) ListMessages(ctx context.Context, chatID int64, limit int) ([]TelegramMessage, error) {
	return r.GetRecentMessages(ctx, chatID, limit)
}

// GetConversationState returns the persistent conversation state for a user.
func (r *Repository) GetConversationState(ctx context.Context, userID string) (*ConversationState, error) {
	s := &ConversationState{}
	var clarJSON []byte
	err := r.db.QueryRow(ctx,
		`SELECT user_id, chat_id, active_session_id, focus_exercise_id, pending_clarification, last_interaction_at, updated_at
		 FROM conversation_states
		 WHERE user_id = $1`,
		userID,
	).Scan(
		&s.UserID, &s.ChatID, &s.ActiveSessionID, &s.FocusExerciseID,
		&clarJSON, &s.LastInteractionAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation state: %w", err)
	}
	if len(clarJSON) > 0 && string(clarJSON) != "null" {
		var clar PendingClarification
		if err := json.Unmarshal(clarJSON, &clar); err == nil {
			s.PendingClarification = &clar
		}
	}
	return s, nil
}

// UpsertConversationState updates or creates the user's conversation state.
func (r *Repository) UpsertConversationState(ctx context.Context, state *ConversationState) error {
	var clarJSON []byte
	var err error
	if state.PendingClarification != nil {
		clarJSON, err = json.Marshal(state.PendingClarification)
		if err != nil {
			return fmt.Errorf("marshal clarification: %w", err)
		}
	}

	_, err = r.db.Exec(ctx,
		`INSERT INTO conversation_states (
		     user_id, chat_id, active_session_id, focus_exercise_id, pending_clarification, last_interaction_at, updated_at
		 ) VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		 ON CONFLICT (user_id) DO UPDATE
		     SET chat_id = EXCLUDED.chat_id,
		         active_session_id = EXCLUDED.active_session_id,
		         focus_exercise_id = EXCLUDED.focus_exercise_id,
		         pending_clarification = EXCLUDED.pending_clarification,
		         last_interaction_at = NOW(),
		         updated_at = NOW()`,
		state.UserID, state.ChatID, state.ActiveSessionID, state.FocusExerciseID, clarJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert conversation state: %w", err)
	}
	return nil
}

// UpsertConversationStateTx updates or creates the user's conversation state inside an existing transaction.
func (r *Repository) UpsertConversationStateTx(ctx context.Context, tx pgx.Tx, state *ConversationState) error {
	var clarJSON []byte
	var err error
	if state.PendingClarification != nil {
		clarJSON, err = json.Marshal(state.PendingClarification)
		if err != nil {
			return fmt.Errorf("marshal clarification: %w", err)
		}
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO conversation_states (
		     user_id, chat_id, active_session_id, focus_exercise_id, pending_clarification, last_interaction_at, updated_at
		 ) VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		 ON CONFLICT (user_id) DO UPDATE
		     SET chat_id = EXCLUDED.chat_id,
		         active_session_id = EXCLUDED.active_session_id,
		         focus_exercise_id = EXCLUDED.focus_exercise_id,
		         pending_clarification = EXCLUDED.pending_clarification,
		         last_interaction_at = NOW(),
		         updated_at = NOW()`,
		state.UserID, state.ChatID, state.ActiveSessionID, state.FocusExerciseID, clarJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert conversation state tx: %w", err)
	}
	return nil
}

// CreateWorkoutSession persists a new workout proposal / session.
func (r *Repository) CreateWorkoutSession(ctx context.Context, s *WorkoutSession) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO workout_sessions (user_id, status, proposal_data, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at, updated_at`,
		s.UserID, s.Status, s.ProposalData, s.ExpiresAt,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create workout session: %w", err)
	}
	return nil
}

// CreateWorkoutSessionTx persists a new workout proposal / session inside an existing transaction.
func (r *Repository) CreateWorkoutSessionTx(ctx context.Context, tx pgx.Tx, s *WorkoutSession) error {
	err := tx.QueryRow(ctx,
		`INSERT INTO workout_sessions (user_id, status, proposal_data, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at, updated_at`,
		s.UserID, s.Status, s.ProposalData, s.ExpiresAt,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create workout session tx: %w", err)
	}
	return nil
}

// SupersedeActiveWorkoutSessionsTx marks existing 'proposed' sessions for this user as 'superseded'.
func (r *Repository) SupersedeActiveWorkoutSessionsTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE workout_sessions
		 SET status = 'superseded',
		     updated_at = NOW()
		 WHERE user_id = $1
		   AND status = 'proposed'`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("supersede active workout sessions tx: %w", err)
	}
	return nil
}

// UpdateWorkoutSessionProposalTx modifies an existing proposal's data inside a transaction.
func (r *Repository) UpdateWorkoutSessionProposalTx(ctx context.Context, tx pgx.Tx, sessionID, userID string, proposalData []byte) error {
	tag, err := tx.Exec(ctx,
		`UPDATE workout_sessions
		 SET proposal_data = $1,
		     updated_at = NOW()
		 WHERE id = $2 AND user_id = $3`,
		proposalData, sessionID, userID,
	)
	if err != nil {
		return fmt.Errorf("update workout session proposal tx: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// GetWorkoutSession retrieves a workout session by ID.
func (r *Repository) GetWorkoutSession(ctx context.Context, sessionID, userID string) (*WorkoutSession, error) {
	s := &WorkoutSession{}
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, status, proposal_data, applied_workout_id, created_at, updated_at, expires_at
		 FROM workout_sessions
		 WHERE id = $1 AND user_id = $2`,
		sessionID, userID,
	).Scan(
		&s.ID, &s.UserID, &s.Status, &s.ProposalData,
		&s.AppliedWorkoutID, &s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workout session: %w", err)
	}
	return s, nil
}

// SaveSessionRevisionTx stores an audit row for proposal modifications.
func (r *Repository) SaveSessionRevisionTx(ctx context.Context, tx pgx.Tx, sessionID string, updateID *int64, previousProposal, newProposal []byte, changeSummary string) error {
	corrID := correlation.GetCorrelationID(ctx)
	_, err := tx.Exec(ctx,
		`INSERT INTO workout_session_revisions (
		     session_id, telegram_update_id, correlation_id, previous_proposal, new_proposal, change_summary
		 ) VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, updateID, corrID, previousProposal, newProposal, changeSummary,
	)
	if err != nil {
		return fmt.Errorf("save session revision tx: %w", err)
	}
	return nil
}

// LockUserTx explicitly locks the owning user row to serialize session operations across concurrent requests.
func (r *Repository) LockUserTx(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&id)
	if err != nil {
		return fmt.Errorf("lock user tx: %w", err)
	}
	return nil
}

// GetActiveWorkoutSession retrieves the most recent active/proposed session for the user that has not expired.
func (r *Repository) GetActiveWorkoutSession(ctx context.Context, userID string) (*WorkoutSession, error) {
	s := &WorkoutSession{}
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, status, proposal_data, applied_workout_id, created_at, updated_at, expires_at
		 FROM workout_sessions
		 WHERE user_id = $1
		   AND status IN ('proposed', 'accepted', 'in_progress')
		   AND expires_at > NOW()
		 ORDER BY created_at DESC
		 LIMIT 1`,
		userID,
	).Scan(
		&s.ID, &s.UserID, &s.Status, &s.ProposalData,
		&s.AppliedWorkoutID, &s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active workout session: %w", err)
	}
	return s, nil
}

// GetActiveWorkoutSessionForUpdate locks and retrieves the most recent active session for the user within a transaction.
func (r *Repository) GetActiveWorkoutSessionForUpdate(ctx context.Context, tx pgx.Tx, userID string) (*WorkoutSession, error) {
	s := &WorkoutSession{}
	err := tx.QueryRow(ctx,
		`SELECT id, user_id, status, proposal_data, applied_workout_id, created_at, updated_at, expires_at
		 FROM workout_sessions
		 WHERE user_id = $1
		   AND status IN ('proposed', 'accepted', 'in_progress')
		   AND expires_at > NOW()
		 ORDER BY created_at DESC
		 LIMIT 1
		 FOR UPDATE`,
		userID,
	).Scan(
		&s.ID, &s.UserID, &s.Status, &s.ProposalData,
		&s.AppliedWorkoutID, &s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active workout session for update: %w", err)
	}
	return s, nil
}

// UpdateWorkoutSessionStatus updates the status and optionally links the applied workout ID.
func (r *Repository) UpdateWorkoutSessionStatus(ctx context.Context, sessionID, userID, status string, appliedWorkoutID *string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE workout_sessions
		 SET status = $1,
		     applied_workout_id = COALESCE($2, applied_workout_id),
		     updated_at = NOW()
		 WHERE id = $3 AND user_id = $4`,
		status, appliedWorkoutID, sessionID, userID,
	)
	if err != nil {
		return fmt.Errorf("update workout session status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// UpdateWorkoutSessionStatusTx updates the status inside an existing transaction.
func (r *Repository) UpdateWorkoutSessionStatusTx(ctx context.Context, tx pgx.Tx, sessionID, userID, status string, appliedWorkoutID *string) error {
	tag, err := tx.Exec(ctx,
		`UPDATE workout_sessions
		 SET status = $1,
		     applied_workout_id = COALESCE($2, applied_workout_id),
		     updated_at = NOW()
		 WHERE id = $3 AND user_id = $4`,
		status, appliedWorkoutID, sessionID, userID,
	)
	if err != nil {
		return fmt.Errorf("update workout session status tx: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// SaveMemory stores a scoped memory item for the user.
func (r *Repository) SaveMemory(ctx context.Context, m *UserMemory) error {
	conf := m.Confidence
	if conf <= 0 {
		conf = 1.0
	}
	scope := m.Scope
	if scope == "" {
		scope = MemoryScopePermanent
	}
	corrID := m.CorrelationID
	if corrID == "" {
		corrID = correlation.GetCorrelationID(ctx)
	}

	err := r.db.QueryRow(ctx,
		`INSERT INTO user_memories (
		     correlation_id, user_id, category, scope, key, value, confidence, valid_until, superseded_by,
		     source_update_id, source_message_id, source_text
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at, updated_at`,
		corrID, m.UserID, m.Category, scope, m.Key, m.Value, conf, m.ValidUntil, m.SupersededBy,
		m.SourceUpdateID, m.SourceMessageID, m.SourceText,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	return nil
}

// SaveMemoryTx stores a scoped memory item inside an active transaction.
func (r *Repository) SaveMemoryTx(ctx context.Context, tx pgx.Tx, m *UserMemory) error {
	conf := m.Confidence
	if conf <= 0 {
		conf = 1.0
	}
	scope := m.Scope
	if scope == "" {
		scope = MemoryScopePermanent
	}
	corrID := m.CorrelationID
	if corrID == "" {
		corrID = correlation.GetCorrelationID(ctx)
	}

	err := tx.QueryRow(ctx,
		`INSERT INTO user_memories (
		     correlation_id, user_id, category, scope, key, value, confidence, valid_until, superseded_by,
		     source_update_id, source_message_id, source_text
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at, updated_at`,
		corrID, m.UserID, m.Category, scope, m.Key, m.Value, conf, m.ValidUntil, m.SupersededBy,
		m.SourceUpdateID, m.SourceMessageID, m.SourceText,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save memory tx: %w", err)
	}
	return nil
}

// SupersedeMemoryTx marks existing active memories with matching key as superseded by the new memory ID.
func (r *Repository) SupersedeMemoryTx(ctx context.Context, tx pgx.Tx, userID, category, key, newMemoryID string) error {
	if newMemoryID == "" {
		var err error
		if tx != nil {
			_, err = tx.Exec(ctx,
				`UPDATE user_memories
				 SET valid_until = NOW(),
				     updated_at = NOW()
				 WHERE user_id = $1
				   AND category = $2
				   AND key = $3
				   AND superseded_by IS NULL`,
				userID, category, key,
			)
		} else {
			_, err = r.db.Exec(ctx,
				`UPDATE user_memories
				 SET valid_until = NOW(),
				     updated_at = NOW()
				 WHERE user_id = $1
				   AND category = $2
				   AND key = $3
				   AND superseded_by IS NULL`,
				userID, category, key,
			)
		}
		if err != nil {
			return fmt.Errorf("supersede memory tx: %w", err)
		}
		return nil
	}

	var err error
	if tx != nil {
		_, err = tx.Exec(ctx,
			`UPDATE user_memories
			 SET superseded_by = $1,
			     updated_at = NOW()
			 WHERE user_id = $2
			   AND category = $3
			   AND key = $4
			   AND superseded_by IS NULL
			   AND id != $1::uuid`,
			newMemoryID, userID, category, key,
		)
	} else {
		_, err = r.db.Exec(ctx,
			`UPDATE user_memories
			 SET superseded_by = $1,
			     updated_at = NOW()
			 WHERE user_id = $2
			   AND category = $3
			   AND key = $4
			   AND superseded_by IS NULL
			   AND id != $1::uuid`,
			newMemoryID, userID, category, key,
		)
	}
	if err != nil {
		return fmt.Errorf("supersede memory tx: %w", err)
	}
	return nil
}

// DeactivateMemoryTx deactivates active memories matching a key/category pattern (for reversals).
func (r *Repository) DeactivateMemoryTx(ctx context.Context, tx pgx.Tx, userID, category, key string) error {
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx,
			`UPDATE user_memories
			 SET valid_until = NOW(),
			     updated_at = NOW()
			 WHERE user_id = $1
			   AND category = $2
			   AND key = $3
			   AND (valid_until IS NULL OR valid_until > NOW())
			   AND superseded_by IS NULL`,
			userID, category, key,
		)
	} else {
		_, err = r.db.Exec(ctx,
			`UPDATE user_memories
		 SET valid_until = NOW(),
		     updated_at = NOW()
		 WHERE user_id = $1
		   AND category = $2
		   AND key = $3
		   AND (valid_until IS NULL OR valid_until > NOW())
		   AND superseded_by IS NULL`,
			userID, category, key,
		)
	}
	if err != nil {
		return fmt.Errorf("deactivate memory tx: %w", err)
	}
	return nil
}

// ListMemories returns active memories for a user, optionally filtered by category and scope.
func (r *Repository) ListMemories(ctx context.Context, userID string, category, scope *string) ([]UserMemory, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, user_id, category, scope, key, value, confidence, valid_until, superseded_by,
		        source_update_id, source_message_id, source_text, created_at, updated_at
		 FROM user_memories
		 WHERE user_id = $1
		   AND ($2::text IS NULL OR category = $2)
		   AND ($3::text IS NULL OR scope = $3)
		   AND (valid_until IS NULL OR valid_until > NOW())
		   AND superseded_by IS NULL
		 ORDER BY created_at DESC`,
		userID, category, scope,
	)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	var out []UserMemory
	for rows.Next() {
		var m UserMemory
		if err := rows.Scan(
			&m.ID, &m.UserID, &m.Category, &m.Scope, &m.Key, &m.Value,
			&m.Confidence, &m.ValidUntil, &m.SupersededBy,
			&m.SourceUpdateID, &m.SourceMessageID, &m.SourceText,
			&m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, m)
	}
	if out == nil {
		out = []UserMemory{}
	}
	return out, rows.Err()
}

// SaveFeedbackEvent logs a feedback event as permanent evidence.
func (r *Repository) SaveFeedbackEvent(ctx context.Context, fb *UserFeedbackEvent) error {
	corrID := fb.CorrelationID
	if corrID == "" {
		corrID = correlation.GetCorrelationID(ctx)
	}
	err := r.db.QueryRow(ctx,
		`INSERT INTO user_feedback_events (
		     correlation_id, user_id, workout_id, exercise_id, feedback_type, raw_text, metadata
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		corrID, fb.UserID, fb.WorkoutID, fb.ExerciseID, fb.FeedbackType, fb.RawText, fb.Metadata,
	).Scan(&fb.ID, &fb.CreatedAt)
	if err != nil {
		return fmt.Errorf("save feedback event: %w", err)
	}
	return nil
}

// ListFeedbackEvents returns recent feedback events for a user, optionally filtered by exercise ID.
func (r *Repository) ListFeedbackEvents(ctx context.Context, userID string, exerciseID *string, limit int) ([]UserFeedbackEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.Query(ctx,
		`SELECT id, user_id, workout_id, exercise_id, feedback_type, raw_text, metadata, created_at
		 FROM user_feedback_events
		 WHERE user_id = $1
		   AND ($2::uuid IS NULL OR exercise_id = $2)
		 ORDER BY created_at DESC
		 LIMIT $3`,
		userID, exerciseID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list feedback events: %w", err)
	}
	defer rows.Close()

	var out []UserFeedbackEvent
	for rows.Next() {
		var fb UserFeedbackEvent
		if err := rows.Scan(
			&fb.ID, &fb.UserID, &fb.WorkoutID, &fb.ExerciseID,
			&fb.FeedbackType, &fb.RawText, &fb.Metadata, &fb.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan feedback event: %w", err)
		}
		out = append(out, fb)
	}
	if out == nil {
		out = []UserFeedbackEvent{}
	}
	return out, rows.Err()
}

// --- Durable Telegram Deliveries (Outbox) ---

// CreateDelivery registers a pending outbound message delivery.
func (r *Repository) CreateDelivery(ctx context.Context, d *TelegramDelivery) error {
	status := d.Status
	if status == "" {
		status = DeliveryStatusPending
	}
	err := r.db.QueryRow(ctx,
		`INSERT INTO telegram_deliveries (
		     correlation_id, update_id, chat_id, user_id, status, payload, attempts
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		d.CorrelationID, d.UpdateID, d.ChatID, d.UserID, status, d.Payload, d.Attempts,
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create delivery: %w", err)
	}
	return nil
}

// CreateDeliveryTx registers a pending outbound message delivery within an existing transaction.
func (r *Repository) CreateDeliveryTx(ctx context.Context, tx pgx.Tx, d *TelegramDelivery) error {
	status := d.Status
	if status == "" {
		status = DeliveryStatusPending
	}
	err := tx.QueryRow(ctx,
		`INSERT INTO telegram_deliveries (
		     correlation_id, update_id, chat_id, user_id, status, payload, attempts
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		d.CorrelationID, d.UpdateID, d.ChatID, d.UserID, status, d.Payload, d.Attempts,
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create delivery tx: %w", err)
	}
	return nil
}

// GetDelivery retrieves a delivery by ID.
func (r *Repository) GetDelivery(ctx context.Context, deliveryID string) (*TelegramDelivery, error) {
	d := &TelegramDelivery{}
	err := r.db.QueryRow(ctx,
		`SELECT id, correlation_id, update_id, chat_id, user_id, status, payload, attempts, locked_at, locked_by, last_error, telegram_message_id, sent_at, created_at, updated_at
		 FROM telegram_deliveries
		 WHERE id = $1`,
		deliveryID,
	).Scan(
		&d.ID, &d.CorrelationID, &d.UpdateID, &d.ChatID, &d.UserID, &d.Status,
		&d.Payload, &d.Attempts, &d.LockedAt, &d.LockedBy,
		&d.LastError, &d.TelegramMessageID, &d.SentAt,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery: %w", err)
	}
	return d, nil
}

// ClaimPendingDelivery atomically locks a delivery for sending.
func (r *Repository) ClaimPendingDelivery(ctx context.Context, deliveryID string, lockedBy string, maxAttempts int, lockTimeout time.Duration) (*TelegramDelivery, error) {
	timeoutThreshold := time.Now().Add(-lockTimeout)
	d := &TelegramDelivery{}
	err := r.db.QueryRow(ctx,
		`UPDATE telegram_deliveries
		 SET status = $1,
		     attempts = attempts + 1,
		     locked_at = NOW(),
		     locked_by = $2,
		     updated_at = NOW()
		 WHERE id = $3
		   AND (
		       status = $4
		       OR (status = $5 AND attempts < $6)
		       OR (status = $1 AND (locked_at IS NULL OR locked_at < $7))
		   )
		 RETURNING id, correlation_id, update_id, chat_id, user_id, status, payload, attempts, locked_at, locked_by, last_error, telegram_message_id, sent_at, created_at, updated_at`,
		DeliveryStatusSending, lockedBy, deliveryID,
		DeliveryStatusPending, DeliveryStatusFailed, maxAttempts, timeoutThreshold,
	).Scan(
		&d.ID, &d.CorrelationID, &d.UpdateID, &d.ChatID, &d.UserID, &d.Status,
		&d.Payload, &d.Attempts, &d.LockedAt, &d.LockedBy,
		&d.LastError, &d.TelegramMessageID, &d.SentAt,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUpdateConflict
	}
	if err != nil {
		return nil, fmt.Errorf("claim delivery: %w", err)
	}
	return d, nil
}

// ListPendingDeliveries retrieves pending deliveries ready for sending.
func (r *Repository) ListPendingDeliveries(ctx context.Context, limit, maxAttempts int, lockTimeout time.Duration) ([]TelegramDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	timeoutThreshold := time.Now().Add(-lockTimeout)
	rows, err := r.db.Query(ctx,
		`SELECT id, correlation_id, update_id, chat_id, user_id, status, payload, attempts, locked_at, locked_by, last_error, telegram_message_id, sent_at, created_at, updated_at
		 FROM telegram_deliveries
		 WHERE status = $1
		    OR (status = $2 AND (locked_at IS NULL OR locked_at < $3))
		    OR (status = $4 AND attempts < $5 AND updated_at < NOW() - INTERVAL '15 seconds')
		 ORDER BY created_at ASC
		 LIMIT $6`,
		DeliveryStatusPending, DeliveryStatusSending, timeoutThreshold, DeliveryStatusFailed, maxAttempts, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending deliveries: %w", err)
	}
	defer rows.Close()

	var out []TelegramDelivery
	for rows.Next() {
		var d TelegramDelivery
		if err := rows.Scan(
			&d.ID, &d.CorrelationID, &d.UpdateID, &d.ChatID, &d.UserID, &d.Status,
			&d.Payload, &d.Attempts, &d.LockedAt, &d.LockedBy,
			&d.LastError, &d.TelegramMessageID, &d.SentAt,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		out = append(out, d)
	}
	if out == nil {
		out = []TelegramDelivery{}
	}
	return out, rows.Err()
}

// MarkDeliverySent records successful dispatch to Telegram and sets the real Telegram message ID.
func (r *Repository) MarkDeliverySent(ctx context.Context, deliveryID string, telegramMessageID int64) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE telegram_deliveries
		 SET status = $1,
		     telegram_message_id = $2,
		     sent_at = NOW(),
		     locked_by = NULL,
		     updated_at = NOW()
		 WHERE id = $3`,
		DeliveryStatusSent, telegramMessageID, deliveryID,
	)
	if err != nil {
		return fmt.Errorf("mark delivery sent: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// MarkDeliveryFailed records a failed delivery attempt.
func (r *Repository) MarkDeliveryFailed(ctx context.Context, deliveryID string, errMsg string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE telegram_deliveries
		 SET status = $1,
		     last_error = $2,
		     locked_by = NULL,
		     updated_at = NOW()
		 WHERE id = $3`,
		DeliveryStatusFailed, errMsg, deliveryID,
	)
	if err != nil {
		return fmt.Errorf("mark delivery failed: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// ClaimMediaGroup allows one conversational response for an album and no image actions.
func (r *Repository) ClaimMediaGroup(ctx context.Context, userID, groupID string, updateID int64) (bool, error) {
	tag, err := r.db.Exec(ctx, `INSERT INTO telegram_media_groups(user_id,media_group_id,first_update_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, userID, groupID, updateID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
