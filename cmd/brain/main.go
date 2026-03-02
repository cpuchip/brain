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

	ctx := context.Background()
	log.Printf("Starting Copilot SDK...")
	if err := aiClient.Start(ctx); err != nil {
		return fmt.Errorf("starting AI client: %w", err)
	}
	defer aiClient.Stop()

	// Initialize classifier
	classify := classifier.New(aiClient, cfg.ConfidenceThreshold)

	// Initialize store
	st := store.New(cfg.BrainDataDir, git)

	// Initialize Discord bot
	bot, err := discord.NewBot(
		cfg.DiscordToken,
		classify,
		st,
		cfg.RateLimits.MaxNotificationsPerDay,
	)
	if err != nil {
		return fmt.Errorf("creating Discord bot: %w", err)
	}

	// Set owner if configured
	if ownerID := os.Getenv("DISCORD_OWNER_ID"); ownerID != "" {
		bot.SetOwner(ownerID)
		log.Printf("  Owner: %s", ownerID)
	} else {
		log.Printf("  Owner: (will be set on first DM)")
	}

	// Give bot access to AI client for model switching
	bot.SetAIClient(aiClient, cfg.AIModelPreset)

	// Start Discord bot
	if err := bot.Start(); err != nil {
		return fmt.Errorf("starting Discord bot: %w", err)
	}
	defer bot.Stop()

	log.Printf("Brain is running. Send me a DM on Discord!")
	log.Printf("Commands: 'status', 'fix: <category>', 'model', 'model: <name>', 'stop'")

	// Wait for interrupt
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("Shutting down...")

	// Push any uncommitted changes before exit
	log.Printf("Pushing to origin...")
	if err := git.Push(); err != nil {
		log.Printf("warning: git push failed: %v", err)
	}

	return nil
}
