# Brain

A personal second brain agent written in Go. Captures thoughts via Discord DM, classifies them using AI (GitHub Models API), and stores them as markdown files with YAML front matter in a private Git repository.

## Architecture

```
[Discord DM] → [Go Brain Binary] → [Private Git Repo]
                     ↑                 (markdown/YAML)
               [GitHub Models API]
               (classification AI)
```

### Building Blocks (from Nate B Jones)

| Block | Implementation |
|-------|---------------|
| **Dropbox** (capture) | Discord DM to the bot |
| **Sorter** (classifier) | GitHub Models API → structured JSON |
| **Form** (schema) | YAML front matter with category-specific fields |
| **Filing Cabinet** (storage) | Private GitHub repo with markdown files |
| **Receipt** (audit trail) | `.brain/audit-log/` YAML files |
| **Bouncer** (confidence filter) | Configurable threshold; low-confidence → inbox |
| **Tap on Shoulder** (surfacing) | Morning digest, weekly review (Phase 2) |
| **Fix Button** (correction) | `fix: <category>` command in Discord |

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
2. A GitHub PAT with `models` scope ([create one](https://github.com/settings/tokens))
3. A Discord bot token ([create one](https://discord.com/developers/applications))
4. The [private-brain](https://github.com/cpuchip/private-brain) repo cloned locally

### Discord Bot Setup

1. Go to [Discord Developer Portal](https://discord.com/developers/applications)
2. Create a new application (e.g., "Brain")
3. Go to Bot → create a bot
4. Enable **Message Content Intent** under Privileged Gateway Intents
5. Copy the bot token
6. Go to OAuth2 → URL Generator:
   - Scopes: `bot`
   - Bot Permissions: `Send Messages`, `Read Message History`, `Add Reactions`
7. Open the generated URL to invite the bot to your server (or just DM it directly)

### Configuration

```bash
cp .env.example .env
# Edit .env with your tokens
```

### Build & Run

```bash
go build -o brain.exe ./cmd/brain
./brain.exe
```

Then send a DM to your bot on Discord!

### Commands

| Command | Action |
|---------|--------|
| *(any text)* | Capture and classify a thought |
| `fix: <category>` | Reclassify the last entry |
| `status` | Show brain status and entry counts |
| `stop` | Pause autonomous processing |

## Phases

- [x] **Phase 1:** Core loop — Discord capture → classify → file → git commit
- [ ] **Phase 2:** Proactive surfacing — morning digest, weekly review
- [ ] **Phase 3:** ibeco.me integration — WebSocket chat, mobile capture
- [ ] **Phase 4:** Self-improvement — audit analysis, improvement proposals
- [ ] **Phase 5:** Scripture study sync — gospel library on VPS

## License

MIT
