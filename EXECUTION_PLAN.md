# Cross-Site Tracker Execution Plan

## Scope and Architecture Lock (What we will build)
- Backend: Go + Fiber
- Database: SQLite
- Frontend: HTMX + Tailwind
- Deployment: Docker Compose
- Multi-PC access: Tailscale
- Source strategy: Native Go connectors + YAML-config connectors

## Success Criteria for v0.1
- You can add, update, and track entries from at least 2 sources.
- Background job checks for updates on a configurable interval.
- Notification flow works (at least one provider).
- App is reachable from multiple PCs securely through Tailscale.
- Backup and restore are documented and tested.

## Phase 1 — Foundation (Days 1-3)
### Deliverables
- Monorepo skeleton with backend and web app folders.
- Fiber server with health endpoint and base config loading.
- SQLite setup with migrations and seed support.
- Docker Compose for app + volume persistence.

### Tasks
1. Create project layout and baseline configs.
2. Implement config module (env + defaults).
3. Add DB connection, migrations, and migration runner.
4. Add initial entities: trackers, sources, chapters, settings.
5. Add health endpoint and structured logging.

### Exit Criteria
- App boots with one command.
- Health endpoint returns OK.
- Migrations apply on first run and are idempotent.

## Phase 2 — Core Tracking API (Days 4-7)
### Deliverables
- CRUD APIs for tracked items and statuses.
- Unified domain model for source items and chapter metadata.
- Basic validation and error model.

### Tasks
1. Implement tracker CRUD endpoints.
2. Implement status filters and sort options.
3. Implement chapter metadata persistence model.
4. Add API tests for create/update/list/delete paths.

### Exit Criteria
- Full CRUD passes tests.
- Filtering and sorting behave as expected.

## Phase 3 — Source Connector System (Days 8-12)
### Deliverables
- Connector interface and registry.
- 2 native connectors (pick stable sources first).
- YAML-driven connector loader for simple sources.

### Tasks
1. Define connector interface and normalization schema.
2. Implement connector registry and health checks.
3. Build native connector #1.
4. Build native connector #2.
5. Add YAML schema + validator + loader.
6. Add retry/backoff and rate-limit guardrails.

### Exit Criteria
- New source can be added by YAML for simple sites.
- Native connectors pass integration tests.

## Phase 4 — Web App UX (Days 13-17)
### Deliverables
- HTMX pages for list/grid view, details, add/edit dialogs.
- Search + filters + sort controls.
- Mobile-friendly responsive layout.

### Tasks
1. Create base layout and theme tokens.
2. Build trackers list page (default unread-first behavior).
3. Build add/edit forms with server-side validation.
4. Add details panel for chapter history.
5. Add settings page for polling and display preferences.

### Exit Criteria
- Core user flows are usable without API tooling.
- Works on desktop and mobile browser sizes.

## Phase 5 — Background Jobs + Notifications (Days 18-20)
### Deliverables
- Scheduler for periodic source refresh.
- Notification integration (start with ntfy-like provider).
- Failure reporting and retry states.

### Tasks
1. Add scheduler service with configurable interval.
2. Implement update diff logic for new chapter detection.
3. Trigger notifications on new releases by status rules.
4. Add UI surface for job errors/warnings.

### Exit Criteria
- New chapter event generates a notification.
- Job failures are visible and recoverable.

## Phase 6 — Multi-PC Access + Ops (Days 21-23)
### Deliverables
- Tailscale-enabled access pattern documented and tested.
- Backup/restore script and retention policy.
- Basic observability: logs + status endpoint.

### Tasks
1. Add Docker docs for LAN + Tailscale access.
2. Add DB backup script (scheduled) and restore workflow.
3. Add runtime diagnostics endpoint.

### Exit Criteria
- App is accessible from at least two different PCs.
- Backup and restore tested successfully.

## Phase 7 — Hardening + Release (Days 24-26)
### Deliverables
- End-to-end smoke tests.
- v0.1 release notes + runbook.
- Install script for near one-command setup.

### Tasks
1. Run API and UI smoke tests.
2. Fix high-severity defects only.
3. Document install, upgrade, rollback.
4. Tag v0.1 and produce release package.

### Exit Criteria
- Fresh install works from clean machine.
- Upgrade path is documented and tested once.

## Risk Register and Mitigations
- Source HTML changes break parsing -> Prefer APIs first, keep connector test fixtures, add fallback selectors.
- Rate limiting / temporary bans -> Backoff, jitter, source-specific intervals.
- Over-scoping frontend polish -> Keep MVP UX only; defer animations/themes.
- Data loss risk -> Automated backups + restore drill before release.

## Working Rules (Execution Discipline)
- Build one vertical slice at a time.
- Add tests for each core feature before moving phases.
- Keep changes minimal and reversible.
- Defer non-MVP features to post-v0.1 backlog.

## Post-v0.1 Backlog (Not in current scope)
- PWA offline cache.
- Browser extension quick-add.
- Plugin scripting (Lua) for advanced connectors.
- Multi-user auth/roles.
