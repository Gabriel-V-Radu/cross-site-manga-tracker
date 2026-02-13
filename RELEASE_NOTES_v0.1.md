# Release Notes — v0.1

## Highlights
- Local-first cross-site tracker backend in Go + Fiber
- SQLite persistence with automatic migrations and default seed data
- Native connector framework with two built-in connectors:
  - MangaDex
  - MangaPlus
- YAML connector loader for extensible source integrations
- HTMX dashboard with distinctive editorial UI:
  - Tracker list/grid
  - Search and filtering
  - Add/edit/delete flows
- Background polling scheduler for tracker updates
- Notification pipeline with webhook notifier support
- Backup and restore scripts for local and Docker workflows

## Included endpoints
- Health: `/health`, `/v1/health`
- Connectors: `/v1/connectors`, `/v1/connectors/health`
- Trackers API: `/v1/trackers` (+ CRUD)
- Dashboard: `/dashboard` and HTMX partial routes

## Operational defaults
- Single-PC mode via localhost bind (`127.0.0.1:8080`)
- Polling enabled every 30 minutes by default
- Notifications disabled by default unless webhook is configured

## Breaking changes
- None (initial public release)

## Known limitations
- YAML connectors currently require a source API contract with predictable response fields
- Notification integration currently focused on webhook output
- Remote multi-device access is deferred (see `FUTURE_IMPROVEMENTS.md`)

## Upgrade notes
- N/A for initial release

## Next priorities
- Harden release pipeline and reproducible smoke task
- Add richer connector diagnostics and per-source retry controls
- Expand notification channels
