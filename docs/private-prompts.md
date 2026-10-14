# Private prompt overrides

The public catalog contains neutral defaults. Personal voice and training preferences belong in operator-owned files or the private `system_prompts` database, never in source code or migrations.

Existing database templates are preserved. The private `jasmine_reply` is now an owner-only base personality rather than the complete system prompt; mandatory reply rules are always compiled separately. With one allowed Telegram ID, ownership is inferred. With multiple allowed IDs, configure `JASMINE_OWNER_TELEGRAM_ID` explicitly. New users start from neutral defaults. No private text is embedded in the public binary or copied into a Docker image.

User-specific preferences, confirmed imports, and revision history are described in [communication-preferences.md](communication-preferences.md). Current user style settings take priority over the owner personality.

## Optional file configuration

Use a UTF-8 JSON object whose keys are registered prompt names and whose values are complete templates:

```json
{
  "jasmine_reply": "You are Jasmine. Reply clearly in the user's language. Only claim actions confirmed by verified_facts. Today is {{current_date}}."
}
```

For a native Go run, set `JASMINE_PROMPT_OVERRIDES_FILE` to the absolute path of the file. On startup the application validates all entries and saves them in one database transaction before seeding missing defaults. An unreadable or invalid configured file stops startup; errors do not print templates. These explicit file overrides take precedence over existing database settings. Removing the configuration does not delete the saved database templates.

For Docker, keep the file outside the checkout and set `JASMINE_PROMPT_OVERRIDES_HOST_FILE` to its absolute path. Make it readable by the container's `appuser`, and restrict access to the operator and application account. Add the read-only overlay:

```bash
docker compose --env-file .env \
  -f docker-compose.yml -f docker-compose.prompts.yml up --build -d
```

For webhook mode, also include `-f docker-compose.webhook.yml`. Subsequent deployments without this overlay keep the templates already stored in PostgreSQL. To apply future file edits, include the overlay and recreate the application again. Do not set the native container path in the base `.env` unless the file is mounted on every deployment.

Local working copies may use `.private/prompts.json`; `.private/` is excluded from Git and the Docker build context. Do not force-add it, attach it to issues, or commit its content in examples, tests, migration seeds, or logs. Back it up in private operator storage.

## Publication

Never copy personal templates into source, migration seeds, examples, tests, or logs. Keep private Git history and operator files out of this portfolio repository.
