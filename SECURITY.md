# Security

Do not include credentials, database exports, personal prompts, or conversations in issues or pull requests. Report sensitive findings privately through GitHub private vulnerability reporting if enabled.

## Repository and CI

This portfolio snapshot contains no private Git history or production deployment job. CI runs on GitHub-hosted runners with read-only repository permissions and no persisted checkout credentials. Do not connect a production self-hosted runner or add production credentials to this repository.

Keep local configuration, operator prompts, profiles, logs, and database dumps outside Git. Ignoring files does not remove them from earlier commits. Scan history and the working tree with Gitleaks before publishing updates. Rotate any exposed credentials; clones and cached views can retain old data.

## Application access and data

Only numeric Telegram IDs in `TELEGRAM_ALLOWED_USER_IDS` may use the bot. Empty configuration fails startup. Denied senders and non-private chats are rejected before provisioning, storage, or model calls. Telegram handles never grant access.

Polling exposes only `/health`. Webhook mode adds an authenticated Telegram webhook and requires a secret. There are no HTTP account or administrative APIs. PostgreSQL holds conversations, imported documents, personal facts, and diagnostics; secure it and its backups. Relevant text and images are sent to the configured model provider.

Prompt customization belongs in private operator files or database overrides. There is no HTTP prompt editor. See [private prompts](docs/private-prompts.md) and [local operation](docs/deployment.md).
