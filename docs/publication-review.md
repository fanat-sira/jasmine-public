# Portfolio snapshot

This is a source snapshot without the private development history, old Actions runs, server runbooks, production deployment scripts, or personal prompt files. Its CI contains only `Secret scan` and `Test & Build`, both on GitHub-hosted runners.

## Publication checks

Before publishing or updating:

- Review the exact file diff, including documentation and synthetic fixtures.
- Run Go tests, vet, and the opt-in integration suite against a disposable PostgreSQL database.
- Scan the working tree and, after committing, the public repository's history with Gitleaks.
- Confirm that workflows have no production deployment job, credentials, or self-hosted runner.
- Configure main protection using the prepared [settings](../.github/main-protection.json), adapted to the repository. This file does not activate protection by itself.

A scanner does not certify the absence of personal prose. Private voice templates, messages, identifiers, and profiles require a separate privacy review. New GitHub checks must run in this repository; results in another repository do not prove this copy passed.

The [demo](demo.md) uses synthetic examples and links to workflow tests. It is not a recording of a live model conversation.
