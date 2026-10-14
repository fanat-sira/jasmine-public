# Telegram demo walkthrough

This is a synthetic example to explain the flow. It is not a production transcript, a live model result, or evidence of a deployed service. No personal messages, Telegram identifiers, credentials, or screenshots are included.

## Example: record and correct a set

Assume an allowed user already has an active workout and an active exercise. The runtime can therefore resolve a short set report without asking which exercise it belongs to.

| Turn | Illustrative message | Application action |
| --- | --- | --- |
| 1 | User: “First set: 12 reps at 27 kg.” | Resolve active session and exercise; validate the set; persist 12 reps at 27 kg and a reply request. |
| 2 | Jasmine: “Recorded: 12 reps at 27 kg.” | Reply composer uses the confirmed action result; delivery worker sends and records the reply. |
| 3 | User: “Correction: 10 reps, not 12.” | Resolve the previous set; change repetitions to 10, preserve 27 kg, and persist correction provenance. |
| 4 | Jasmine: “Updated the set to 10 reps at 27 kg.” | Compose a confirmation from the updated state. |

Exact wording depends on the configured models and prompts. Without an active workout, a resolvable exercise, or sufficient context, the bot may ask for clarification instead. A failed delivery does not roll back a confirmed workout action, and retrying delivery does not execute that action again.

## Evidence in the repository

- [Runtime validation and planning tests](../internal/module/assistant/jasmine_runtime_test.go)
- [Coach action tests](../internal/module/assistant/coach_test.go)
- [Workout correction tests](../internal/module/workout/correction_test.go)
- [PostgreSQL concurrency tests](../tests/integration/idempotency_concurrency_test.go)
- [Reply boundary tests](../tests/integration/reply_boundary_test.go)

PostgreSQL-backed evidence requires the opt-in integration run against a disposable test database. A local unit-test pass alone does not prove a live Telegram/provider interaction.

To capture a real portfolio demonstration later, use a separate demo bot and synthetic profile/workout data. Hide account names, message identifiers, tokens, and private conversation history before sharing. No live provider call or message was sent for this walkthrough.
