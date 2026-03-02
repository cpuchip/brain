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
	log.Printf("  AI model: %s", cfg.AIModel)
	if cfg.AIModelPreset != "" {
		log.Printf("  Preset: %s", cfg.AIModelPreset)
	}
	log.Printf("  Confidence threshold: %.0f%%", cfg.ConfidenceThreshold*100)
	log.Printf("  Relay: %v", cfg.RelayEnabled)
	log.Printf("  Discord: %v", cfg.DiscordEnabled)

	// Initialize git manager
	git := store.NewGit(
		cfg.BrainDataDir,
		"brain:",
		true, // auto-commit
		cfg.RateLimits.MaxGitCommitsPerDay,
	)

	if err := git.EnsureRepo(); err != nil {
		return fmt.Errorf("git check: %w", err)
	}

	// Pull latest
	log.Printf("Pulling latest from origin...")
	if err := git.Pull(); err != nil {
		log.Printf("warning: git pull failed (may be empty repo): %v", err)
	}

	// Initialize AI client (Copilot SDK)
	aiClient := ai.NewClient(cfg.AIModel, cfg.GitHubToken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("Starting Copilot SDK...")
	if err := aiClient.Start(ctx); err != nil {
		return fmt.Errorf("starting AI client: %w", err)
	}
	defer aiClient.Stop()

	// Initialize classifier
	classify := classifier.New(aiClient, cfg.ConfidenceThreshold)

	// Initialize store
	st := store.New(cfg.BrainDataDir, git)

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

		bot.SetAIClient(aiClient, cfg.AIModelPreset)

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

	// Wait for interrupt
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("Shutting down...")
	cancel() // Signal relay client to stop

	if relayClient != nil {
		relayClient.Stop()
	}

	// Push any uncommitted changes before exit
	log.Printf("Pushing to origin...")
	if err := git.Push(); err != nil {
		log.Printf("warning: git push failed: %v", err)
	}

	return nil
}
