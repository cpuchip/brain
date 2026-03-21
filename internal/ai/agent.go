package ai

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// AgentConfig configures an agent session with MCP tool access.
type AgentConfig struct {
	Model         string            // Model for agent reasoning (e.g. "claude-sonnet-4.6")
	SystemMessage string            // System prompt for the agent
	MCPServers    map[string]MCPDef // External MCP servers to connect
	WorkingDir    string            // Working directory for file operations

	// Workspace-aware fields
	SkillDirectories []string // Directories to load skills from (e.g. .github/skills/)
	InfiniteSessions bool     // Enable context compaction for long sessions
}

// MCPDef describes an MCP server that should be available to agent sessions.
type MCPDef struct {
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
}

// Agent manages Copilot SDK sessions that have MCP tools available.
// Unlike the classifier session (stateless, reused), agent sessions
// are conversational and tool-enabled.
type Agent struct {
	client  *copilot.Client
	config  AgentConfig
	mu      sync.Mutex
	session *copilot.Session
	started bool
}

// NewAgent creates an agent backed by the given Copilot client.
func NewAgent(client *copilot.Client, cfg AgentConfig) *Agent {
	return &Agent{
		client: client,
		config: cfg,
	}
}

// Ask sends a prompt to the agent session. The agent can use MCP tools
// (gospel-mcp, etc.) to look up information before responding.
// Creates a new session on first call; reuses it for subsequent calls.
func (a *Agent) Ask(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()

	if session == nil {
		var err error
		session, err = a.createSession(ctx)
		if err != nil {
			return "", fmt.Errorf("creating agent session: %w", err)
		}
		a.mu.Lock()
		a.session = session
		a.mu.Unlock()
	}

	response, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		// Session may be broken — destroy so next call creates fresh
		a.mu.Lock()
		if a.session == session {
			a.session.Destroy()
			a.session = nil
		}
		a.mu.Unlock()
		return "", fmt.Errorf("agent send: %w", err)
	}

	if response == nil || response.Data.Content == nil || *response.Data.Content == "" {
		return "", fmt.Errorf("empty response from agent (model=%s)", a.config.Model)
	}

	return *response.Data.Content, nil
}

// AskStreaming sends a prompt and streams the response to w as it arrives.
// Tool calls are logged. Blocks until session is idle or context is cancelled.
// Returns the final assembled response text.
func (a *Agent) AskStreaming(ctx context.Context, prompt string, w io.Writer) (string, error) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()

	if session == nil {
		var err error
		session, err = a.createSession(ctx)
		if err != nil {
			return "", fmt.Errorf("creating agent session: %w", err)
		}
		a.mu.Lock()
		a.session = session
		a.mu.Unlock()
	}

	idleCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	var fullResponse string
	var mu sync.Mutex
	var streamedChars int64

	// Track last event time for inactivity watchdog
	lastEvent := time.Now()
	var lastEventMu sync.Mutex

	touchEvent := func() {
		lastEventMu.Lock()
		lastEvent = time.Now()
		lastEventMu.Unlock()
	}

	unsubscribe := session.On(func(event copilot.SessionEvent) {
		touchEvent()

		switch event.Type {
		case copilot.AssistantMessageDelta, copilot.AssistantStreamingDelta:
			// Streaming deltas use DeltaContent; message deltas may use Content
			delta := event.Data.DeltaContent
			if delta == nil {
				delta = event.Data.Content
			}
			if delta != nil {
				n, _ := fmt.Fprint(w, *delta)
				mu.Lock()
				streamedChars += int64(n)
				mu.Unlock()
			}
		case copilot.AssistantMessage:
			if event.Data.Content != nil {
				mu.Lock()
				fullResponse = *event.Data.Content
				chars := streamedChars
				mu.Unlock()
				log.Printf("Response complete (%d chars streamed, %d chars final)", chars, len(*event.Data.Content))
			}
		case copilot.ToolExecutionStart:
			if event.Data.ToolName != nil {
				log.Printf("Tool start: %s", *event.Data.ToolName)
			}
		case copilot.SessionIdle:
			log.Printf("Session idle")
			select {
			case idleCh <- struct{}{}:
			default:
			}
		case copilot.SessionError:
			errMsg := "session error"
			if event.Data.Message != nil {
				errMsg = *event.Data.Message
			}
			log.Printf("Session error: %s", errMsg)
			select {
			case errCh <- fmt.Errorf("session error: %s", errMsg):
			default:
			}

		// Noisy events we expect but don't need to log
		case copilot.AssistantReasoningDelta, copilot.AssistantReasoning,
			copilot.HookStart, copilot.HookEnd,
			copilot.ToolExecutionComplete,
			copilot.AssistantTurnStart, copilot.AssistantTurnEnd:
			// ignore

		default:
			// Log truly unexpected events
			log.Printf("Event: %s", event.Type)
		}
	})
	defer unsubscribe()

	// Inactivity watchdog — warns if no events for 30s
	watchdogCtx, cancelWatchdog := context.WithCancel(ctx)
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				lastEventMu.Lock()
				idle := time.Since(lastEvent)
				lastEventMu.Unlock()
				if idle >= 30*time.Second {
					mu.Lock()
					chars := streamedChars
					mu.Unlock()
					log.Printf("WARNING: no events for %s (streamed %d chars so far)", idle.Round(time.Second), chars)
				}
			case <-watchdogCtx.Done():
				return
			}
		}
	}()

	_, err := session.Send(ctx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		cancelWatchdog()
		return "", fmt.Errorf("agent send: %w", err)
	}

	var result string
	select {
	case <-idleCh:
		mu.Lock()
		result = fullResponse
		chars := streamedChars
		mu.Unlock()
		// If nothing was streamed but we have a final response, write it now
		if chars == 0 && result != "" {
			log.Printf("No streaming deltas received; writing final response (%d chars)", len(result))
			fmt.Fprint(w, result)
		}
	case err := <-errCh:
		cancelWatchdog()
		<-watchdogDone
		return "", err
	case <-ctx.Done():
		cancelWatchdog()
		<-watchdogDone
		return "", fmt.Errorf("waiting for agent: %w", ctx.Err())
	}

	// Stop watchdog
	cancelWatchdog()
	<-watchdogDone

	return result, nil
}

