package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/discord"
	"github.com/cpuchip/brain/internal/ibecome"
	"github.com/cpuchip/brain/internal/lmstudio"
	brainmcp "github.com/cpuchip/brain/internal/mcp"
	"github.com/cpuchip/brain/internal/relay"
	"github.com/cpuchip/brain/internal/store"
	"github.com/cpuchip/brain/internal/web"
	"github.com/joho/godotenv"
	"github.com/philippgille/chromem-go"
)

//go:embed all:dist
var frontendFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Check for subcommands
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(); err != nil {
			fmt.Fprintf(os.Stderr, "brain mcp: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "exec" {
		if err := runExec(); err != nil {
			fmt.Fprintf(os.Stderr, "brain exec: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	log.Printf("Brain starting...")
	log.Printf("  Data dir: %s", cfg.BrainDataDir)
	log.Printf("  Database: %s", cfg.DBPath)
	log.Printf("  AI backend: %s", cfg.AIBackend)
	if cfg.AIBackend == "copilot" {
		log.Printf("  Copilot model: %s", cfg.AIModel)
		if cfg.AIModelPreset != "" {
			log.Printf("  Preset: %s", cfg.AIModelPreset)
		}
	} else {
		log.Printf("  LM Studio: %s (model: %s)", cfg.LMStudioURL, cfg.LMStudioModel)
	}
	log.Printf("  Embedding: %s (model: %s)", cfg.EmbeddingBackend, cfg.EmbeddingModel)
	log.Printf("  Confidence threshold: %.0f%%", cfg.ConfidenceThreshold*100)
	log.Printf("  Relay: %v", cfg.RelayEnabled)
	log.Printf("  Discord: %v", cfg.DiscordEnabled)
	log.Printf("  Web UI: %v (port %s)", cfg.WebEnabled, cfg.WebPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize AI backend
	var completer ai.Completer
	var copilotClient *ai.Client

	switch cfg.AIBackend {
	case "copilot":
		copilotClient = ai.NewClient(cfg.AIModel, cfg.GitHubToken)
		log.Printf("Starting Copilot SDK...")
		if err := copilotClient.Start(ctx); err != nil {
			return fmt.Errorf("starting AI client: %w", err)
		}
		defer copilotClient.Stop()
		completer = copilotClient

	default: // "lmstudio"
		// Auto-start LM Studio server if not running
		if err := lmstudio.EnsureServer(ctx, cfg.LMStudioURL); err != nil {
			return fmt.Errorf("ensuring LM Studio server: %w", err)
		}

		// Auto-load the classification model
		if err := lmstudio.EnsureModel(ctx, cfg.LMStudioURL, cfg.LMStudioModel); err != nil {
			log.Printf("warning: could not auto-load classification model %q: %v", cfg.LMStudioModel, err)
		}

		lm := ai.NewLMStudioClient(cfg.LMStudioURL, cfg.LMStudioModel)
		log.Printf("LM Studio connected (%s, model: %s)", cfg.LMStudioURL, cfg.LMStudioModel)
		completer = lm
	}

	// Initialize embedding function (auto-loads model if needed)
	embedFunc := chooseEmbedder(ctx, cfg)

	// Initialize SQLite database
	db, err := store.OpenDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	count, _ := db.EntryCount()
	log.Printf("  Database entries: %d", count)

	// Initialize vector store
	vec, err := store.NewVecStore(cfg.VecDir, embedFunc, cfg.EmbeddingModel)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}
	if vec.Enabled() {
		log.Printf("  Vector store: enabled (%d documents)", vec.Count(ctx))
	} else {
		log.Printf("  Vector store: disabled (no embedding backend)")
	}

	// Initialize git for archive export (optional)
	var git *store.Git
	if cfg.ArchiveDir != "" {
		git = store.NewGit(
			cfg.ArchiveDir,
			"brain:",
			true,
			cfg.RateLimits.MaxGitCommitsPerDay,
		)
		if err := git.EnsureRepo(); err != nil {
			log.Printf("warning: archive repo not available: %v", err)
			git = nil
		} else {
			log.Printf("  Archive dir: %s", cfg.ArchiveDir)
		}
	}

	// Initialize store (wires together SQLite + vector + optional git)
	st := store.New(db, vec, git)

	// Initialize classifier
	classify := classifier.New(completer, cfg.ConfidenceThreshold)
	if p := classify.ActiveProfile(); p != nil {
		log.Printf("  Classification profile: %s (%s)", p.Name, p.ID)
	} else {
		log.Printf("  Classification profile: default (no profile for %s)", completer.Model())
	}

	// Initialize agent (Copilot SDK sessions with MCP tools)
	var agent *ai.Agent
	if copilotClient != nil && len(cfg.MCPServers) > 0 {
		mcpDefs := make(map[string]ai.MCPDef, len(cfg.MCPServers))
		for name, def := range cfg.MCPServers {
			mcpDefs[name] = ai.MCPDef{
				Command: def.Command,
				Args:    def.Args,
				Env:     def.Env,
				Cwd:     def.Cwd,
			}
		}
		// Compute workspace root for working directory
		var workingDir string
		if cfg.BrainCodeDir != "" {
			scriptsDir := filepath.Dir(cfg.BrainCodeDir)
			workspaceDir := filepath.Dir(scriptsDir)
			if _, err := os.Stat(filepath.Join(workspaceDir, "go.work")); err == nil {
				workingDir = workspaceDir
			} else {
				workingDir = cfg.BrainCodeDir
			}
		}

		agent = ai.NewAgent(copilotClient.CopilotClient(), ai.AgentConfig{
			Model: cfg.AgentModel,
			SystemMessage: `You are a development agent for the scripture-study project. You have access to:

1. SCRIPTURE TOOLS (MCP): gospel_search, gospel_get, gospel_list, search_scriptures, search_talks, webster_define — use these to look up scriptures, conference talks, and word definitions.

2. BUILT-IN FILE TOOLS: You can read, search, and edit files in the workspace.

When given a spec or task:
- Read and understand the relevant source code first
- Make precise, minimal changes that implement the spec
- After making changes, verify by reading the modified files
- Report what you changed and why

When answering scripture questions:
- Use the search tools to find relevant passages
- Always cite specific references
- Read the full source to verify quotes before citing them`,
			MCPServers: mcpDefs,
			WorkingDir: workingDir,
		})
		log.Printf("  Agent: enabled (model: %s, %d MCP servers)", cfg.AgentModel, len(mcpDefs))
	} else {
		log.Printf("  Agent: disabled (requires copilot backend + MCP servers)")
	}

	// Start web UI
	if cfg.WebEnabled {
		distFS, err := fs.Sub(frontendFS, "dist")
		if err != nil {
			log.Printf("warning: frontend not available: %v", err)
			distFS = nil
		}
		srv := web.NewServer(st, cfg, classify, agent, distFS)
		go func() {
			addr := ":" + cfg.WebPort
			log.Printf("Web UI starting on http://localhost%s", addr)
			if err := srv.ListenAndServe(addr); err != nil {
				log.Printf("warning: web server error: %v", err)
			}
		}()
	}

	// Start relay transport (WebSocket to ibeco.me)
	var relayClient *relay.Client
	if cfg.RelayEnabled {
		// Create ibecome client for task sync (uses same token as relay)
		var ibecomeClient *ibecome.Client
		if cfg.IbecomeTaskSync && cfg.RelayToken != "" && cfg.IbecomeURL != "" {
			ibecomeClient = ibecome.NewClient(cfg.IbecomeURL, cfg.RelayToken)
			log.Printf("  Task sync: enabled → %s", cfg.IbecomeURL)
		}

		relayClient = relay.NewClient(cfg.RelayURL, cfg.RelayToken, classify, st, ibecomeClient)
		go relayClient.Run(ctx)
		log.Printf("Relay transport started → %s", cfg.RelayURL)
	}

	// Start Discord transport (optional)
	var bot *discord.Bot
	if cfg.DiscordEnabled && cfg.DiscordToken != "" {
		var err error
		bot, err = discord.NewBot(
			cfg.DiscordToken,
			classify,
			st,
			cfg.RateLimits.MaxNotificationsPerDay,
		)
		if err != nil {
			return fmt.Errorf("creating Discord bot: %w", err)
		}

		if ownerID := os.Getenv("DISCORD_OWNER_ID"); ownerID != "" {
			bot.SetOwner(ownerID)
			log.Printf("  Discord owner: %s", ownerID)
		}

		if copilotClient != nil {
			bot.SetAIClient(copilotClient, cfg.AIModelPreset)
		}

		if err := bot.Start(); err != nil {
			return fmt.Errorf("starting Discord bot: %w", err)
		}
		defer bot.Stop()
		log.Printf("Discord transport started")
	}

	log.Printf("Brain is running.")
	if cfg.RelayEnabled {
		log.Printf("  Relay: connected to %s", cfg.RelayURL)
	}
	if cfg.DiscordEnabled {
		log.Printf("  Discord: listening for DMs")
	}
	if cfg.WebEnabled {
		log.Printf("  Web: http://localhost:%s", cfg.WebPort)
	}

	// Wait for interrupt
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("Shutting down...")
	cancel()

	if relayClient != nil {
		relayClient.Stop()
	}

	// Push archive if configured
	if git != nil {
		log.Printf("Pushing archive to origin...")
		if err := git.Push(); err != nil {
			log.Printf("warning: git push failed: %v", err)
		}
	}

	return nil
}

// chooseEmbedder selects an embedding function based on config.
// For LM Studio, it auto-loads the embedding model if not already loaded.
func chooseEmbedder(ctx context.Context, cfg *config.Config) chromem.EmbeddingFunc {
	switch cfg.EmbeddingBackend {
	case "lmstudio":
		if cfg.LMStudioURL == "" {
			return nil
		}
		// Auto-load the embedding model (different from classification model)
		if err := lmstudio.EnsureModel(ctx, cfg.LMStudioURL, cfg.EmbeddingModel); err != nil {
			log.Printf("warning: could not auto-load embedding model %q: %v", cfg.EmbeddingModel, err)
			log.Printf("Embedding will be disabled — semantic search unavailable")
			return nil
		}
		log.Printf("Embedding: LM Studio (%s, model: %s)", cfg.LMStudioURL, cfg.EmbeddingModel)
		return chromem.NewEmbeddingFuncOpenAICompat(
			cfg.LMStudioURL, "", cfg.EmbeddingModel, nil,
		)

	case "ollama":
		if cfg.OllamaURL == "" {
			return nil
		}
		log.Printf("Embedding: Ollama (%s, model: %s)", cfg.OllamaURL, cfg.EmbeddingModel)
		return chromem.NewEmbeddingFuncOllama(cfg.EmbeddingModel, cfg.OllamaURL)

	case "openai":
		if os.Getenv("OPENAI_API_KEY") == "" {
			log.Printf("warning: EMBEDDING_BACKEND=openai but OPENAI_API_KEY not set")
			return nil
		}
		log.Printf("Embedding: OpenAI (text-embedding-3-small)")
		return chromem.NewEmbeddingFuncDefault()

	default: // "none" or unrecognized
		log.Printf("Embedding: disabled")
		return nil
	}
}

// runMCP starts brain in MCP server mode (stdio transport, read-only).
// Only the database and vector store are needed — no AI backend, relay, or web UI.
func runMCP() error {
	// MCP server logs to stderr (stdout is the MCP protocol channel)
	log.SetFlags(0)
	log.SetOutput(os.Stderr)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log.Printf("brain mcp starting (data: %s)", cfg.BrainDataDir)

	ctx := context.Background()

	// Open SQLite (WAL mode allows concurrent reads with the daemon)
	db, err := store.OpenDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	// Initialize embedding for semantic search (optional, non-fatal)
	embedFunc := chooseEmbedder(ctx, cfg)

	var vec *store.VecStore
	if embedFunc != nil {
		vec, err = store.NewVecStore(cfg.VecDir, embedFunc, cfg.EmbeddingModel)
		if err != nil {
			log.Printf("warning: vector store unavailable: %v", err)
		}
	}

	st := store.New(db, vec, nil)

	srv := brainmcp.New(st)
	return srv.Serve()
}

// runExec handles "brain exec" — reads a spec (from file or stdin) and has the
// agent execute it. Prints the agent's response to stdout.
//
// Usage:
//
//	brain exec <spec-file>         # Read spec from file
//	brain exec --prompt "do X"     # Inline prompt
func runExec() error {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Parse args after "exec"
	args := os.Args[2:]
	var prompt string

	switch {
	case len(args) >= 2 && args[0] == "--prompt":
		prompt = strings.Join(args[1:], " ")
	case len(args) == 1:
		specPath := args[0]
		data, err := os.ReadFile(specPath)
		if err != nil {
			return fmt.Errorf("reading spec file: %w", err)
		}
		prompt = fmt.Sprintf("Execute this spec. Read the relevant source code, make the changes described, and report what you changed.\n\n---\n\n%s", string(data))
	default:
		return fmt.Errorf("usage: brain exec <spec-file> | brain exec --prompt \"...\"")
	}

	ctx := context.Background()

	// Start Copilot SDK
	copilotClient := ai.NewClient(cfg.AgentModel, cfg.GitHubToken)
	log.Printf("Starting Copilot SDK (model: %s)...", cfg.AgentModel)
	if err := copilotClient.Start(ctx); err != nil {
		return fmt.Errorf("starting Copilot SDK: %w", err)
	}
	defer copilotClient.Stop()

	// Build MCP server defs
	mcpDefs := make(map[string]ai.MCPDef, len(cfg.MCPServers))
	for name, def := range cfg.MCPServers {
		mcpDefs[name] = ai.MCPDef{
			Command: def.Command,
			Args:    def.Args,
			Env:     def.Env,
			Cwd:     def.Cwd,
		}
	}

	// Compute workspace root for working directory
	var workingDir string
	if cfg.BrainCodeDir != "" {
		scriptsDir := filepath.Dir(cfg.BrainCodeDir)
		workspaceDir := filepath.Dir(scriptsDir)
		if _, err := os.Stat(filepath.Join(workspaceDir, "go.work")); err == nil {
			workingDir = workspaceDir
		} else {
			workingDir = cfg.BrainCodeDir
		}
	}

	agent := ai.NewAgent(copilotClient.CopilotClient(), ai.AgentConfig{
		Model: cfg.AgentModel,
		SystemMessage: `You are a development agent for the scripture-study project. You have access to:

1. SCRIPTURE TOOLS (MCP): gospel_search, gospel_get, gospel_list, search_scriptures, search_talks, webster_define — use these to look up scriptures, conference talks, and word definitions.

2. BUILT-IN FILE TOOLS: You can read, search, and edit files in the workspace.

When given a spec or task:
- Read and understand the relevant source code first
- Make precise, minimal changes that implement the spec
- After making changes, verify by reading the modified files
- Report what you changed and why`,
		MCPServers: mcpDefs,
		WorkingDir: workingDir,
	})

	log.Printf("Agent ready (%d MCP servers, working dir: %s)", len(mcpDefs), workingDir)
	log.Printf("Sending spec to agent...")

	// Use a 10-minute timeout for spec execution — streaming shows progress
	execCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	response, err := agent.AskStreaming(execCtx, prompt, os.Stdout)
	if err != nil {
		return fmt.Errorf("agent execution: %w", err)
	}

	// Print final newline after streaming output
	fmt.Println()
	_ = response // full response was already streamed to stdout
	return nil
}
