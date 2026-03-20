package ai

import (
	"context"
	"fmt"
	"log"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
)

// AgentConfig configures an agent session with MCP tool access.
type AgentConfig struct {
	Model         string            // Model for agent reasoning (e.g. "claude-sonnet-4.6")
	SystemMessage string            // System prompt for the agent
	MCPServers    map[string]MCPDef // External MCP servers to connect
	WorkingDir    string            // Working directory for file operations
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
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Hooks: &copilot.SessionHooks{
			OnPostToolUse: func(input copilot.PostToolUseHookInput, _ copilot.HookInvocation) (*copilot.PostToolUseHookOutput, error) {
				log.Printf("Agent tool call: %s", input.ToolName)
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
