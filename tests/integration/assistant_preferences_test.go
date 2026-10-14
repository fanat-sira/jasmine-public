package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"jasmine/internal/clients/openrouter"
	"jasmine/internal/module/assistant"
	"jasmine/internal/module/telegram"
)

func TestPostgreSQL_AssistantPreferencesVersionsAndIsolation(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := telegram.NewRepository(pool)
	one, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 801001})
	if err != nil {
		t.Fatal(err)
	}
	two, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 801002})
	if err != nil {
		t.Fatal(err)
	}
	p := assistant.NewAssistantPreferences(pool, nil, "")
	p.ConfigureOwner(801001, "PRIVATE_OWNER_VOICE")
	e := assistant.AssistantEvent{Kind: assistant.AssistantUserMessage, UserID: one.UserID, ChatID: 801001, UpdateID: 1, MessageID: 1, Text: "обращайся ко мне на вы"}
	if _, handled, err := p.Handle(ctx, e); err != nil || !handled {
		t.Fatalf("preference command: %v", err)
	}
	first, err := p.LoadByChat(ctx, 801001)
	if err != nil || first.Version != 1 || first.Settings["form_of_address"] != "formal" || first.Personality != "PRIVATE_OWNER_VOICE" {
		t.Fatalf("profile=%+v err=%v", first, err)
	}
	other, err := p.LoadByChat(ctx, 801002)
	if err != nil || other.Version != 0 || len(other.Settings) != 0 || other.Personality != "" {
		t.Fatal("preferences/personality crossed user boundary")
	}
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := p.Load(ctx, one.UserID)
	if duplicate.Version != 1 {
		t.Fatal("duplicate Telegram update created another revision")
	}
	e.UpdateID = 2
	e.Text = "давай лучше на ты"
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	updated, _ := p.Load(ctx, one.UserID)
	if updated.Settings["form_of_address"] != "informal" {
		t.Fatal("new style did not replace old style")
	}
	e.UpdateID = 3
	e.Text = "верни прежний стиль"
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	restored, _ := p.Load(ctx, one.UserID)
	if restored.Settings["form_of_address"] != "formal" || restored.Version != 3 {
		t.Fatal("undo did not restore prior settings")
	}
	e.UpdateID = 4
	e.Text = "сбрось настройки общения"
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	reset, _ := p.LoadByChat(ctx, 801001)
	if len(reset.Settings) != 0 || reset.Personality != "PRIVATE_OWNER_VOICE" {
		t.Fatal("reset failed or removed the owner base")
	}
	_ = two
	// Concurrent independent changes must retain both keys and all revisions.
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key, value := "emoji", "disabled"
			if i%2 == 0 {
				key, value = "verbosity", "concise"
			}
			event := e
			event.UpdateID = int64(100 + i)
			_, err := p.Mutate(ctx, event, assistant.PreferenceChanges{Set: map[string]string{key: value}}, "replace", "", "")
			failures <- err
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	final, _ := p.Load(ctx, one.UserID)
	if final.Version != 12 || final.Settings["emoji"] != "disabled" || final.Settings["verbosity"] != "concise" {
		t.Fatalf("lost concurrent changes: %+v", final)
	}
	var revisions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM assistant_preference_revisions WHERE user_id=$1`, one.UserID).Scan(&revisions); err != nil || revisions != 12 {
		t.Fatal("revision provenance lost")
	}
}
func TestPostgreSQL_PreferenceImportRequiresBoundConfirmation(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := telegram.NewRepository(pool)
	user, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 802001})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 802002})
	if err != nil {
		t.Fatal(err)
	}
	p := assistant.NewAssistantPreferences(pool, nil, "")
	e := assistant.AssistantEvent{Kind: assistant.AssistantUserDocument, UserID: user.UserID, ChatID: 802001, UpdateID: 201, MessageID: 201, Document: &telegram.DocumentInput{Kind: telegram.DocumentText, Filename: "prompt.txt", Text: "пиши коротко и без эмодзи"}}
	preview, handled, err := p.Handle(ctx, e)
	if err != nil || !handled {
		t.Fatal(err)
	}
	active, _ := p.Load(ctx, user.UserID)
	if active.Version != 0 || active.Instructions != "" || len(active.Settings) != 0 {
		t.Fatal("filename alone installed preferences")
	}
	command := preview.ReplyContext.CurrentState["apply_command"].(string)
	e.Kind = assistant.AssistantUserMessage
	e.Document = nil
	e.UpdateID = 202
	e.Text = command
	foreign := e
	foreign.UserID = other.UserID
	foreign.ChatID = 802002
	if _, _, err = p.Handle(ctx, foreign); err == nil {
		t.Fatal("another user applied the proposal")
	}
	wrongChat := e
	wrongChat.ChatID = 999
	if _, _, err = p.Handle(ctx, wrongChat); err == nil {
		t.Fatal("confirmation from another chat accepted")
	}
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	confirmed, _ := p.Load(ctx, user.UserID)
	if confirmed.Version != 1 || confirmed.Settings["verbosity"] != "concise" || confirmed.Settings["emoji"] != "disabled" || confirmed.Instructions == "" {
		t.Fatalf("confirmed import missing: %+v", confirmed)
	}
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal("confirmation retry failed")
	}
	after, _ := p.Load(ctx, user.UserID)
	if after.Version != 1 {
		t.Fatal("confirmation retry duplicated import")
	}
	var filename string
	var approval bool
	if err = pool.QueryRow(ctx, `SELECT source_filename,confirmed FROM assistant_preference_revisions WHERE user_id=$1`, user.UserID).Scan(&filename, &approval); err != nil || filename != "prompt.txt" || !approval {
		t.Fatal("import source or approval not recorded")
	}
	// A preview cannot overwrite changes made after it was presented.
	e.Kind = assistant.AssistantUserDocument
	e.UpdateID = 203
	e.Document = &telegram.DocumentInput{Kind: telegram.DocumentJSON, Filename: "prompt.json", Text: `{"preferences":{"tone":"professional"}}`}
	preview, _, err = p.Handle(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	e.Kind = assistant.AssistantUserMessage
	e.Document = nil
	e.UpdateID = 204
	e.Text = "обращайся на вы"
	if _, _, err = p.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.UpdateID = 205
	e.Text = fmt.Sprint(preview.ReplyContext.CurrentState["apply_command"])
	if _, _, err = p.Handle(ctx, e); err == nil {
		t.Fatal("stale preview overwrote newer preferences")
	}

	// Cancellation and expiration must prevent applying the exact preview later.
	for i, mode := range []string{"cancel", "expire"} {
		e.Kind = assistant.AssistantUserDocument
		e.UpdateID = int64(206 + i*3)
		e.Document = &telegram.DocumentInput{Kind: telegram.DocumentJSON, Filename: "prompt.json", Text: `{"preferences":{"tone":"playful"}}`}
		pending, _, err := p.Handle(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		apply := pending.ReplyContext.CurrentState["apply_command"].(string)
		e.Kind = assistant.AssistantUserMessage
		e.Document = nil
		e.UpdateID++
		if mode == "cancel" {
			e.Text = pending.ReplyContext.CurrentState["cancel_command"].(string)
			if _, _, err = p.Handle(ctx, e); err != nil {
				t.Fatal(err)
			}
		} else {
			id := strings.Fields(apply)[1]
			if _, err = pool.Exec(ctx, `UPDATE assistant_preference_proposals SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		e.UpdateID++
		e.Text = apply
		if _, _, err = p.Handle(ctx, e); err == nil {
			t.Fatalf("%s proposal was applied", mode)
		}
	}
	active, err = p.Load(ctx, user.UserID)
	if err != nil || active.Version != 2 || active.Settings["tone"] != "" {
		t.Fatal("inactive proposal changed preferences")
	}
	e.Kind = assistant.AssistantUserDocument
	e.UpdateID = 220
	e.Document = &telegram.DocumentInput{Kind: telegram.DocumentText, Filename: "prompt.txt", Text: string([]byte{0xff})}
	if _, _, err = p.Handle(ctx, e); err == nil {
		t.Fatal("non-UTF-8 prompt accepted")
	}
}

