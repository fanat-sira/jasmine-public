# Jasmine

A personal AI agent in Telegram with persistent state and validated actions, applied to workouts, nutrition, and personal context. Built with Go and PostgreSQL.

Jasmine turns messages and photos into validated domain actions: recording a set, correcting a meal, importing a profile, or planning a workout. Conversation history and application state live in PostgreSQL. Language models select tools and interpret input; Go services validate and persist the results.

The project is under active development. Workout tools use a shared runtime; nutrition, profile imports, and image workflows still use specialized adapters.

## Features

- **Workouts:** exercise search, workout proposals, live sessions, set logging, and corrections with revision history.
- **Nutrition:** food diary, water tracking, photo-based meal estimates, and label lookup through Open Food Facts.
- **Personal context:** JSON and Markdown profile imports, source-linked personal facts, and retrieval of relevant preferences and projects.
- **Communication preferences:** user-specific style settings, revision history, confirmed prompt imports, and reply prompts compiled for each turn.
- **Telegram:** polling or webhook transport, private-chat access controls, photo albums, duplicate-update handling, and queued replies.
- **Diagnostics:** persisted LLM call results, token usage, delivery status, and correlation IDs, with credential redaction.

## Product demo

An illustrative conversation with synthetic data; reply wording varies with the model:

```text
User:    First set: 12 reps at 27 kg.
Jasmine: Recorded: 12 reps at 27 kg.
User:    Correction: 10 reps, not 12.
Jasmine: Updated the set to 10 reps at 27 kg.
```

The bot resolves the active exercise, validates the operation, saves it, and then composes the confirmation from the recorded result. A delivery retry reuses the saved action rather than recording a second set. See [the demo walkthrough](docs/demo.md) for context and test evidence.

## Architecture

```mermaid
flowchart LR
    T[Telegram update] --> I[Inbox and duplicate check]
    I --> R[Jasmine runtime]
    R --> P[Planner and tool registry]
    P --> D[Domain services]
    R --> A[Image and profile adapters]
    A --> D
    D --> DB[(PostgreSQL)]
    DB --> O[Delivery outbox]
    O --> C[Reply composer]
    C --> T
```

Three independently configured model roles use OpenRouter:

| Variable | Responsibility |
| --- | --- |
| `LLM_MAIN` | Planning, tool selection, and structured extraction |
| `LLM_PHOTO` | Image classification and visual extraction |
| `LLM_REPLY` | Writing the final conversational reply |

The runtime validates tool arguments and ownership before executing an action. Core write flows save domain changes and a reply request in one transaction. Delivery retries reuse that request without repeating the domain action. Telegram can still receive a duplicate reply if it accepts a message but the acknowledgement is lost.

Domain prompts are defined in [the prompt catalog](internal/module/prompts/catalog.go). Startup creates missing database entries; domain database overrides take precedence. Replies combine mandatory core rules, a neutral personality, an optional private owner personality, and current user preferences; see [communication preferences](docs/communication-preferences.md). PostgreSQL stores the conversation history, while model requests use a bounded context window.

See [runtime boundaries](docs/jasmine-runtime.md) and [message processing](docs/message-processing.md) for implementation details.

## Engineering decisions

| Decision | Reason and tradeoff |
| --- | --- |
| Models choose registered tools; Go validates and executes them | Model output cannot directly write SQL or supply another user's identity. New actions require explicit schemas and executors. |
| PostgreSQL owns conversation and domain state | State is inspectable and persists across model calls; context assembly and migrations remain application responsibilities. |
| Domain writes and reply requests share a transaction | Delivery can retry without repeating the write. Telegram acknowledgements can still be ambiguous, so a duplicate outgoing reply remains possible. |
| Planning, image extraction, and reply generation use separate model roles | Each request has a defined purpose. More provider calls introduce latency and cost, and reply generation still requires an available model. |
| Deterministic fallbacks cover supported workout actions | Certain model failures remain recoverable; fallback coverage is deliberately narrower than the full planner. |

