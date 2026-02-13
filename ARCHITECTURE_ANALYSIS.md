# Mantium Architecture Analysis & Alternatives

## Overview
Mantium is a self-hosted cross-site manga tracker with the following tech stack:
- **Backend**: Go (Gin framework) with REST API
- **Frontend**: Python (Streamlit)
- **Database**: PostgreSQL
- **Deployment**: Docker Compose
- **Integrations**: Ntfy, Kaizoku, Tranga, Suwayomi

---

## 1. Hosting & Multi-PC Access

### Current Approach: Docker Compose
**How it works:**
- API runs on port 8080 (not accessible externally by default)
- Dashboard runs on port 8501
- PostgreSQL database in container
- Access from multiple PCs requires:
  - Host network mode OR
  - Reverse proxy (Nginx/Traefik)
  - Optional: Authentication proxy (Authelia/Authentik)

**Pros:**
✅ Simple deployment with one command (`docker compose up -d`)
✅ Isolated environment
✅ Easy backup (just volumes)
✅ Reproducible across machines

**Cons:**
❌ Requires Docker knowledge
❌ Network configuration needed for multi-PC
❌ Updates require container rebuild
❌ Resource overhead from containers

### Alternatives:

#### Option A: **Cloud Native (Kubernetes/Docker Swarm)**
**Better for:** Production deployments, auto-scaling, high availability
```yaml
Pros:
- Load balancing built-in
- Auto-scaling
- Service discovery
- Rolling updates
- Health checks

Cons:
- Much more complex
- Overkill for single-user
- Higher resource usage
- Steeper learning curve

Use case: If you plan to serve many users or need high availability
```

#### Option B: **Platform-as-a-Service (Railway, Render, Fly.io)**
**Better for:** Zero-config cloud hosting, easy access from anywhere
```yaml
Pros:
- No server management
- Automatic HTTPS
- Global CDN
- Easy scaling
- Built-in monitoring

Cons:
- Monthly costs ($5-20+)
- Less control
- Data privacy concerns
- Vendor lock-in

Use case: If you want "set and forget" hosting with global access
```

#### Option C: **Tailscale/ZeroTier VPN**
**Better for:** Private access across devices without exposing ports
```yaml
Pros:
- Secure encrypted mesh network
- No port forwarding needed
- Works behind NAT/firewalls
- Easy multi-device access
- Free for personal use

Cons:
- Requires client on each device
- Slight latency overhead

Use case: Best for your use case - private access from multiple PCs
```

#### Option D: **Cloudflare Tunnel**
**Better for:** Public/semi-public access without opening ports
```yaml
Pros:
- Free tier available
- No port forwarding
- Built-in DDoS protection
- HTTPS automatic

Cons:
- Requires Cloudflare account
- Privacy considerations
- Slower than local network

Use case: If you want to access from anywhere without VPN
```

**RECOMMENDATION FOR YOUR CASE:**
Keep Docker Compose + add **Tailscale** for easy multi-PC access. This gives you:
- Simple deployment
- Secure remote access
- No complex networking
- Free solution

---

## 2. Application Type: Web vs Desktop

### Current Approach: Web Application (Streamlit)
**How it works:**
- Python Streamlit dashboard served on port 8501
- Browser-based access
- Real-time updates every 5 seconds

**Pros:**
✅ Cross-platform (any device with browser)
✅ No installation on client machines
✅ Easy to update (server-side only)
✅ Responsive design for mobile

**Cons:**
❌ Requires server always running
❌ Internet/network dependency
❌ Browser resource usage
❌ Limited offline capability

### Alternatives:

#### Option A: **Desktop Application (Electron/Tauri)**
```yaml
Technology: Electron (Chromium-based) or Tauri (Rust-based)

Pros:
- Native application feel
- Offline capability
- System tray integration
- Better performance (Tauri)
- Local notifications
- File system access

Cons:
- Larger installation size (especially Electron ~100MB)
- Need to build for each OS (Windows, Mac, Linux)
- Updates more complex
- Heavier development effort

Use case: If offline access or system integration is important
```