type forbiddenPreferencePlanner struct{ t *testing.T }

func (p forbiddenPreferencePlanner) Next(context.Context, assistant.AssistantEvent, *assistant.JasmineContext) (assistant.JasmineStep, error) {
	p.t.Fatal("style command reached the tool planner")
	return assistant.JasmineStep{}, nil
}
func TestPostgreSQL_PreferencesApplyToImmediateAndUnrelatedReply(t *testing.T) {
	pool, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	user, err := telegram.NewRepository(pool).FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 803001})
	if err != nil {
		t.Fatal(err)
	}
	p := assistant.NewAssistantPreferences(pool, nil, "")
	runtime := assistant.NewJasmineRuntime(forbiddenPreferencePlanner{t}, 5)
	runtime.SetAssistantPreferences(p)
	runtime.SetTurnObserver(func(context.Context, assistant.AssistantEvent) ([]string, error) {
		t.Fatal("style command reached the personal-fact extractor")
		return nil, nil
	})
	result, err := runtime.IngestMessage(ctx, user.UserID, 803001, 301, 301, nil, "обращайся ко мне на вы")
	if err != nil || result.ReplyContext == nil || len(result.ReplyContext.ActionsPerformed) == 0 {
		t.Fatal("style command was not handled before tools")
	}
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var request struct{ Messages []openrouter.Message }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt := request.Messages[0].Content.(string)
		if !strings.Contains(prompt, `"form_of_address":"formal"`) || !strings.HasSuffix(prompt, assistant.ReplyCoreInstructions) {
			t.Error("current preferences/core missing from actual reply request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "TEST_REPLY"}}}})
	}))
	defer server.Close()
	composer := assistant.NewTelegramReplyComposer(openrouter.NewClientWithBaseURL("test", "main", server.URL, server.Client()), "reply")
	composer.SetAssistantPreferences(p)
	result.ReplyContext.UserMessage = "обращайся ко мне на вы"
	if _, err = composer.Compose(ctx, 803001, *result.ReplyContext, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = composer.Compose(ctx, 803001, telegram.ReplyContext{UserMessage: "не используй прошлые сообщения; сколько было повторений?", EventType: "general_chat"}, nil); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatal("reply requests were not issued")
	}
}
func TestPostgreSQL_PreferenceMigrationCarriesValidatedLegacySettings(t *testing.T) {
	pool, cleanup := setupTestDBThrough(t, "000028_telegram_only.up.sql")
	defer cleanup()
	ctx := context.Background()
	user, err := telegram.NewRepository(pool).FindOrCreateTelegramUser(ctx, &telegram.TelegramUser{TelegramUserID: 804001})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO telegram_updates(update_id,chat_id,telegram_user_id,raw_payload) VALUES(401,804001,804001,'{}')`); err != nil {
		t.Fatal(err)
	}
	knowledge := assistant.NewPersonalKnowledge(pool, nil, "")
	if _, err = knowledge.Observe(ctx, user.UserID, 401, 401, "не ставь точку в конце ответа"); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000029_assistant_preferences.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	profile, err := assistant.NewAssistantPreferences(pool, nil, "").Load(ctx, user.UserID)
	if err != nil || profile.Settings["final_sentence_period"] != "disabled" {
		t.Fatalf("legacy preference not migrated: %+v %v", profile, err)
	}
	down, err := os.ReadFile(filepath.Join(findMigrationsDir(), "000029_assistant_preferences.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var users int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, user.UserID).Scan(&users); err != nil || users != 1 {
		t.Fatal("preference rollback damaged user identity")
	}
	if _, err = pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
}
