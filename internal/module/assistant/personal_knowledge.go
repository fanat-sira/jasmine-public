package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/prompts"
	"jasmine/internal/pkg/correlation"
)

type PersonalFact struct {
	ID               string          `json:"id,omitempty"`
	Namespace        string          `json:"namespace"`
	Subject          string          `json:"subject"`
	Predicate        string          `json:"predicate"`
	Value            json.RawMessage `json:"value_json"`
	Confidence       float64         `json:"confidence"`
	SourceType       string          `json:"source_type"`
	ValidFrom        time.Time       `json:"valid_from"`
	SourceID         string          `json:"source_id,omitempty"`
	SourceDocumentID *string         `json:"source_document_id,omitempty"`
	SourceFilename   string          `json:"source_filename,omitempty"`
	SourceMessageID  *int64          `json:"source_message_id,omitempty"`
	SourceExcerpt    string          `json:"source_excerpt,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}
type FactProposal struct {
	Namespace  string  `json:"namespace"`
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Value      any     `json:"value"`
	Operation  string  `json:"operation"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"`
}
type PersonalKnowledge struct {
	db      *pgxpool.Pool
	client  *openrouter.Client
	model   string
	prompts prompts.Store
}

func NewPersonalKnowledge(db *pgxpool.Pool, client *openrouter.Client, model string) *PersonalKnowledge {
	return &PersonalKnowledge{db: db, client: client, model: model}
}
func (k *PersonalKnowledge) SetPromptStore(store prompts.Store) { k.prompts = store }

var factSchemas = map[string]bool{
	"identity": true, "location": true, "device": true, "fitness": true, "nutrition": true,
	"assistant_preferences": true, "lifestyle": true, "project": true, "work": true,
	"education": true, "technology": true, "preference": true, "routine": true, "other": true,
}
var factPredicateRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var factSubjectRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
var locationRE = regexp.MustCompile(`(?i)(?:я (?:теперь )?живу в|переехал(?:а)? в)\s+([\p{L}-]+)`)
var forgetLocationRE = regexp.MustCompile(`(?i)забудь(?:,?\s+что)?\s+я\s+живу\s+в\s+[\p{L}-]+`)
var laptopRE = regexp.MustCompile(`(?i)(?:на (?:моем |моём )?ноут(?:е|буке)|у меня на ноут(?:е|буке))(?:\s+стоит|\s+установлен)?\s+(arch(?: linux)?|windows|ubuntu|linux|macos)`)
var workPCRE = regexp.MustCompile(`(?i)на (?:моем |моём )?рабочем (?:компе|пк|компьютере)(?:\s+стоит)?\s+(arch(?: linux)?|windows|ubuntu|linux|macos)`)
var nameRE = regexp.MustCompile(`(?i)(?:меня зовут|мое имя|моё имя)\s+([\p{L}-]+)`)
var ageRE = regexp.MustCompile(`(?i)мне (?:теперь )?(\d{1,3})(?:\s+год|\s+лет|\s+года)?`)

