package ai

import (
	"context"
	"log"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/cpuchip/brain/internal/config"
)

// runningTask tracks an in-progress agent task with a cancellable context.
type runningTask struct {
	EntryID   string
	AgentName string
	cancel    context.CancelFunc
}

// AgentPool manages multiple named Agent instances with lazy creation.
// Each agent name gets its own session with a system message composed
// from workspace configuration.
type AgentPool struct {
	client     *copilot.Client
	baseConfig AgentConfig
	agents     map[string]*Agent
	tasks      map[string]*runningTask // keyed by entry ID
	mu         sync.RWMutex
}

// SessionSummary represents current usage and state for one named agent session.
type SessionSummary struct {
	Name  string       `json:"name"`
	Usage SessionUsage `json:"usage"`
}

// NewAgentPool creates a pool backed by the given Copilot client and base config.
// Individual agents are created lazily on first access via GetOrCreate.
func NewAgentPool(client *copilot.Client, baseCfg AgentConfig) *AgentPool {
	return &AgentPool{
		client:     client,
		baseConfig: baseCfg,
		agents:     make(map[string]*Agent),
		tasks:      make(map[string]*runningTask),
	}
}

// Client returns the underlying Copilot SDK client.
// Used by the pipeline package to create agents with custom configs.
func (p *AgentPool) Client() *copilot.Client {
	return p.client
}

// GetOrCreate returns the agent for the given name, creating it if needed.
// The agent's system message is composed from workspace config + agent prompt.
// An empty agentName returns a default agent with base instructions only.
func (p *AgentPool) GetOrCreate(agentName string, wc config.WorkspaceConfig) *Agent {
	key := agentName
	if key == "" {
		key = "_default"
	}

	p.mu.RLock()
	if a, ok := p.agents[key]; ok {
		p.mu.RUnlock()
		return a
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if a, ok := p.agents[key]; ok {
		return a
	}

	// Build agent-specific config
	cfg := p.baseConfig
	cfg.SystemMessage = BuildSystemMessage(wc, agentName)
	cfg.AgentName = agentName

	// Default agent gets custom agents for SDK intent-based delegation.
	// Named agents don't — they ARE the agent the router selected.
	if agentName == "" {
		cfg.CustomAgents = BuildCustomAgents(wc)
	}

	a := NewAgent(p.client, cfg)
	p.agents[key] = a

	if agentName != "" {
		log.Printf("Agent pool: created agent %q", agentName)
	} else {
		log.Printf("Agent pool: created default agent")
	}

	return a
}

// Default returns the default (unnamed) agent.
func (p *AgentPool) Default(wc config.WorkspaceConfig) *Agent {
	return p.GetOrCreate("", wc)
}

// ActiveSessions returns the names of agents that have been created.
func (p *AgentPool) ActiveSessions() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	names := make([]string, 0, len(p.agents))
	for name := range p.agents {
		names = append(names, name)
	}
	return names
}

// SessionSummaries returns usage snapshots for all created agent sessions.
func (p *AgentPool) SessionSummaries() []SessionSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]SessionSummary, 0, len(p.agents))
	for name, a := range p.agents {
		result = append(result, SessionSummary{
			Name:  name,
			Usage: a.Usage(),
		})
	}
	return result
}

// Reset destroys a specific agent's session and removes it from the pool.
func (p *AgentPool) Reset(agentName string) {
	key := agentName
	if key == "" {
		key = "_default"
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if a, ok := p.agents[key]; ok {
		a.Reset()
		delete(p.agents, key)
		log.Printf("Agent pool: reset agent %q", key)
	}
}

// ResetAll destroys all agent sessions.
func (p *AgentPool) ResetAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for name, a := range p.agents {
		a.Reset()
		delete(p.agents, name)
	}
	log.Printf("Agent pool: reset all agents")
}

// InactivityTimeout is how long a task can go without SDK events before being cancelled.
// The SDK sends events (reasoning deltas, tool calls, message deltas) continuously when
// the agent is working. A 5-minute gap with zero events means the connection has stalled.
// There is no wall clock cap — the human (or steward) is the circuit breaker for long tasks.
const InactivityTimeout = 5 * time.Minute

// activityContext wraps a cancellable context with an inactivity timer.
// The timer resets on each Touch() call. If the timer fires (no activity for
// InactivityTimeout), the context is cancelled.
type activityContext struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timer   *time.Timer
	timeout time.Duration
	mu      sync.Mutex
	closed  bool
}

