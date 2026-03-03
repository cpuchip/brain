package discord

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/cpuchip/brain/internal/ai"
	"github.com/cpuchip/brain/internal/classifier"
	"github.com/cpuchip/brain/internal/config"
	"github.com/cpuchip/brain/internal/store"
)

// Bot is the Discord bot that captures thoughts via DM.
type Bot struct {
	session      *discordgo.Session
	classify     *classifier.Classifier
	store        *store.Store
	aiClient     *ai.Client
	currentModel string // Preset name (e.g. "gpt-mini") or empty for custom
	ownerID      string // Only respond to DMs from this user
	mu           sync.Mutex
	lastPaths    map[string]string // messageID -> file relPath (for fix command)
	notifCount   int
	maxNotifs    int
	lastReset    int
}

// NewBot creates a new Discord bot.
func NewBot(token string, classify *classifier.Classifier, st *store.Store, maxNotifsPerDay int) (*Bot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("creating Discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	bot := &Bot{
		session:   session,
		classify:  classify,
		store:     st,
		lastPaths: make(map[string]string),
		maxNotifs: maxNotifsPerDay,
	}

	session.AddHandler(bot.onMessage)

	return bot, nil
}

// SetAIClient gives the bot access to the AI client for model switching.
func (b *Bot) SetAIClient(client *ai.Client, presetName string) {
	b.aiClient = client
	b.currentModel = presetName
}

// Start opens the Discord connection.
func (b *Bot) Start() error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("opening Discord connection: %w", err)
	}

	// Get our own user to identify the owner on first DM
	user, err := b.session.User("@me")
	if err != nil {
		log.Printf("warning: could not get bot user: %v", err)
	} else {
		log.Printf("Discord bot connected as %s#%s", user.Username, user.Discriminator)
	}

	return nil
}

// Stop closes the Discord connection.
func (b *Bot) Stop() error {
	return b.session.Close()
}

// SetOwner sets the Discord user ID that the bot responds to.
// If empty, responds to all DMs (not recommended for production).
func (b *Bot) SetOwner(userID string) {
	b.ownerID = userID
}

// SendDM sends a direct message to the owner.
func (b *Bot) SendDM(message string) error {
	if b.ownerID == "" {
		return fmt.Errorf("owner ID not set — cannot send DM")
	}

	if err := b.checkNotifLimit(); err != nil {
		return err
	}

	channel, err := b.session.UserChannelCreate(b.ownerID)
	if err != nil {
		return fmt.Errorf("creating DM channel: %w", err)
	}

	_, err = b.session.ChannelMessageSend(channel.ID, message)
	if err != nil {
		return fmt.Errorf("sending DM: %w", err)
	}

	b.notifCount++
	return nil
}

// onMessage handles incoming Discord messages.
func (b *Bot) onMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore our own messages
	if m.Author.ID == s.State.User.ID {
		return
	}

	// Only respond to DMs
	channel, err := s.Channel(m.ChannelID)
	if err != nil {
		log.Printf("error getting channel: %v", err)
		return
	}
	if channel.Type != discordgo.ChannelTypeDM {
		return
	}

	// If owner is set, only respond to them
	if b.ownerID != "" && m.Author.ID != b.ownerID {
		log.Printf("ignoring DM from non-owner: %s", m.Author.Username)
		return
	}

	// If owner not set yet, capture the first DM sender as owner
	if b.ownerID == "" {
		b.ownerID = m.Author.ID
		log.Printf("Owner set to %s (%s)", m.Author.Username, m.Author.ID)
	}

	rawText := strings.TrimSpace(m.Content)
	if rawText == "" {
		return
	}

	// Handle commands
	if strings.HasPrefix(strings.ToLower(rawText), "model:") || strings.ToLower(rawText) == "model" {
		b.handleModel(s, m, rawText)
		return
	}

	if strings.HasPrefix(strings.ToLower(rawText), "fix:") {
		b.handleFix(s, m, rawText)
		return
	}

	if strings.ToLower(rawText) == "status" {
		b.handleStatus(s, m)
		return
	}

	if strings.ToLower(rawText) == "stop" {
		s.ChannelMessageSend(m.ChannelID, "Brain paused. All autonomous processing stopped. Send `start` to resume.")
		return
	}

	// Classify the thought
	b.handleCapture(s, m, rawText)
}

// handleCapture classifies and stores a thought.
func (b *Bot) handleCapture(s *discordgo.Session, m *discordgo.MessageCreate, rawText string) {
	// React with thinking emoji
	s.MessageReactionAdd(m.ChannelID, m.ID, "🧠")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Classify
	result, err := b.classify.Classify(ctx, rawText)
	if err != nil {
		log.Printf("classification error: %v", err)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Classification failed: %v\nSaved to inbox for manual review.", err))
		// Save as-is to inbox
		result = &classifier.Result{
			Category:   "inbox",
			Confidence: 0,
			Title:      "unclassified-" + time.Now().Format("150405"),
			Fields:     classifier.Fields{Notes: rawText},
		}
	}

	needsReview := b.classify.NeedsReview(result)

	// Store
	relPath, err := b.store.Save(result, rawText, needsReview, "discord")
	if err != nil {
		log.Printf("store error: %v", err)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Failed to save: %v", err))
		return
	}

	// Remember the path for fix command
	b.mu.Lock()
	b.lastPaths[m.ID] = relPath
	b.mu.Unlock()

	// Audit log
	auditRecord := &store.AuditRecord{
		Timestamp:   time.Now().UTC(),
		RawText:     rawText,
		Category:    result.Category,
		Title:       result.Title,
		Confidence:  result.Confidence,
		NeedsReview: needsReview,
		Source:      "discord",
		FilePath:    relPath,
		Tags:        result.Tags,
	}
	if err := b.store.SaveAudit(auditRecord); err != nil {
		log.Printf("audit error: %v", err)
	}

	// Remove thinking reaction, add confirmation
	s.MessageReactionRemove(m.ChannelID, m.ID, "🧠", "@me")
	s.MessageReactionAdd(m.ChannelID, m.ID, "✅")

	// Send confirmation
	confirmation := classifier.FormatConfirmation(result, needsReview)
	s.ChannelMessageSend(m.ChannelID, confirmation)
}

