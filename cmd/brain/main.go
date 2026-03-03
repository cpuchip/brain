package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/discord"
	"github.com/cpuchip/brain/internal/relay"
	"github.com/cpuchip/brain/internal/store"
	"github.com/cpuchip/brain/internal/web"
	"github.com/philippgille/chromem-go"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

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
		lm := ai.NewLMStudioClient(cfg.LMStudioURL, cfg.LMStudioModel)
		log.Printf("Checking LM Studio connectivity...")
		if err := lm.Ping(ctx); err != nil {
			return fmt.Errorf("LM Studio not reachable: %w", err)
		}
		log.Printf("LM Studio connected (%s)", cfg.LMStudioURL)
		completer = lm
	}

	// Initialize embedding function
	embedFunc := chooseEmbedder(cfg)

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

	// Start web UI
	if cfg.WebEnabled {
		srv := web.NewServer(st, cfg)
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
		relayClient = relay.NewClient(cfg.RelayURL, cfg.RelayToken, classify, st)
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
func chooseEmbedder(cfg *config.Config) chromem.EmbeddingFunc {
	switch cfg.EmbeddingBackend {
	case "lmstudio":
		if cfg.LMStudioURL == "" {
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