#### Option B: **Progressive Web App (PWA)**
```yaml
Technology: Modern web app with service workers

Pros:
- Installable like native app
- Offline support
- Push notifications
- Smaller than Electron
- Single codebase
- Auto-updates

Cons:
- Limited compared to native
- Still needs occasional internet
- Browser dependent

Use case: Best middle ground - web benefits + offline support
```

#### Option C: **CLI Tool**
```yaml
Technology: Go CLI with TUI (Bubble Tea, tview)

Pros:
- Extremely lightweight
- Fast
- SSH-friendly
- Low resource usage
- Scriptable

Cons:
- Limited UI capabilities
- Not user-friendly for everyone
- No images/rich media

Use case: If you prefer terminal-based workflows
```

**RECOMMENDATION:**
Stick with **Web App** but consider adding **PWA features**:
- Installable icon
- Offline mode for reading cached data
- Push notifications
This gives native-like experience without desktop app complexity.

---

## 3. Database Choice

### Current Approach: PostgreSQL
**Why they chose it:**
- Robust ACID compliance
- Great for relational data (manga → chapters → multimangas)
- JSON support for flexible configs
- Excellent Go drivers

**Pros:**
✅ Reliable and battle-tested
✅ Complex queries support
✅ ACID transactions
✅ Good for relational data
✅ Full text search

**Cons:**
❌ Requires separate server
❌ More complex backup/restore
❌ Overkill for simple use cases
❌ Schema migrations needed

### Alternatives:

#### Option A: **SQLite (Embedded)**
```yaml
Better for: Single-user, embedded applications

Pros:
- Single file database
- No separate server
- Zero configuration
- Easy backup (just copy file)
- Lightweight
- Transactions supported
- Fast for read-heavy workloads

Cons:
- Not great for high concurrency
- Limited in-memory performance
- No network access (need API wrapper)

Migration effort: Easy - Go has excellent SQLite support
Use case: Perfect for single-user tracker
```

#### Option B: **MongoDB/Document DB**
```yaml
Better for: Flexible schema, rapid development

Pros:
- Schemaless (easier to add new source sites)
- JSON-native
- Horizontal scaling
- Flexible data models

Cons:
- No JOIN operations
- Larger storage footprint
- ACID only on single documents
- More complex queries

Use case: If adding many new sources frequently
```

#### Option C: **SurrealDB/EdgeDB**
```yaml
Better for: Modern graph-like relations + SQL

Pros:
- Graph capabilities with SQL
- Time-travel queries
- Multi-model (documents + relations)
- Real-time subscriptions
- Embedded or server mode

Cons:
- Newer technology (less mature)
- Smaller ecosystem
- Learning curve

Use case: If you want modern features while keeping SQL
```

#### Option D: **JSON Files + In-Memory Cache**
```yaml
Better for: Extremely simple deployments

Pros:
- No database server needed
- Human-readable storage
- Easy backup (git-friendly)
- Zero dependencies

Cons:
- Slow for large datasets
- No complex queries
- Manual indexing needed
- Concurrent access issues

Use case: MVP/prototype only
```

**RECOMMENDATION:**
**Use SQLite** instead of PostgreSQL for your use case because:
- Single-user application
- Simpler deployment (no DB container)
- Faster backup/restore
- Portable (single file)
- Still supports all relational features needed

Only keep PostgreSQL if you plan to:
- Serve 100+ users simultaneously
- Use advanced PG-specific features
- Horizontal scaling

---

## 4. Adding New Sites

### Current Approach: Source-Specific Scrapers
**Architecture:**
```
api/src/sources/
  ├── mangadex/      # One package per source
  ├── mangahub/
  ├── mangaplus/
  └── custom_manga/  # Generic scraper with CSS/XPath selectors
```

