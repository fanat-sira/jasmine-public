package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	offpkg "jasmine/internal/clients/off"
	orrpkg "jasmine/internal/clients/openrouter"
	tgclientpkg "jasmine/internal/clients/telegram"
	"jasmine/internal/config"
	assistantmodule "jasmine/internal/module/assistant"
	exercisemodule "jasmine/internal/module/exercise"
	promptsmodule "jasmine/internal/module/prompts"
	tgmodule "jasmine/internal/module/telegram"
	workoutmodule "jasmine/internal/module/workout"
	"jasmine/internal/pkg/logger"
	"jasmine/internal/pkg/observability"
	"jasmine/internal/storage"
)

func main() {
	cfg := config.MustLoad()
	log := logger.Setup(cfg.Env)
	log.Info("starting", slog.String("env", cfg.Env))

	ctx := context.Background()

	db, err := storage.NewPostgres(ctx, cfg.DBURL)
	if err != nil {
		log.Error("postgres connect failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer db.Close()

	var offClient *offpkg.Client
	if cfg.OFFEnabled {
		offClient = offpkg.NewClient()
	}
	exerciseRepo := exercisemodule.NewRepository(db)

	workoutRepo := workoutmodule.NewRepository(db)

	var orClient *orrpkg.Client
	if cfg.OpenRouter.APIKey != "" {
		orClient = orrpkg.NewClient(cfg.OpenRouter.APIKey, cfg.OpenRouter.MainModel)
		log.Info(
			"openrouter AI enabled",
			slog.String("main_model", cfg.OpenRouter.MainModel),
			slog.String("photo_model", cfg.OpenRouter.PhotoModel),
			slog.String("reply_model", cfg.OpenRouter.ReplyModel),
		)
	}
	aiRepo := promptsmodule.NewRepository(db)
	if cfg.PromptOverridesFile != "" {
		if err := aiRepo.ApplyOverridesFile(ctx, cfg.PromptOverridesFile); err != nil {
			log.Error("private prompt overrides could not be loaded")
			os.Exit(1)
		}
	}
	if err := aiRepo.EnsurePromptDefaults(ctx); err != nil {
		log.Error("prompt catalog bootstrap failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// --- Telegram module ---
	var tgClient *tgclientpkg.Client
	if cfg.Telegram.BotToken != "" {
		tgClient = tgclientpkg.NewClient(cfg.Telegram.BotToken)
		log.Info("telegram bot enabled")
	}
	tgRepo := tgmodule.NewRepository(db)
	tgRenderer := tgmodule.NewDraftRenderer(tgClient, log)
	tgDeliveryWorker := tgmodule.NewDeliveryWorker(tgRepo, tgClient, log)
	replyComposer := assistantmodule.NewTelegramReplyComposer(orClient, cfg.OpenRouter.ReplyModel)
	tgDeliveryWorker.SetReplyComposer(replyComposer)
	tgSvc := tgmodule.NewService(tgRepo, tgClient, tgRenderer, tgDeliveryWorker, log)
	allowedTelegramUsers, _ := cfg.Telegram.AllowedTelegramUserIDs() // Validated by MustLoad.
	tgSvc.SetAllowedUserIDs(allowedTelegramUsers)
	tgHandler := tgmodule.NewHandler(tgSvc, cfg.Telegram.SecretToken, log)

	workoutExtractor := workoutmodule.NewExtractor(orClient, cfg.OpenRouter.MainModel)
	workoutExtractor.SetPromptStore(aiRepo)
	correctionExtractor := workoutmodule.NewCorrectionExtractor(orClient, cfg.OpenRouter.MainModel)
	correctionExtractor.SetPromptStore(aiRepo)
	exerciseResolver := exercisemodule.NewResolver(exerciseRepo, orClient, cfg.OpenRouter.MainModel)
	exerciseResolver.SetPromptStore(aiRepo)
	referentialResolver := workoutmodule.NewReferentialResolver(workoutRepo, exerciseResolver, tgRepo)
	correctionSvc := workoutmodule.NewCorrectionService(db, workoutRepo, referentialResolver, correctionExtractor, tgRepo, tgRenderer)

	workoutIngestion := workoutmodule.NewIngestionPipeline(db, workoutRepo, exerciseResolver, workoutExtractor, tgRepo, tgRenderer)
	workoutIngestion.SetCorrectionService(correctionSvc)

	memoryExtractor := assistantmodule.NewMemoryExtractor(orClient, cfg.OpenRouter.MainModel)
	memoryExtractor.SetPromptStore(aiRepo)
	contextBuilder := assistantmodule.NewContextBuilder(assistantmodule.DefaultBudgets())
	candidateSelector := assistantmodule.NewCandidateSelector(exerciseRepo)
	generator := assistantmodule.NewWorkoutGenerator(orClient, cfg.OpenRouter.MainModel)
	generator.SetPromptStore(aiRepo)
	sessionService := assistantmodule.NewSessionService(tgRepo, tgRenderer)

	orchestrator := assistantmodule.NewOrchestrator(
		db, orClient, cfg.OpenRouter.MainModel, cfg.OpenRouter.ReplyModel,
		workoutIngestion, correctionSvc, memoryExtractor, contextBuilder,
		candidateSelector, generator, sessionService,
		tgRepo, tgRenderer,
	)

	imageAnalyzer := assistantmodule.NewImageAnalyzer(orClient, cfg.OpenRouter.PhotoModel, cfg.OpenRouter.MainModel, cfg.OpenRouter.ReplyModel)
	imageAnalyzer.SetPromptStore(aiRepo)
	if offClient != nil {
		imageAnalyzer.SetProductLookup(assistantmodule.OFFBarcodeLookup{Client: offClient})
	}
	orchestrator.SetImageAnalyzer(imageAnalyzer)
	orchestrator.SetPromptStore(aiRepo)
	ownerID := cfg.OwnerTelegramID
	if ownerID == 0 && len(allowedTelegramUsers) == 1 {
		ownerID = allowedTelegramUsers[0]
	}
	ownerPersonality, err := aiRepo.GetPromptTemplate(ctx, promptsmodule.KeyJasmineReply)
	if err != nil {
		log.Error("owner personality could not be loaded")
		os.Exit(1)
	}
	if ownerPersonality == promptsmodule.Default(promptsmodule.KeyJasmineReply) {
		ownerPersonality = ""
	}
	if ownerID == 0 && ownerPersonality != "" {
		log.Error("set JASMINE_OWNER_TELEGRAM_ID to scope the private personality when multiple users are allowed")
		os.Exit(1)
	}
	orchestrator.AssistantPreferences().ConfigureOwner(ownerID, ownerPersonality)
	replyComposer.SetAssistantPreferences(orchestrator.AssistantPreferences())
	replyComposer.SetPersonalKnowledge(orchestrator.PersonalKnowledge())
	if err := orchestrator.BackfillPersonalProfiles(ctx); err != nil {
		log.Error("personal profile backfill failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	jasmineRuntime, err := assistantmodule.NewJasmineRuntimeWithLegacy(orchestrator)
	if err != nil {
		log.Error("Jasmine runtime initialization failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if orClient != nil {
		planner := assistantmodule.NewModelJasminePlanner(orClient, cfg.OpenRouter.MainModel, jasmineRuntime.ToolSpecs, assistantmodule.NewCoachFallbackPlanner(orchestrator))
		planner.SetPromptStore(aiRepo)
		jasmineRuntime.SetPlanner(planner)
	}
	publicInfo := assistantmodule.PublicRuntimeInfo{
		Runtime: "JasmineRuntime", MainModel: cfg.OpenRouter.MainModel,
		PhotoModel: cfg.OpenRouter.PhotoModel, ReplyModel: cfg.OpenRouter.ReplyModel,
		HasOpenRouter:           orClient != nil,
		HasAssistantPreferences: true,
	}
	skillNames := map[string]bool{}
	for _, spec := range jasmineRuntime.ToolSpecs() {
		publicInfo.Tools = append(publicInfo.Tools, spec.Name)
		skillNames[spec.Skill] = true
	}
	for name := range skillNames {
		publicInfo.Skills = append(publicInfo.Skills, name)
	}
	for key := range promptsmodule.Defaults {
		publicInfo.PromptKeys = append(publicInfo.PromptKeys, key)
	}
	replyComposer.SetPublicRuntimeInfo(publicInfo)
	tgSvc.SetIngestionPipeline(jasmineRuntime)
	tgSvc.SetImageIngestionPipeline(jasmineRuntime)
	tgSvc.SetAlbumIngestionPipeline(jasmineRuntime)
	tgSvc.SetDocumentIngestionPipeline(jasmineRuntime)

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	if tgClient != nil {
		if cfg.Telegram.PollingMode {
			log.Info("telegram running in polling mode")
		} else {
			log.Info("telegram running in webhook mode", slog.String("webhook_url", cfg.Telegram.WebhookURL))
		}
		go tgDeliveryWorker.Start(workerCtx, 2*time.Second)
		go tgSvc.StartMediaGroupWorker(workerCtx, time.Second)
	}

	if orClient != nil {
		orClient.SetObserver(observability.NewService(observability.NewRepository(db)))
	}
	r := newRouter(cfg.Telegram.PollingMode, tgHandler.HandleWebhook, log)

	// --- HTTP server ---
	srv := &http.Server{
		Addr:         cfg.HTTP.Addr(),
		Handler:      r,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("http server started", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	if tgClient != nil {
		if cfg.Telegram.PollingMode {
			go tgSvc.StartPolling(workerCtx)
		} else if cfg.Telegram.WebhookURL != "" {
			go tgSvc.StartWebhook(workerCtx, cfg.Telegram.WebhookURL, cfg.Telegram.SecretToken)
		}
	}

	<-quit
	log.Info("shutting down...")
	stopWorkers()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", slog.String("error", err.Error()))
	}
	log.Info("stopped")
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Duration("latency", time.Since(start)),
			)
		})
	}
}