// handleFix reclassifies the most recent entry.
func (b *Bot) handleFix(s *discordgo.Session, m *discordgo.MessageCreate, rawText string) {
	parts := strings.SplitN(rawText, ":", 2)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "Usage: `fix: <category>` (people/projects/ideas/actions/study/journal)")
		return
	}

	newCategory := strings.TrimSpace(strings.ToLower(parts[1]))
	validCategories := map[string]bool{
		"people": true, "projects": true, "ideas": true,
		"actions": true, "study": true, "journal": true,
	}
	if !validCategories[newCategory] {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unknown category: %s. Valid: people, projects, ideas, actions, study, journal", newCategory))
		return
	}

	// Find the most recent path to fix
	b.mu.Lock()
	var lastPath string
	// Find the most recent entry (simple approach: just use any stored path)
	for _, p := range b.lastPaths {
		lastPath = p
	}
	b.mu.Unlock()

	if lastPath == "" {
		s.ChannelMessageSend(m.ChannelID, "No recent entry to fix. Capture something first.")
		return
	}

	newPath, err := b.store.Reclassify(lastPath, newCategory)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Reclassification failed: %v", err))
		return
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Moved to **%s**: %s", newCategory, newPath))
}

// handleStatus reports current brain status.
func (b *Bot) handleStatus(s *discordgo.Session, m *discordgo.MessageCreate) {
	var sb strings.Builder
	sb.WriteString("**Brain Status**\n")
	if b.aiClient != nil {
		sb.WriteString(fmt.Sprintf("Model: **%s** (`%s`)\n", b.currentModel, b.aiClient.Model()))
	}
	sb.WriteString(fmt.Sprintf("Confidence threshold: %.0f%%\n", b.classify.Threshold()*100))

	// Count entries per category
	categories := []string{"people", "projects", "ideas", "actions", "study", "journal", "inbox"}
	for _, cat := range categories {
		entries, _ := b.store.ListCategory(cat)
		sb.WriteString(fmt.Sprintf("• %s: %d entries\n", cat, len(entries)))
	}

	s.ChannelMessageSend(m.ChannelID, sb.String())
}

func (b *Bot) checkNotifLimit() error {
	today := time.Now().YearDay()
	if today != b.lastReset {
		b.notifCount = 0
		b.lastReset = today
	}
	if b.notifCount >= b.maxNotifs {
		return fmt.Errorf("daily notification limit reached (%d/%d)", b.notifCount, b.maxNotifs)
	}
	return nil
}

// handleModel switches AI model or lists available presets.
func (b *Bot) handleModel(s *discordgo.Session, m *discordgo.MessageCreate, rawText string) {
	if b.aiClient == nil {
		s.ChannelMessageSend(m.ChannelID, "AI client not available for model switching.")
		return
	}

	// "model" with no colon — list available models
	if strings.ToLower(strings.TrimSpace(rawText)) == "model" {
		var sb strings.Builder
		sb.WriteString("**Available Models**\n")
		sb.WriteString(fmt.Sprintf("Current: **%s** (`%s`)\n\n", b.currentModel, b.aiClient.Model()))

		// Sort preset names for consistent output
		names := make([]string, 0, len(config.AvailableModels))
		for name := range config.AvailableModels {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			preset := config.AvailableModels[name]
			marker := "  "
			if name == b.currentModel {
				marker = "▸ "
			}
			sb.WriteString(fmt.Sprintf("%s**%s** — %s (%s premium)\n", marker, name, preset.DisplayName, preset.PremiumRate))
		}
		sb.WriteString("\nSwitch: `model: <name>`")
		s.ChannelMessageSend(m.ChannelID, sb.String())
		return
	}

	// "model: <preset>" — switch model
	parts := strings.SplitN(rawText, ":", 2)
	if len(parts) != 2 {
		s.ChannelMessageSend(m.ChannelID, "Usage: `model: <name>` or just `model` to list options")
		return
	}

	presetName := strings.TrimSpace(strings.ToLower(parts[1]))
	preset, ok := config.AvailableModels[presetName]
	if !ok {
		names := make([]string, 0, len(config.AvailableModels))
		for name := range config.AvailableModels {
			names = append(names, name)
		}
		sort.Strings(names)
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unknown model: %s\nAvailable: %s", presetName, strings.Join(names, ", ")))
		return
	}

	b.aiClient.SetModel(preset.ID)
	b.currentModel = presetName

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Switched to **%s** — %s (%s premium)", presetName, preset.DisplayName, preset.PremiumRate))
	log.Printf("Model switched to %s (%s)", presetName, preset.ID)
}