**How it works:**
1. Each source implements an interface
2. Source-specific logic for API calls or scraping
3. Custom manga allows user-defined CSS/XPath selectors
4. Selenium/headless browser support for JS-heavy sites

**Pros:**
✅ Type-safe source implementations
✅ Optimized per-source (use native APIs when available)
✅ Fallback to custom scraper for unsupported sites
✅ User can add any site via CSS selectors

**Cons:**
❌ Need code changes for new native sources
❌ Selectors break when sites change
❌ Maintenance burden

### Alternatives:

#### Option A: **Plugin System**
```yaml
Technology: Go plugins or WASM modules

Pros:
- Add sources without recompiling
- Community can contribute sources
- Version plugins independently
- Load/unload at runtime

Cons:
- More complex architecture
- Go plugin system limitations (CGO, OS-specific)
- Security concerns
- Debugging harder

Implementation:
- Use HashiCorp go-plugin
- Or compile to WASM
- Define source interface
```

#### Option B: **Lua/JavaScript Scripting**
```yaml
Technology: Embed Lua interpreter (gopher-lua) or JS (goja)

Examples: 
- Tachiyomi uses JSON config + JS
- Komga uses scripting for sources

Pros:
- Users add sources without Go knowledge
- Hot reload scripts
- Share scripts in community
- Inspect/modify easily

Cons:
- Performance overhead
- Sandboxing needed for security
- Another language to maintain

Implementation:
sources/
  scripts/
    mangadex.lua
    custom.lua
```

#### Option C: **Declarative Configuration**
```yaml
Technology: YAML/TOML source definitions

Example structure:
sources/
  mangadex.yaml:
    name: MangaDex
    base_url: https://api.mangadex.org
    manga_endpoint: /manga/{id}
    selectors:
      title: .manga-title
      chapters: .chapter-list > .chapter
```

**Pros:**
- Non-programmers can add sources
- Version control friendly
- Easy to share
- Type validation

**Cons:**
- Limited to simple scraping patterns
- Can't handle complex logic
- Authentication complex
- API interactions limited

#### Option D: **Hybrid Approach (Mantium's Current + Extensions)**
Keep native implementations for major sites, add scripting for others:

```yaml
Priority 1: Native Go (MangaDex, MangaPlus) - best performance
Priority 2: Generic scraper (current custom manga)
Priority 3: Community scripts (Lua/JS) - for niche sites
```

**RECOMMENDATION:**
**Enhance current approach with:**
1. **Declarative source configs** for simple sites (YAML)
2. Keep Go implementations for major sites
3. Add **Lua scripting** for complex custom sources
4. Version source configs separately from code

This gives flexibility without over-engineering.

---

## 5. Unified Database Concern

### Current Approach: Centralized PostgreSQL
All data in one database:
- `mangas` table
- `multimangas` table  
- `chapters` table
- `configs` table

**Pros:**
✅ Single source of truth
✅ ACID transactions across entities
✅ Relational integrity (foreign keys)
✅ One backup/restore point

**Cons:**
❌ Single point of failure
❌ Scaling bottleneck (though unlikely for this use case)

### Alternatives:

#### Option A: **Microservices with Separate DBs**
```yaml
Split into services:
- Manga Service → Postgres
- Notification Service → Redis
- User Prefs → SQLite
- Search → Elasticsearch

Pros:
- Scale independently
- Different DB per need
- Failure isolation

Cons:
- HUGE complexity increase
- Distributed transactions nightmare
- Overkill for 99.9% of use cases

Verdict: DON'T DO THIS for a personal tracker
```

#### Option B: **Multi-Database with Sync**
```yaml
Each device has local database, sync to central:
- CouchDB/PouchDB for automatic sync
- Local-first architecture

Pros:
- Offline capability
- Fast local access
- Resilient

Cons:
- Conflict resolution needed
- Sync complexity
- More storage needed

Use case: If offline access is critical
```