The implementation is maintained by [fanat-sira](https://github.com/fanat-sira). The project includes the runtime, domain services, Telegram transport, data model, local Docker setup, and workflow tests. Current implementation boundaries are documented explicitly.

## Stack and layout

Go 1.26+, Chi, pgx, PostgreSQL 16, Docker Compose, and GitHub Actions.

```text
cmd/jasmine/           Server entry point
cmd/jasmine-import/    Exercise catalog importer
internal/clients/     Telegram, OpenRouter, and Open Food Facts clients
internal/module/      Domain services, assistant runtime, and handlers
internal/pkg/         Middleware, logging, and shared utilities
scripts/migrations/   PostgreSQL schema and data migrations
tests/integration/    PostgreSQL-backed workflow and migration tests
docs/                 Architecture, demo, and deployment notes
```

## Run locally

The simplest setup uses Docker with the Compose plugin. To work on the source directly, install Go 1.26+ as well.

```bash
cp .env.example .env
```

Edit `.env` before starting. Set your PostgreSQL password, Telegram bot token, allowed Telegram user IDs, and OpenRouter API key. Choose available model IDs for all three `LLM_*` roles. The values in `.env.example` are configuration examples, not credentials or a guarantee of model availability.

Polling is enabled in the example and needs no public domain. Set `TELEGRAM_ALLOWED_USER_IDS` to your numeric Telegram user ID; an empty value is rejected at startup. Usernames never grant access.

```bash
docker compose --env-file .env up --build -d
```

Compose starts the stores, applies migrations, imports the exercise catalog, and starts the bot. PostgreSQL does not publish a host port. The polling configuration also keeps the application port inside the Compose network.

For existing servers and deployment preflight checks, see [docs/deployment.md](docs/deployment.md).

The bundled prompts are neutral defaults. Keep personal voice and preferences in private database overrides or an operator-owned file; see [private prompt configuration](docs/private-prompts.md). Existing database templates are preserved across application updates.

**Existing database:** migration `000023_reset_legacy_user_data` clears legacy user records, conversations, meals, and workouts. Its down migration cannot restore them. Use a new local database or review the migration and back up existing data before applying it.

## Verification

```bash
go test ./...
go vet ./...
go build -buildvcs=false -o /tmp/jasmine ./cmd/jasmine
go build -buildvcs=false -o /tmp/jasmine-import ./cmd/jasmine-import
```

PostgreSQL integration tests are opt-in and **reset the target database schema**. Run them only against a disposable test database:

```bash
RUN_INTEGRATION_TESTS=1 \
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/jasmine_test?sslmode=disable' \
go test -count=1 ./tests/integration/...
```

Without `RUN_INTEGRATION_TESTS=1`, database integration tests are skipped. Paid provider smoke tests are also opt-in.

GitHub Actions runs Go tests, vet, race detection, PostgreSQL integration tests, and builds the server and production image. This portfolio repository has no production deployment job or production credentials.

## Data handling and limitations

Messages, imported documents, personal facts, and model diagnostics can contain personal data and are stored in the application database. Relevant text is sent to OpenRouter; photos are sent to the image model during processing. Photo bytes are not stored in PostgreSQL, but Telegram file identifiers and extracted results are retained. Deployment operators need to manage database access, backups, and provider data policies.

The bot currently handles private chats. Voice transcription is not implemented. Image-based nutrition values are estimates. Telegram is the only user interface. In polling mode, HTTP exposes only `/health`. Webhook mode additionally exposes `POST /api/v1/telegram/webhook`, authenticated with the Telegram transport secret. There is no registration, login, JWT, administrative API, or web client.

Repository safety guidance is in [SECURITY.md](SECURITY.md). The [publication review](docs/publication-review.md) records what was checked and what remains to be verified before changing repository visibility.

## Access and identity

`TELEGRAM_ALLOWED_USER_IDS` is mandatory. Messages from other sender IDs are ignored before identity creation, database writes, or model calls. An empty service allowlist denies all users. Telegram handles and profile names are display metadata; profiles are mapped by immutable numeric sender ID.

Webhook mode requires `TELEGRAM_SECRET_TOKEN` in every environment. The webhook handler rejects an unconfigured or incorrect secret before reading the update. The webhook route is absent in polling mode.

Migration `000028_telegram_only` removes the retired HTTP credential/token tables, account columns, and generated-workout cache. It preserves domain users, Telegram mappings, messages, personal profiles, meals, and workouts. Its down migration restores compatibility structure only, not discarded credentials. Historical schema migrations keep their ordering and upgrade behavior; the training prompt seed uses a neutral template instead of personal preferences.

## License and bundled data

[MIT](LICENSE). The exercise catalog was authored by the repository owner and is included under the same license. Its metadata records the author and provenance. Dependencies retain their own licenses.