// newActivityContext creates a context that cancels after inactivityTimeout of silence.
func newActivityContext(timeout time.Duration) *activityContext {
	ctx, cancel := context.WithCancel(context.Background())
	ac := &activityContext{
		ctx:     ctx,
		cancel:  cancel,
		timeout: timeout,
	}
	ac.timer = time.AfterFunc(timeout, func() {
		ac.mu.Lock()
		defer ac.mu.Unlock()
		if !ac.closed {
			log.Printf("Agent pool: inactivity timeout (%s with no events) — cancelling task", timeout)
			ac.cancel()
		}
	})
	return ac
}

// Touch resets the inactivity timer. Called on every SDK event.
func (ac *activityContext) Touch() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if !ac.closed {
		ac.timer.Reset(ac.timeout)
	}
}

// Cancel stops the timer and cancels the context.
func (ac *activityContext) Cancel() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.closed = true
	ac.timer.Stop()
	ac.cancel()
}

// StartTask registers a running task for an entry and returns a context with an
// inactivity-based timeout plus a touch function. The context cancels only when
// the agent goes silent (no SDK events for InactivityTimeout). Call touch() on
// every SDK event to keep the timer alive. There is no wall clock cap.
func (p *AgentPool) StartTask(entryID, agentName string) (context.Context, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Cancel any existing task for this entry
	if existing, ok := p.tasks[entryID]; ok {
		existing.cancel()
		delete(p.tasks, entryID)
	}

	ac := newActivityContext(InactivityTimeout)
	p.tasks[entryID] = &runningTask{
		EntryID:   entryID,
		AgentName: agentName,
		cancel:    ac.Cancel,
	}
	log.Printf("Agent pool: started task for entry %s (agent=%s, inactivity_timeout=%s)", entryID, agentName, InactivityTimeout)
	return ac.ctx, ac.Touch
}

// FinishTask removes a task from tracking (called when agent work completes).
func (p *AgentPool) FinishTask(entryID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tasks, entryID)
}

// CancelTask cancels a specific running task.
func (p *AgentPool) CancelTask(entryID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if task, ok := p.tasks[entryID]; ok {
		task.cancel()
		delete(p.tasks, entryID)
		log.Printf("Agent pool: cancelled task for entry %s", entryID)
	}
}

// CancelAll cancels all running tasks.
func (p *AgentPool) CancelAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, task := range p.tasks {
		task.cancel()
		delete(p.tasks, id)
	}
	log.Printf("Agent pool: cancelled all tasks")
}

// RunningTasks returns info about currently running tasks.
func (p *AgentPool) RunningTasks() []runningTask {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]runningTask, 0, len(p.tasks))
	for _, t := range p.tasks {
		result = append(result, runningTask{
			EntryID:   t.EntryID,
			AgentName: t.AgentName,
		})
	}
	return result
}

// BuildSystemMessage composes the system message from workspace config and optional agent.
// Exported so cmd/brain can use it for one-off exec sessions.
func BuildSystemMessage(wc config.WorkspaceConfig, agentName string) string {
	var parts []string

	if agentName != "" {
		if agent, ok := wc.Agents[agentName]; ok {
			parts = append(parts, agent.Prompt)
			log.Printf("Using agent: %s (%s)", agentName, agent.Description)
		} else {
			available := make([]string, 0, len(wc.Agents))
			for name := range wc.Agents {
				available = append(available, name)
			}
			log.Printf("WARNING: agent %q not found. Available: %v", agentName, available)
			log.Printf("Falling back to base instructions")
		}
	}

	if wc.BaseInstructions != "" {
		parts = append(parts, wc.BaseInstructions)
	}

	if len(parts) == 0 {
		return `You are a development agent for the scripture-study project. You have access to:

1. SCRIPTURE TOOLS (MCP): gospel_search, gospel_get, gospel_list, search_scriptures, search_talks, webster_define — use these to look up scriptures, conference talks, and word definitions.

2. BUILT-IN FILE TOOLS: You can read, search, and edit files in the workspace.

When given a spec or task:
- Read and understand the relevant source code first
- Make precise, minimal changes that implement the spec
- After making changes, verify by reading the modified files
- Report what you changed and why`
	}

	return joinParts(parts)
}

func joinParts(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += "\n\n---\n\n" + p
	}
	return result
}