#### Option C: **Current Approach is PERFECT**
For a manga tracker:
- Single database is **the right choice**
- Data is naturally relational
- No need for distributed systems
- Backup/restore is simple

**RECOMMENDATION:**
**Keep the unified database**. It's the correct architecture for this use case.

Enhancements you CAN add:
- Automated backups
- Read replicas (if multiple users)
- Connection pooling (already in Go)

---

## 6. Ease of Running

### Current vs Alternatives Comparison:

| Aspect | Current (Docker Compose) | Better Alternative |
|--------|-------------------------|-------------------|
| **Initial Setup** | `git clone` → `.env` → `docker compose up` | Same OR use install script |
| **Updates** | `docker compose pull && up -d` | Could add auto-update checker |
| **Backups** | Manual volume copy | Add automated backups to cloud |
| **Monitoring** | External tools needed | Add health dashboard |
| **Configuration** | .env file | Keep + add web UI for configs |

### Recommended Improvements:

#### 1. **One-Command Install**
```bash
# install.sh
curl -fsSL https://raw.githubusercontent.com/you/project/main/install.sh | bash

# This script:
# - Installs Docker if missing
# - Clones repo
# - Generates secure .env
# - Starts services
# - Prints access URL
```

#### 2. **Auto-Update System**
```go
// In API
func CheckForUpdates() {
    latestVersion := fetchFromGitHub()
    if latestVersion > currentVersion {
        notifyUser("Update available!")
        // Optional: auto-update if enabled
    }
}
```

#### 3. **Health Check Dashboard**
Add to API a `/health` endpoint showing:
- Database status
- Disk space
- Last successful manga update
- Integration statuses

#### 4. **Configuration Web UI**
Instead of editing `.env`, add a settings page in dashboard for:
- Notification settings
- Update frequency
- Integration configs
- Advanced options

---

## 7. Overall Architecture Recommendations

### For Your Cross-Site Tracker Project:

#### Recommended Stack:
```yaml
Backend: 
  Language: Go (excellent choice - fast, compiled, easy deployment)
  Framework: Gin (current) or Fiber (faster, Express-like)
  
Database:
  Primary: SQLite (simpler than PostgreSQL for single-user)
  Cache: In-memory (Go maps with sync) or Redis if needed
  
Frontend:
  Framework: 
    Option A: Keep Streamlit (fastest development)
    Option B: SvelteKit (modern, fast, better UX)
    Option C: HTMX + Tailwind (minimal JS, server-rendered)
  
Deployment:
  Local: Docker Compose (current is good)
  Access: Add Tailscale for secure multi-PC access
  
Features to Add:
  - PWA support (installable, offline-capable)
  - Automatic backups to cloud storage
  - Browser extension for quick "add to tracker"
  - Mobile-responsive design (Mantium has this)
```

#### Architecture Diagram:
```
┌─────────────────────────────────────────────┐
│     Access Layer (Choose One or Both)       │
│  • Tailscale VPN (private access)          │
│  • Cloudflare Tunnel (public access)       │
└─────────────────┬───────────────────────────┘
                  │
        ┌─────────▼──────────┐
        │  Reverse Proxy     │  Optional: Caddy with auto-HTTPS
        │  (Caddy/Traefik)   │
        └─────────┬──────────┘
                  │
        ╔═════════▼══════════╗
        ║    Docker Compose  ║
        ╠════════════════════╣
        ║                    ║
        ║  ┌──────────────┐ ║
        ║  │  Frontend    │ ║  Port 8501
        ║  │  (Streamlit  │ ║  Or SvelteKit
        ║  │   or other)  │ ║
        ║  └───────┬──────┘ ║
        ║          │         ║
        ║  ┌───────▼──────┐ ║
        ║  │  API Server  │ ║  Port 8080
        ║  │     (Go)     │ ║  
        ║  └───────┬──────┘ ║
        ║          │         ║
        ║  ┌───────▼──────┐ ║
        ║  │   SQLite DB  │ ║  Volume mount
        ║  │ (or PostGres)│ ║
        ║  └──────────────┘ ║
        ║                    ║
        ╚════════════════════╝

Optional Integrations:
  ├─ Ntfy (notifications)
  ├─ Browser extension (quick add)
  └─ Cloud backup (B2, S3)
```

