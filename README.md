# Brain

A personal second brain agent written in Go. Captures thoughts from multiple sources (Discord, relay, web UI), classifies them using AI, and stores them in SQLite with semantic vector search via chromem-go.

## Ecosystem

Brain is part of a three-component system:

| Component | Location | Purpose |
|-----------|----------|---------|
| **brain.exe** (this repo) | `scripts/brain/` | Local brain — capture, classify, store, search |
| **ibeco.me** | `scripts/becoming/` (in scripture-study) | Cloud hub — relay, web UI, practices, journaling. Deployed via Dokploy on VPS |
| **brain-app** | `scripts/brain-app/` (separate git repo) | Flutter app — Android, Windows (iOS/Mac planned) |

brain.exe is the authoritative data store. ibeco.me connects via WebSocket relay and caches entries for web access. brain-app connects to either brain.exe directly or through the ibeco.me relay.

## Architecture

```
[Discord DM]  ──┐
[ibeco.me app] ──┤──→ [Go Brain Binary] ──→ [SQLite DB] + [chromem-go vectors]
[Web UI]       ──┘         ↑                       ↓ (optional)
                     [LM Studio / Copilot]    [Archive → private-brain git repo]
```

### Building Blocks (from Nate B Jones)

| Block | Implementation |
|-------|---------------|
| **Dropbox** (capture) | Discord DM, ibeco.me relay, web UI, CLI |
| **Sorter** (classifier) | LM Studio / Copilot SDK → structured JSON |
| **Form** (schema) | SQLite columns with category-specific fields |
| **Filing Cabinet** (storage) | SQLite + chromem-go vector embeddings |
| **Receipt** (audit trail) | `audit_log` table in SQLite |
| **Bouncer** (confidence filter) | Configurable threshold; low-confidence → inbox |
| **Tap on Shoulder** (surfacing) | Morning digest, weekly review (Phase 2) |
| **Fix Button** (correction) | `fix: <category>` in Discord, reclassify via web/API |
| **Search** (retrieval) | Full-text search + semantic vector search |

### Categories

| Category | For |
|----------|-----|
| `people` | Relationship context, follow-ups, details about someone |
| `projects` | Active work with status and next actions |
| `ideas` | Thoughts, insights, concepts to explore |
| `actions` | Tasks, errands, things with a "done" state |
| `study` | Scripture insights, spiritual impressions, gospel learning |
| `journal` | Personal reflections, observations, becoming |

## Setup

### Prerequisites

1. Go 1.21+
2. One of:
   - [LM Studio](https://lmstudio.ai/) running locally (default — free, private)
   - GitHub Copilot CLI installed and authenticated
3. Optional: A Discord bot token ([create one](https://discord.com/developers/applications))
4. Optional for semantic search: LM Studio with an embedding model, or Ollama with `nomic-embed-text`

### Configuration

```bash
cp .env.example .env
# Edit .env with your tokens and preferences
```

Key settings:
- `AI_BACKEND` — `lmstudio` (default) or `copilot`
- `EMBEDDING_BACKEND` — `lmstudio`, `ollama`, `openai`, or `none`
- `WEB_ENABLED` / `WEB_PORT` — web UI (default: `true` / `8445`)
- `BRAIN_DATA_DIR` — where SQLite + vectors live (default: `~/.brain-data`)
- `BRAIN_ARCHIVE_DIR` — optional private-brain repo for markdown export

### Build & Run

```bash
go build -o brain.exe ./cmd/brain
./brain.exe
```

Open http://localhost:8445 in your browser, or send a DM to your Discord bot.

### Commands (Discord)

| Command | Action |
|---------|--------|
| *(any text)* | Capture and classify a thought |
| `fix: <category>` | Reclassify the last entry |
| `model` | List available AI models |
| `model: <name>` | Switch AI model (gpt-mini, haiku, sonnet) |
| `status` | Show brain status and entry counts |
| `stop` | Pause autonomous processing |

### REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/entries` | GET | List entries (optional `?category=`) |
| `/api/entries` | POST | Create entry |
| `/api/entries/{id}` | GET | Get single entry |
| `/api/entries/{id}` | PUT | Update entry |
| `/api/entries/{id}` | DELETE | Delete entry |
| `/api/entries/{id}/reclassify` | POST | Move to new category |
| `/api/search?q=` | GET | Full-text search |
| `/api/search/semantic?q=` | GET | Semantic vector search |
| `/api/stats` | GET | Category counts |
| `/api/tags` | GET | Tag frequencies |
| `/api/archive` | POST | Export entry to markdown archive |

### AI Models

Powered by local LM Studio (default) or [GitHub Copilot SDK](https://github.com/github/copilot-sdk).

| Preset | Model | Premium Rate |
|--------|-------|--------------|
| `gpt-mini` (default) | gpt-5-mini | **0x** (free) |
| `haiku` | claude-haiku-4.5 | 0.33x |
| `flash` | gemini-3-flash | 0.33x |
| `sonnet` | claude-sonnet-4.6 | 1x |
| `gpt5` | gpt-5 | 1x |

Switch at runtime via Discord: `model: haiku`
Or set in .env: `AI_MODEL=sonnet`
Or use any Copilot model ID directly: `AI_MODEL=claude-opus-4.6`

## Phases

- [x] **Phase 1:** Core loop — capture → classify → store → git commit
- [x] **Phase A:** ibeco.me relay integration — WebSocket transport
- [x] **Phase B:** Relay client — brain.exe ↔ ibeco.me hub
- [x] **Phase C:** Flutter mobile app
- [x] **Phase D:** Integration tests
- [x] **Phase E:** CLI tool (brain-cli)
- [x] **Phase F:** SQLite + chromem-go — shared memory, web UI, semantic search
- [ ] **Phase 2:** Proactive surfacing — morning digest, weekly review
- [ ] **Phase 3:** Migration — import existing private-brain markdown
- [ ] **Phase 4:** Self-improvement — audit analysis, improvement proposals

## License

MIT
