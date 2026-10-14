# Local operation

This repository contains source code and GitHub-hosted CI. It has no production deployment job or server configuration. Start a separate bot with your own credentials and a new database using the [README](../README.md).

## Polling

```bash
cp .env.example .env
# Set your credentials and numeric Telegram allowlist before starting.
docker compose --env-file .env up --build -d
docker compose ps
```

Polling needs outbound Telegram access and no public domain. Application and database ports remain inside the Compose network. Do not run two polling processes with the same bot token.

## Webhook limitations

Configure an HTTPS reverse proxy, `TELEGRAM_POLLING_MODE=false`, `TELEGRAM_WEBHOOK_URL`, and `TELEGRAM_SECRET_TOKEN`. Use the webhook overlay to bind the app port to localhost:

```bash
docker compose --env-file .env -f docker-compose.yml -f docker-compose.webhook.yml up --build -d
```

Webhook processing currently runs inside the HTTP request. Processing errors are acknowledged with HTTP 200, and long model calls can outlast the HTTP write timeout. Polling is the preferred mode until webhook intake and background processing are separated.

## Storage and diagnostics

Use a new database for a demo. Migration 23 resets legacy user data; its rollback cannot recover it. Review migrations and back up any existing database before upgrading. Integration tests reset the selected schema and require a disposable test database.

`/health` is a process liveness check; it does not confirm Telegram or provider availability. Check a real reply with synthetic data before recording a demonstration. Do not share credentials, private logs, or database contents.

See [private prompt configuration](private-prompts.md) for optional read-only operator files.