---

## 8. Summary: What to Change from Mantium

### Keep As-Is ✅
1. **Go backend** - excellent choice
2. **Docker Compose deployment** - simple and effective
3. **REST API architecture** - clean separation
4. **Multi-source support** - core feature
5. **Background jobs** - necessary for updates

### Consider Changing 🤔
1. **PostgreSQL → SQLite** - simpler for single-user
2. **Streamlit → Modern framework** - better UX (if time permits)
3. **Add Tailscale** - easier multi-PC access
4. **Add PWA features** - installable, offline-capable
5. **Plugin system for sources** - easier community contributions
6. **Automated backups** - protect user data

### Prioritized Recommendations:

#### Phase 1 (MVP): Keep It Simple
- ✅ Go + Gin backend
- ✅ SQLite database
- ✅ Simple web UI (can start with Streamlit or templates)
- ✅ Docker Compose
- ✅ Basic scrapers for top sources

#### Phase 2 (Usability):
- ➕ Tailscale integration
- ➕ PWA features
- ➕ Automated backups
- ➕ One-command installer

#### Phase 3 (Extensibility):
- ➕ Plugin system for sources
- ➕ Browser extension
- ➕ Advanced search/filters
- ➕ Analytics/statistics

---

## 9. Code Quality Observations from Mantium

### Good Practices They Follow:
✅ Clean package structure
✅ Database migrations system
✅ Comprehensive error handling
✅ API documentation (Swagger)
✅ Environment-based configuration
✅ Docker multi-stage builds
✅ Testing infrastructure

### Could Be Improved:
⚠️ Tight coupling between API and integrations
⚠️ Dashboard directly calls API (could use SDK)
⚠️ Limited test coverage
⚠️ No CI/CD pipeline visible
⚠️ Source site changes break scrapers (inherent issue)

### Suggestions for Your Project:
1. **Add dependency injection** - easier testing
2. **Use repository pattern** - abstract database layer
3. **Add GraphQL option** - more efficient than REST for complex queries
4. **Implement rate limiting** - protect against scraping bans
5. **Add telemetry** - understand usage patterns
6. **Structured logging** - easier debugging (they use zerolog ✅)

---

## Final Verdict: Should You Use Similar Architecture?

### ✅ YES, use similar approach for:
- Docker Compose deployment
- Go backend (fast, reliable, single binary)
- Centralized database
- Background job system
- Multi-source architecture

### 🔄 MODIFY these aspects:
- Use **SQLite** instead of PostgreSQL (simpler)
- Add **Tailscale** for multi-PC access (easier than port forwarding)
- Consider **SvelteKit or HTMX** instead of Streamlit (better UX)
- Add **Lua scripting** for extensible sources
- Implement **PWA features** for better mobile/desktop experience

### ❌ DON'T overcomplicate with:
- Microservices (overkill)
- Kubernetes (unnecessary)
- Separate databases per entity
- Complex authentication (unless multi-user)

---

## Quick Start Recommendation

For your cross-site tracker, I'd recommend this stack:

```plaintext
LANGUAGE:  Go 1.21+
FRAMEWORK: Fiber or Gin
DATABASE:  SQLite with GORM
FRONTEND:  HTMX + Tailwind (or SvelteKit if you want a SPA)
DEPLOY:    Docker Compose + Tailscale
SOURCES:   Native Go scrapers + YAML configs for simple sites
```

This gives you:
- Fast development (Go is productive)
- Simple deployment (single binary + SQLite file)
- Easy multi-PC access (Tailscale)
- Low maintenance (no complex infrastructure)
- Extensible (can add features incrementally)

Want me to create a starter template with this stack?