func explicitFactProposals(text string) []FactProposal {
	lower := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "ё", "е")))
	if (strings.Contains(lower, "не ставь точк") || strings.Contains(lower, "не ставить точк")) && (strings.Contains(lower, "последн") || strings.Contains(lower, "в конце")) {
		return []FactProposal{{Namespace: "assistant_preferences", Subject: "self", Predicate: "final_sentence_period", Value: false, Operation: "replace", Confidence: 1, Evidence: text}}
	}
	if lower == "" || strings.Contains(lower, "?") || strings.HasPrefix(lower, "/profile") {
		return nil
	}
	if forgetLocationRE.MatchString(text) {
		return []FactProposal{{Namespace: "location", Subject: "self", Predicate: "home_city", Operation: "forget", Confidence: 1, Evidence: text}}
	}
	out := []FactProposal{}
	appendFact := func(ns, subject, key string, value any) {
		out = append(out, FactProposal{Namespace: ns, Subject: subject, Predicate: key, Value: value, Operation: "replace", Confidence: 1, Evidence: text})
	}
	if m := locationRE.FindStringSubmatch(text); len(m) > 1 {
		appendFact("location", "self", "home_city", canonicalCity(m[1]))
	}
	if m := laptopRE.FindStringSubmatch(text); len(m) > 1 {
		appendFact("device", "laptop", "operating_system", canonicalOS(m[1]))
	}
	if m := workPCRE.FindStringSubmatch(text); len(m) > 1 {
		appendFact("device", "work_pc", "operating_system", canonicalOS(m[1]))
	}
	if m := nameRE.FindStringSubmatch(text); len(m) > 1 {
		appendFact("identity", "self", "name", m[1])
	}
	if m := ageRE.FindStringSubmatch(text); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= 120 {
			appendFact("identity", "self", "age", n)
		}
	}
	if strings.Contains(lower, "не люблю бег") {
		appendFact("fitness", "self", "running_preference", false)
	}
	return out
}
func canonicalOS(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "arch", "arch linux":
		return "Arch Linux"
	case "windows":
		return "Windows"
	case "ubuntu":
		return "Ubuntu"
	case "macos":
		return "macOS"
	default:
		return "Linux"
	}
}
func canonicalCity(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "сочи":
		return "Сочи"
	case "москву", "москве", "москва":
		return "Москва"
	}
	if v == "" {
		return ""
	}
	runes := []rune(v)
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}
func normalizeProposal(p FactProposal) (FactProposal, bool) {
	p.Namespace = strings.ToLower(strings.TrimSpace(p.Namespace))
	p.Subject = strings.ToLower(strings.TrimSpace(p.Subject))
	p.Predicate = strings.ToLower(strings.TrimSpace(p.Predicate))
	if p.Subject == "" {
		p.Subject = "self"
	}
	if !factSchemas[p.Namespace] || !factPredicateRE.MatchString(p.Predicate) || !factSubjectRE.MatchString(p.Subject) || p.Confidence < 0.85 {
		return p, false
	}
	switch p.Operation {
	case "replace", "merge", "append", "forget":
	case "transient", "domain_event", "ignore":
		return p, false
	default:
		return p, false
	}
	if p.Namespace == "device" && p.Predicate == "operating_system" {
		v, ok := p.Value.(string)
		if !ok {
			return p, false
		}
		p.Value = canonicalOS(v)
	}
	if p.Namespace == "location" && p.Predicate == "home_city" && p.Operation != "forget" {
		v, ok := p.Value.(string)
		if !ok {
			return p, false
		}
		p.Value = canonicalCity(v)
		if p.Value == "" {
			return p, false
		}
	}
	if p.Namespace == "identity" && p.Predicate == "age" && p.Operation != "forget" {
		v, ok := p.Value.(float64)
		if ok {
			p.Value = int(v)
		}
		age, ok := p.Value.(int)
		if !ok || age < 1 || age > 120 {
			return p, false
		}
	}
	encoded, err := json.Marshal(p.Value)
	if err != nil || len(encoded) > 1024 {
		return p, false
	}
	return p, true
}
func likelyPersonalStatement(text string) bool {
	lower := strings.ToLower(text)
	for _, part := range []string{"я ", "мой ", "моя ", "мне ", "у меня", "живу", "переехал", "ноут", "рабочем комп", "забудь", "не ставь точки", "можешь не ставить точки", "обращайся ко мне"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}
func (k *PersonalKnowledge) Extract(ctx context.Context, text string) ([]FactProposal, error) {
	explicit := explicitFactProposals(text)
	if k.client == nil || strings.TrimSpace(text) == "" || IsHypotheticalOrQuestion(text) || !likelyPersonalStatement(text) {
		return explicit, nil
	}
	prompt, err := prompts.Resolve(ctx, k.prompts, prompts.KeyPersonalFactExtraction)
	if err != nil {
		if len(explicit) > 0 {
			return explicit, nil
		}
		return nil, err
	}
	aiCtx, cancel := context.WithTimeout(correlation.WithOperation(ctx, "personal_fact_extraction"), 20*time.Second)
	defer cancel()
	raw, err := k.client.SendConversation(aiCtx, k.model, prompt, nil, text)
	if err != nil {
		if len(explicit) > 0 {
			return explicit, nil
		}
		return nil, err
	}
	var decoded struct {
		Facts []FactProposal `json:"facts"`
	}
	if err := json.Unmarshal([]byte(stripMarkdownFences(raw)), &decoded); err != nil {
		if len(explicit) > 0 {
			return explicit, nil
		}
		return nil, err
	}
	valid := make([]FactProposal, 0, len(explicit)+len(decoded.Facts))
	seen := make(map[string]struct{}, len(explicit)+len(decoded.Facts))
	for _, p := range explicit {
		if normalized, ok := normalizeProposal(p); ok {
			key := normalized.Namespace + "/" + normalized.Subject + "/" + normalized.Predicate
			seen[key] = struct{}{}
			valid = append(valid, normalized)
		}
	}
	for _, p := range decoded.Facts {
		if p.Evidence == "" || !strings.Contains(strings.ToLower(text), strings.ToLower(p.Evidence)) {
			continue
		}
		if normalized, ok := normalizeProposal(p); ok {
			key := normalized.Namespace + "/" + normalized.Subject + "/" + normalized.Predicate
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			valid = append(valid, normalized)
		}
	}
	return valid, nil
}
func (k *PersonalKnowledge) Observe(ctx context.Context, userID string, updateID, messageID int64, text string) ([]string, error) {
	proposals, err := k.Extract(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(proposals) == 0 {
		return nil, nil
	}
	tx, err := k.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	ops := []string{}
	for _, p := range proposals {
		op, err := k.applyTx(ctx, tx, userID, &updateID, &messageID, "explicit_user", p)
		if err != nil {
			return nil, err
		}
		if op != "" {
			ops = append(ops, op)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ops, nil
}
func sourceRank(source string) int {
	switch source {
	case "explicit_user":
		return 3
	case "profile_import", "profile_document", "profile_json":
		return 2
	default:
		return 1
	}
}
func (k *PersonalKnowledge) applyTx(ctx context.Context, tx pgx.Tx, userID string, updateID, messageID *int64, sourceType string, p FactProposal, sourceDocumentID ...*string) (string, error) {
	p, ok := normalizeProposal(p)
	if !ok {
		return "", nil
	}
	identity := userID + "/" + p.Namespace + "/" + p.Subject + "/" + p.Predicate
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, identity); err != nil {
		return "", err
	}
	var oldID, oldSource string
	var oldValue json.RawMessage
	err := tx.QueryRow(ctx, `SELECT id,value,source_type FROM user_memories WHERE user_id=$1 AND category='personal_fact' AND namespace=$2 AND subject=$3 AND predicate=$4 AND superseded_by IS NULL AND valid_until IS NULL FOR UPDATE`, userID, p.Namespace, p.Subject, p.Predicate).Scan(&oldID, &oldValue, &oldSource)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if sourceType != "explicit_user" {
		if oldID != "" && sourceRank(oldSource) > sourceRank(sourceType) {
			return "", nil
		}
		var latestOperation string
		err := tx.QueryRow(ctx, `SELECT operation FROM personal_fact_events WHERE user_id=$1 AND namespace=$2 AND subject=$3 AND predicate=$4 AND source_update_id IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1`, userID, p.Namespace, p.Subject, p.Predicate).Scan(&latestOperation)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if latestOperation == "forget" {
			return "", nil
		}
	}
	if updateID != nil {
		var prior int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM personal_fact_events WHERE user_id=$1 AND source_update_id=$2 AND namespace=$3 AND subject=$4 AND predicate=$5`, userID, *updateID, p.Namespace, p.Subject, p.Predicate).Scan(&prior); err != nil {
			return "", err
		}
		if prior > 0 {
			return "", nil
		}
	}
	operation := p.Operation
	if operation == "forget" {
		if oldID == "" {
			return "", nil
		}
		if _, err := tx.Exec(ctx, `UPDATE user_memories SET valid_until=NOW(),updated_at=NOW() WHERE id=$1`, oldID); err != nil {
			return "", err
		}
		return k.auditTx(ctx, tx, userID, p, operation, &oldID, nil, updateID, messageID)
	}
	value, err := json.Marshal(p.Value)
	if err != nil {
		return "", err
	}
	if operation == "merge" && oldID != "" {
		var oldList, newList []any
		if json.Unmarshal(oldValue, &oldList) == nil && json.Unmarshal(value, &newList) == nil {
			seen := map[string]bool{}
			merged := []any{}
			for _, item := range append(oldList, newList...) {
				key, _ := json.Marshal(item)
				if !seen[string(key)] {
					seen[string(key)] = true
					merged = append(merged, item)
				}
			}
			value, _ = json.Marshal(merged)
		}
	}
	if oldID != "" && string(oldValue) == string(value) && sourceRank(sourceType) <= sourceRank(oldSource) {
		return "", nil
	}
	if oldID != "" {
		if _, err := tx.Exec(ctx, `UPDATE user_memories SET valid_until=NOW(),updated_at=NOW() WHERE id=$1`, oldID); err != nil {
			return "", err
		}
	}
	var newID string
	var documentID *string
	if len(sourceDocumentID) > 0 {
		documentID = sourceDocumentID[0]
	}
	err = tx.QueryRow(ctx, `INSERT INTO user_memories(user_id,category,scope,key,value,confidence,namespace,subject,predicate,source_type,source_id,source_update_id,source_message_id,source_text,correlation_id,operation,source_document_id)
       VALUES($1,'personal_fact','permanent',$2,$3::jsonb,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, userID, p.Namespace+"."+p.Predicate, string(value), p.Confidence, p.Namespace, p.Subject, p.Predicate, sourceType, factSourceID(sourceType, updateID), updateID, messageID, p.Evidence, correlation.GetCorrelationID(ctx), operation, documentID).Scan(&newID)
	if err != nil {
		return "", err
	}
	if oldID != "" {
		if _, err := tx.Exec(ctx, `UPDATE user_memories SET superseded_by=$2 WHERE id=$1`, oldID, newID); err != nil {
			return "", err
		}
	}
	return k.auditTx(ctx, tx, userID, p, operation, optionalID(oldID), &newID, updateID, messageID, documentID)
}
func factSourceID(sourceType string, updateID *int64) string {
	if updateID == nil {
		return sourceType
	}
	return fmt.Sprintf("telegram:%d", *updateID)
}
func optionalID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
func (k *PersonalKnowledge) auditTx(ctx context.Context, tx pgx.Tx, userID string, p FactProposal, operation string, oldID, newID *string, updateID, messageID *int64, documentID ...*string) (string, error) {
	var sourceDoc *string
	if len(documentID) > 0 {
		sourceDoc = documentID[0]
	}
	_, err := tx.Exec(ctx, `INSERT INTO personal_fact_events(user_id,namespace,subject,predicate,operation,previous_fact_id,new_fact_id,source_update_id,source_message_id,correlation_id,source_document_id)
      VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, userID, p.Namespace, p.Subject, p.Predicate, operation, oldID, newID, updateID, messageID, correlation.GetCorrelationID(ctx), sourceDoc)
	if err != nil {
		return "", err
	}
	return operation + ":" + p.Namespace + "/" + p.Subject + "/" + p.Predicate, nil
}
func personalNamespaces(text string) []string {
	t := strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	switch {
	case strings.Contains(t, "проект"):
		return []string{"project"}
	case strings.Contains(t, "ноут") || strings.Contains(t, "комп") || strings.Contains(t, "устройств"):
		return []string{"device", "technology"}
	case strings.Contains(t, "обращ") || strings.Contains(t, "как писать") || strings.Contains(t, "стиль"):
		return []string{"assistant_preferences", "identity"}
	case strings.Contains(t, "профил") || strings.Contains(t, "обо мне") || strings.Contains(t, "кто я") || strings.Contains(t, "помнишь про меня") || strings.Contains(t, "откуда"):
		return []string{"identity", "location", "fitness", "nutrition", "assistant_preferences", "lifestyle", "device", "project", "work", "education", "technology", "preference", "routine", "other"}
	case strings.Contains(t, "тренир") || strings.Contains(t, "лег курл") || strings.Contains(t, "сколько ставить"):
		return []string{"identity", "fitness", "assistant_preferences"}
	case strings.Contains(t, "ешь") || strings.Contains(t, "калори") || strings.Contains(t, "еда"):
		return []string{"identity", "nutrition", "fitness"}
	default:
		return []string{"identity", "assistant_preferences"}
	}
}
func profileSourceQuery(text string) bool {
	t := strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	return strings.Contains(t, "в профиле") || strings.Contains(t, "из профиля") || strings.Contains(t, "profile.md") || strings.Contains(t, "profile.json") || strings.Contains(t, "из этого файла") || strings.Contains(t, "из файла")
}
func (k *PersonalKnowledge) Relevant(ctx context.Context, userID, text string) ([]PersonalFact, error) {
	namespaces := personalNamespaces(text)
	sourceOnly := profileSourceQuery(text)
	projectOnly := len(namespaces) == 1 && namespaces[0] == "project"
	limit := 30
	if sourceOnly || projectOnly {
		limit = 80
	}
	rows, err := k.db.Query(ctx, `SELECT m.id,m.namespace,m.subject,m.predicate,m.value,m.confidence,m.source_type,m.valid_from,
 COALESCE(m.source_id,''),m.source_document_id,COALESCE(d.filename,''),m.source_message_id,COALESCE(m.source_text,''),m.created_at
 FROM user_memories m LEFT JOIN personal_documents d ON d.id=m.source_document_id
 WHERE m.user_id=$1 AND m.category='personal_fact' AND m.namespace=ANY($2) AND m.superseded_by IS NULL AND (m.valid_until IS NULL OR m.valid_until>NOW())
 AND (NOT $3::bool OR m.source_type IN ('profile_import','profile_document','profile_json'))
 AND ($5::text='' OR d.filename=$5)
 ORDER BY CASE m.namespace WHEN 'identity' THEN 0 WHEN 'project' THEN 1 WHEN 'fitness' THEN 2 ELSE 3 END,m.updated_at DESC LIMIT $4`, userID, namespaces, sourceOnly, limit, func() string {
		t := strings.ToLower(text)
		if strings.Contains(t, "profile.md") {
			return "profile.md"
		}
		if strings.Contains(t, "profile.json") {
			return "profile.json"
		}
		return ""
	}())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonalFact{}
	for rows.Next() {
		var f PersonalFact
		if err := rows.Scan(&f.ID, &f.Namespace, &f.Subject, &f.Predicate, &f.Value, &f.Confidence, &f.SourceType, &f.ValidFrom, &f.SourceID, &f.SourceDocumentID, &f.SourceFilename, &f.SourceMessageID, &f.SourceExcerpt, &f.CreatedAt); err != nil {
			return nil, err
		}
		if f.Namespace != "assistant_preferences" {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}
func (k *PersonalKnowledge) ListProjects(ctx context.Context, userID string) ([]PersonalFact, error) {
	return k.Relevant(ctx, userID, "какие у меня проекты")
}

func (k *PersonalKnowledge) RelevantByChat(ctx context.Context, chatID int64, text string) ([]PersonalFact, error) {
	var userID string
	err := k.db.QueryRow(ctx, `SELECT user_id FROM telegram_users WHERE telegram_user_id=$1`, chatID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k.Relevant(ctx, userID, text)
}