// Reset destroys the current session so the next Ask creates a fresh one.
func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != nil {
		a.session.Destroy()
		a.session = nil
		log.Printf("Agent session reset")
	}
}

func (a *Agent) createSession(ctx context.Context) (*copilot.Session, error) {
	cfg := &copilot.SessionConfig{
		Model:               a.config.Model,
		Streaming:           true,
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Hooks: &copilot.SessionHooks{
			OnPostToolUse: func(input copilot.PostToolUseHookInput, _ copilot.HookInvocation) (*copilot.PostToolUseHookOutput, error) {
				log.Printf("Tool done: %s", input.ToolName)
				return nil, nil
			},
		},
	}

	if a.config.SystemMessage != "" {
		cfg.SystemMessage = &copilot.SystemMessageConfig{
			Content: a.config.SystemMessage,
		}
	}

	if a.config.WorkingDir != "" {
		cfg.WorkingDirectory = a.config.WorkingDir
	}

	// Register skill directories
	if len(a.config.SkillDirectories) > 0 {
		cfg.SkillDirectories = a.config.SkillDirectories
		log.Printf("Agent skill directories: %v", a.config.SkillDirectories)
	}

	// Enable infinite sessions for context compaction
	if a.config.InfiniteSessions {
		cfg.InfiniteSessions = &copilot.InfiniteSessionConfig{
			Enabled: boolPtr(true),
		}
		log.Printf("Agent infinite sessions: enabled")
	}

	// Register MCP servers
	if len(a.config.MCPServers) > 0 {
		cfg.MCPServers = make(map[string]copilot.MCPServerConfig)
		for name, def := range a.config.MCPServers {
			serverCfg := map[string]any{
				"type":    "stdio",
				"command": def.Command,
			}
			if len(def.Args) > 0 {
				serverCfg["args"] = def.Args
			}
			if len(def.Env) > 0 {
				serverCfg["env"] = def.Env
			}
			if def.Cwd != "" {
				serverCfg["cwd"] = def.Cwd
			}
			cfg.MCPServers[name] = serverCfg
			log.Printf("Agent MCP server registered: %s (command: %s %v)", name, def.Command, def.Args)
		}
	}

	session, err := a.client.CreateSession(ctx, cfg)
	if err != nil {
		return nil, err
	}

	log.Printf("Agent session created (model: %s, mcp_servers: %d)", a.config.Model, len(a.config.MCPServers))
	return session, nil
}

func boolPtr(b bool) *bool { return &b }
