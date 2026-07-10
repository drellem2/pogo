// Package agent manages agent processes with PTY allocation.
//
// pogod spawns each agent with its own PTY. The master file descriptor stays
// with pogod for interactive access (attach), input injection (nudge), and
// output monitoring. The slave end becomes the agent process's controlling
// terminal.
package agent

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/drellem2/pogo/internal/events"
)

// AgentType distinguishes long-running crew agents from ephemeral polecats.
type AgentType string

const (
	TypeCrew    AgentType = "crew"
	TypePolecat AgentType = "polecat"
)

// AgentStatus represents the current state of an agent.
type AgentStatus string

const (
	StatusRunning    AgentStatus = "running"
	StatusExited     AgentStatus = "exited"
	StatusRestarting AgentStatus = "restarting"
)

// Default PTY dimensions used when no attach client has reported its size yet.
// A real attach overwrites these via the resize frame on connect; the values
// just need to be non-zero so the agent's TUI doesn't render at 0×0 (which Ink
// falls back to 80×24).
const (
	defaultPTYCols uint16 = 200
	defaultPTYRows uint16 = 50
)

// Agent represents a running agent process with its PTY.
type Agent struct {
	Name      string      `json:"name"`
	PID       int         `json:"pid"`
	Type      AgentType   `json:"type"`
	StartTime time.Time   `json:"start_time"`
	Command   []string    `json:"command"`
	Status    AgentStatus `json:"status"`

	// ExitTime records when the process exited, for accurate uptime on exited agents.
	ExitTime time.Time `json:"exit_time,omitempty"`

	// ExitCode is set after the process exits (-1 if signaled).
	ExitCode int `json:"exit_code,omitempty"`

	// RestartCount tracks how many times a crew agent has been restarted.
	RestartCount int `json:"restart_count,omitempty"`

	// RestartOnCrash controls whether pogod respawns this agent when it
	// exits unexpectedly. Resolved at spawn time from prompt frontmatter
	// (see ResolveRestartOnCrash); defaults follow agent type — crew
	// agents restart, polecats do not.
	RestartOnCrash bool `json:"restart_on_crash"`

	// PromptFile is the path to the agent's prompt file (if any).
	PromptFile string `json:"prompt_file,omitempty"`

	// Dir is the working directory for the agent process.
	Dir string `json:"dir,omitempty"`

	// WorktreeDir is the git worktree path for polecat isolation (cleanup on exit).
	WorktreeDir string `json:"worktree_dir,omitempty"`
	// SourceRepo is the original repo path used to create/remove the worktree.
	SourceRepo string `json:"source_repo,omitempty"`

	// WorkItemID links a polecat (or future crew claim) to an mg work item
	// (e.g. "mg-3640"). Set at spawn from SpawnPolecatAPIRequest.Id; empty for
	// agents not tied to a specific item. Used by spend tracking to attribute
	// running-agent token cost to the right item without grepping events.
	WorkItemID string `json:"work_item_id,omitempty"`

	// RateLimited is set by the modal watcher when the agent is suspected to
	// have hit a provider usage limit — the rate-limit-options modal is visible
	// and the agent's event log has been stale past the usage-limit threshold
	// (~5m). It is cleared when the agent resumes producing events. Guarded by
	// mu; mutated only through SetRateLimited. See internal/claude/modal_hook.go
	// and docs/operations.md (usage-limit recovery runbook).
	RateLimited bool `json:"rate_limited,omitempty"`
	// RateLimitedSince records when RateLimited was last set true (zero when the
	// agent is not currently rate-limited). Guarded by mu.
	RateLimitedSince time.Time `json:"rate_limited_since,omitempty"`

	// InitialNudge is the message sent after spawn to bypass the CLI interactive prompt.
	// Stored so Respawn can re-send it.
	InitialNudge string `json:"-"`

	// master is the PTY master file descriptor. Not exported (held by pogod).
	master *os.File
	cmd    *exec.Cmd

	// nudge is the provider's PTY-input dialect, captured at spawn from this
	// agent's resolved provider (or DefaultNudgeProfile when none is set).
	// Immutable after construction, so it is safe to read without a.mu.
	nudge NudgeProfile

	// provider is the harness descriptor resolved for this agent at spawn
	// time. It carries the PostSpawnHook / SessionHook, the prompt-injection
	// strategy, and (via nudge above) the PTY dialect. Stored on the agent so
	// a restart (Respawn, driven by the onExit hook) comes back with this
	// agent's OWN provider, not a registry-global default. May be nil for a
	// bare-registry spawn. Immutable after construction; safe to read without
	// a.mu.
	provider *Provider

	// outputBuf holds recent output for monitoring.
	outputBuf *RingBuffer

	// socketPath is the unix domain socket for attach.
	socketPath string
	listener   net.Listener

	// socketInfo identifies the socket file the current listener is bound to.
	// The supervisor compares it (os.SameFile — device + inode) against whatever
	// now sits at socketPath, to notice a socket that was unlinked underneath us
	// (macOS reaps stale entries under $TMPDIR) or replaced by a foreign bind.
	// nil means "unknown, skip the check". Best-effort: a rebind of the same
	// path can reuse the inode number, so a replacement can go unnoticed. The
	// accept-loop and missing-file signals below are exact; this one is a guard.
	socketInfo os.FileInfo

	// listenerDead is closed by acceptLoop when it stops serving. The supervisor
	// waits on it so a listener that dies while the process lives gets rebound
	// rather than leaving a socket file nobody accepts on (mg-d216).
	listenerDead chan struct{}

	// attachStop is closed by Cleanup to retire the supervisor. attachStopped
	// records the same fact under mu so a rebind that is already in flight
	// aborts instead of recreating a socket for a torn-down agent.
	attachStop    chan struct{}
	attachStopped bool

	// supervisorInterval is this agent's copy of attachSupervisorInterval,
	// snapshotted on the spawning goroutine so a test that retunes the package
	// default cannot race a running supervisor. Zero means use the default.
	supervisorInterval time.Duration

	// attachConns tracks active attach/terminal connections for output fanout.
	// readOutput fans out PTY output to these in addition to the ring buffer.
	attachConns map[io.Writer]struct{}
	attachMu    sync.Mutex

	// done is closed when the agent process exits and output is drained.
	done chan struct{}
	// outputDone is closed when the readOutput goroutine finishes.
	outputDone chan struct{}
	// exitErr holds the process exit error (nil on clean exit).
	exitErr error

	// stopRequested is set by Stop before signaling so waitAndHandle can
	// classify a non-zero exit as agent_stopped (reason="requested") rather
	// than agent_crashed.
	stopRequested bool

	mu sync.Mutex
}

// GetStatus returns the agent's current status, safe for concurrent use.
func (a *Agent) GetStatus() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Status
}

// SetRateLimited records (or clears) the usage-limit condition for this agent.
// It is idempotent: setting the same value twice is a no-op, so the modal
// watcher can call it on every gate evaluation without churning
// RateLimitedSince. Setting true stamps RateLimitedSince with the current time;
// clearing resets it to zero. Safe for concurrent use — the modal watcher
// goroutine calls this while status/diagnose readers hold the same lock.
func (a *Agent) SetRateLimited(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v == a.RateLimited {
		return
	}
	a.RateLimited = v
	if v {
		a.RateLimitedSince = time.Now()
	} else {
		a.RateLimitedSince = time.Time{}
	}
}

// IsRateLimited reports whether the agent is currently flagged as rate-limited.
func (a *Agent) IsRateLimited() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.RateLimited
}

// pidAlive reports whether a process with the given pid is currently running,
// via kill(pid, 0): a nil error means the process exists and is signalable.
// A pid <= 0 is treated as dead.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// alive reports whether the agent's OS process is still running. A closed done
// channel (cmd.Wait has returned and reaped the process) or a pid that no
// longer answers signal 0 both mean not alive. Stop and Spawn use this to
// detect a stale registry entry whose process has died (gh #19) — a crew
// agent that exited cleanly without re-arming, a crash whose respawn failed,
// etc. — so stop can clear it and start can overwrite it.
func (a *Agent) alive() bool {
	select {
	case <-a.done:
		return false
	default:
	}
	return pidAlive(a.PID)
}

// Alive reports whether the agent's OS process is still running. Exported for
// the scheduler's stale-mail-check GC (gh drellem2/macguffin #15), which must
// distinguish a live agent from one whose process has died.
func (a *Agent) Alive() bool { return a.alive() }

// ProviderID returns the id of the harness provider resolved for this agent
// at spawn time ("claude", "codex", "pi"), or "" for a bare-registry spawn.
// The field is immutable after construction, so no lock is needed. Exposed so
// integration tests can assert which provider the resolution chain actually
// picked (per-type config vs global default vs built-in fallback).
func (a *Agent) ProviderID() string {
	if a.provider == nil {
		return ""
	}
	return a.provider.ID
}

// eventAgent returns the agent identity string used in event log envelopes.
// Mirrors the identity convention from docs/event-log.md: crew-<name> or cat-<name>.
func (a *Agent) eventAgent() string {
	if a.Type == TypeCrew {
		return "crew-" + a.Name
	}
	return "cat-" + a.Name
}

// emitSpawned records an agent_spawned event for both fresh spawns and respawns.
func (a *Agent) emitSpawned() {
	details := map[string]any{
		"agent_type": string(a.Type),
		"pid":        a.PID,
	}
	if a.PromptFile != "" {
		details["prompt_file"] = a.PromptFile
	}
	if a.WorktreeDir != "" {
		details["worktree"] = a.WorktreeDir
	}
	events.Emit(context.Background(), events.Event{
		EventType: "agent_spawned",
		Agent:     a.eventAgent(),
		Repo:      a.SourceRepo,
		Details:   details,
	})
}

// AgentCommandConfig provides the explicitly-configured command template and
// harness provider for a given agent type. This interface decouples the agent
// package from the config package (config.AgentsConfig satisfies it).
//
// AgentCommand returns "" when no explicit command is configured — the
// Registry then falls back to the resolved provider's CommandTemplate.
// AgentProvider returns the configured provider id for the type (per-type
// override, else the global default); it feeds the per-spawn provider
// resolution chain in resolveProvider.
type AgentCommandConfig interface {
	AgentCommand(agentType string) string
	AgentProvider(agentType string) string
}

// Registry is the in-memory agent registry. Thread-safe.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent

	// socketDir is where per-agent unix domain sockets live.
	socketDir string

	// cmdConfig provides agent command templates. May be nil (uses default).
	cmdConfig AgentCommandConfig

	// onExit is called when an agent process exits. Set by the registry owner
	// (pogod) to handle crew restarts and polecat cleanup.
	onExit func(a *Agent, err error)

	// providers is the pool of known harness descriptors, keyed by id
	// ("claude", "codex", "pi"). pogod registers every provider at startup
	// (RegisterProvider); resolveProvider then picks one per spawn from the
	// precedence chain. Before mg-b31b the registry held a single global
	// provider plus a global hook pair — provider selection is now per-spawn,
	// so a mixed Claude/Codex fleet needs no pogod restart. An empty map is a
	// bare registry (unit tests): spawns fall back to pogo's built-in
	// nudge/PTY defaults and run no lifecycle hooks.
	//
	// Each spawn's resolved *Provider is stored on the Agent (Agent.provider),
	// so its PostSpawnHook / SessionHook / nudge dialect / PTY size travel with
	// that agent — including across a restart via the onExit hook.
	providers map[string]*Provider

	// defaultProviderID is the global-default provider id — the resolution
	// tier used when no --provider flag, prompt frontmatter, or per-agent-type
	// config selects one. pogod sets it from [agents] provider (which
	// POGO_AGENT_PROVIDER already folds in). Empty leaves the built-in
	// DefaultProviderID as the floor.
	defaultProviderID string

	// shutdown is set by StopAll to short-circuit any in-flight respawn
	// goroutines (scheduled by the OnExit hook for restart_on_crash agents)
	// so the registry can be torn down without an agent coming back.
	shutdown bool

	// stallSchedules, when set, supplies the recurring cron schedules targeting
	// an agent so diagnose can suppress the stalled label during normal
	// between-cron idle (mg-5b23). pogod wires it to its scheduler; nil (the
	// default, and what unit tests use) disables cron-aware suppression.
	stallSchedules StallScheduleProvider

	// schedulePauser, when set, lets Park/Wake pause and restore an agent's
	// pogod schedules (mg-41e1). pogod wires it to its scheduler; nil (bare
	// registry, scheduler disabled) makes park skip schedule handling.
	schedulePauser SchedulePauser

	// mailCheckRegistrar, when set, auto-registers a per-polecat mail-check
	// schedule at spawn so a review-loop polecat notices peer mail without
	// polling (mg-e633). pogod wires it to its scheduler; nil (bare registry,
	// scheduler disabled) makes spawn skip registration.
	mailCheckRegistrar MailCheckRegistrar
}

// SetStallScheduleProvider installs the cron-schedule lookup used by diagnose to
// distinguish by-design between-cron idle from a genuine wedge. Call once at
// startup before agents are diagnosed.
func (r *Registry) SetStallScheduleProvider(p StallScheduleProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stallSchedules = p
}

// SetOnExit sets the callback invoked when any agent exits.
func (r *Registry) SetOnExit(fn func(a *Agent, err error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onExit = fn
}

// SetCommandConfig sets the agent command configuration.
func (r *Registry) SetCommandConfig(cfg AgentCommandConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmdConfig = cfg
}

// SessionHookFunc is the signature of a lifetime-scoped agent hook. The ctx
// passed in is cancelled when the agent's PTY exits; the function is expected
// to block (goroutine-style) until then. Sibling to PostSpawnHook, which runs
// only for a bounded post-spawn window — see mg-4421 / mg-ef6b for the
// motivation (mid-session modal watchers need lifetime scope, not spawn scope).
//
// Both hooks live on the Provider descriptor (Provider.PostSpawnHook /
// Provider.SessionHook). They are applied per-spawn off the agent's resolved
// provider, not off a registry-global field — so a Codex polecat and a Claude
// crew agent in the same fleet each run their own provider's hooks.
type SessionHookFunc func(ctx context.Context, a *Agent)

// RegisterProvider adds a harness provider to the registry's per-spawn
// resolution pool, keyed by p.ID. pogod registers every known provider at
// startup (claude, codex, pi); resolveProvider then picks one per spawn from the
// precedence chain. Registering the same id twice replaces the earlier entry.
// A nil provider is ignored.
func (r *Registry) RegisterProvider(p *Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]*Provider)
	}
	r.providers[p.ID] = p
}

// SetDefaultProvider sets the global-default provider id — the resolution tier
// consulted when no --provider flag, prompt frontmatter, or per-agent-type
// config selects one. pogod sets this from [agents] provider (which
// POGO_AGENT_PROVIDER already folds in). Empty leaves the built-in
// DefaultProviderID as the resolution floor.
func (r *Registry) SetDefaultProvider(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultProviderID = id
}

// resolveProvider walks the per-spawn provider precedence chain and returns the
// harness descriptor for one spawn. See resolveProviderLocked for the chain and
// error semantics. Takes the read lock; callers must NOT already hold r.mu.
func (r *Registry) resolveProvider(agentType AgentType, flagProvider, frontmatterProvider string) (*Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveProviderLocked(agentType, flagProvider, frontmatterProvider)
}

// resolveProviderLocked is the lock-free core of resolveProvider. The caller
// must hold r.mu (read or write); Spawn calls it directly because it already
// holds the write lock.
//
// Precedence, highest wins:
//
//  1. flagProvider        — explicit --provider override (spawn-polecat)
//  2. frontmatterProvider — provider: key in the agent prompt's frontmatter
//  3. per-type / global config — [agents.<type>] provider, then [agents] provider
//  4. registry default    — SetDefaultProvider (pogod: [agents] provider / env)
//  5. built-in default    — DefaultProviderID ("claude")
//
// An unknown id from the flag tier is a hard error — the caller explicitly
// asked for that provider, so failing fast beats silently spawning the wrong
// harness. An unknown id from any lower tier (frontmatter, config, default)
// logs a warning and falls back to the built-in default, so a stale config or
// prompt degrades gracefully instead of wedging the spawn.
//
// Returns (nil, nil) for a bare registry with no providers registered — the
// degenerate unit-test case — which tells Spawn to fall back to pogo's
// built-in nudge/PTY defaults and run no lifecycle hooks.
func (r *Registry) resolveProviderLocked(agentType AgentType, flagProvider, frontmatterProvider string) (*Provider, error) {
	// Bare registry: nothing registered, nothing to resolve.
	if len(r.providers) == 0 {
		return nil, nil
	}

	id, tier := flagProvider, "--provider flag"
	if id == "" {
		id, tier = frontmatterProvider, "provider: frontmatter"
	}
	if id == "" && r.cmdConfig != nil {
		id, tier = r.cmdConfig.AgentProvider(string(agentType)), "config"
	}
	if id == "" {
		id, tier = r.defaultProviderID, "global default"
	}
	if id == "" {
		id, tier = DefaultProviderID, "built-in default"
	}

	if p := r.providers[id]; p != nil {
		return p, nil
	}

	// Unknown id. The flag is an explicit, just-typed request: fail fast so a
	// typo never silently spawns the wrong harness.
	if tier == "--provider flag" {
		return nil, fmt.Errorf("unknown agent provider %q (known: %s)", id, r.knownProviderIDsLocked())
	}
	// Lower tiers: warn and fall back to the built-in default rather than
	// wedging the spawn (never a silent wrong-provider spawn — the warning is
	// the trace).
	log.Printf("WARNING: unknown agent provider %q (from %s); falling back to %q",
		id, tier, DefaultProviderID)
	if p := r.providers[DefaultProviderID]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("unknown agent provider %q and built-in default %q is not registered",
		id, DefaultProviderID)
}

// knownProviderIDsLocked returns the registered provider ids, sorted and
// comma-joined, for use in error messages. Caller must hold r.mu.
func (r *Registry) knownProviderIDsLocked() string {
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// spawnDefaults returns the nudge profile and PTY winsize for a freshly-spawned
// agent, sourced from its resolved per-spawn provider — or pogo's built-in
// defaults when the provider is nil (bare registry).
func spawnDefaults(p *Provider) (NudgeProfile, *pty.Winsize) {
	nudge := DefaultNudgeProfile
	winsize := &pty.Winsize{Cols: defaultPTYCols, Rows: defaultPTYRows}
	if p != nil {
		nudge = p.Nudge
		if p.PTYSize != nil {
			winsize = &pty.Winsize{Cols: p.PTYSize.Cols, Rows: p.PTYSize.Rows}
		}
	}
	return nudge, winsize
}

// invokeSessionHook spawns the agent's resolved-provider session hook (if any)
// on a fresh goroutine bound to ctx. The ctx is cancelled by a watcher
// goroutine when the agent's Done() channel closes, so the hook tears down
// without callers having to wire OnExit plumbing.
//
// The hook is read off a.provider — the agent's own per-spawn provider — so a
// restarted agent re-arms its own provider's watcher, never a registry-global
// one. Called from Spawn / Respawn while r.mu is already held exclusively.
func (r *Registry) invokeSessionHook(a *Agent) {
	if a.provider == nil || a.provider.SessionHook == nil {
		return
	}
	fn := a.provider.SessionHook
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-a.done
		cancel()
	}()
	go fn(ctx, a)
}

// commandTemplate returns the command template for the given agent type and
// resolved provider.
//
// Precedence: an explicit per-type or global [agents] command (config file or
// POGO_AGENT_COMMAND env) wins; otherwise the resolved provider's
// CommandTemplate supplies the default. Returns "" only in the degenerate case
// where neither a config command nor a provider is available — pogod always
// resolves a provider, so this happens only in bare-registry unit tests, where
// ExpandCommand then surfaces a clear "expanded to empty string" error.
func (r *Registry) commandTemplate(agentType AgentType, p *Provider) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cmdConfig != nil {
		if cmd := r.cmdConfig.AgentCommand(string(agentType)); cmd != "" {
			return cmd
		}
	}
	if p != nil {
		return p.CommandTemplate
	}
	return ""
}

// NewRegistry creates an agent registry. socketDir is created if needed.
func NewRegistry(socketDir string) (*Registry, error) {
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	return &Registry{
		agents:    make(map[string]*Agent),
		providers: make(map[string]*Provider),
		socketDir: socketDir,
	}, nil
}

// ProcessName returns the pgrep-discoverable process name for an agent.
// Crew: pogo-crew-<name>, Polecat: pogo-cat-<name>
func ProcessName(agentType AgentType, name string) string {
	if agentType == TypeCrew {
		return "pogo-crew-" + name
	}
	return "pogo-cat-" + name
}

// SpawnRequest contains everything needed to spawn an agent.
type SpawnRequest struct {
	Name           string
	Type           AgentType
	Command        []string // e.g. ["claude", "--append-system-prompt", "<prompt content>"]
	Env            []string // additional env vars
	PromptFile     string   // path to prompt file (optional)
	Dir            string   // working directory for the process (optional)
	WorktreeDir    string   // git worktree path for polecat isolation (optional)
	SourceRepo     string   // original repo path for worktree removal (optional)
	InitialNudge   string   // if set, pogod sends this nudge after spawn to bypass the CLI interactive prompt
	RestartOnCrash bool     // if true, pogod respawns this agent when it exits unexpectedly
	WorkItemID     string   // mg work item id this agent is assigned to (polecats); empty for crew/general agents

	// Provider is the harness descriptor resolved for this one spawn. The
	// handlers that build the command from a provider's template
	// (handleSpawnPolecat, StartCrewAgent) resolve it up-front and pass it
	// here. When nil — the generic POST /agents path and bare-registry tests —
	// Spawn resolves it itself by agent type. It supplies the nudge dialect,
	// PTY size, prompt-injection strategy, and lifecycle hooks for the agent.
	Provider *Provider
}

// Spawn starts a new agent process with a PTY.
func (r *Registry) Spawn(req SpawnRequest) (*Agent, error) {
	// Validate before taking the lock or touching the filesystem: a name pogod
	// cannot bind an attach socket for is rejected outright rather than spawned
	// with attach quietly unavailable (mg-ef80). This is the choke point every
	// spawn path funnels through — the generic POST /agents, StartCrewAgent,
	// and handleSpawnPolecat.
	if err := ValidateAgentName(req.Name); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, exists := r.agents[req.Name]; exists {
		if existing.alive() {
			return nil, fmt.Errorf("agent %q already running", req.Name)
		}
		// Dead-process registry semantics (gh #19): the existing registration's
		// process has died (clean exit without re-arm, crash whose respawn
		// failed, …). Treat it as a stale entry — tear it down and proceed with
		// the fresh spawn below, overwriting it — so `start` just works instead
		// of refusing against a dead pid.
		log.Printf("agent %s: overwriting stale registration (previous pid=%d is dead)", req.Name, existing.PID)
		existing.Cleanup()
		delete(r.agents, req.Name)
	}

	if len(req.Command) == 0 {
		return nil, fmt.Errorf("command is required")
	}

	// Resolve the harness provider for this one spawn. handleSpawnPolecat /
	// StartCrewAgent resolve it up-front (they build the command from the
	// provider's template) and pass it in req.Provider; the generic
	// POST /agents path and bare-registry tests leave it nil, so resolve it
	// here by agent type. A nil result is fine — pogo's built-in defaults
	// apply.
	provider := req.Provider
	if provider == nil {
		p, perr := r.resolveProviderLocked(req.Type, "", "")
		if perr != nil {
			return nil, fmt.Errorf("resolve provider: %w", perr)
		}
		provider = p
	}

	// Deliver the initial prompt as a trailing positional argv element when the
	// provider declares InitialPromptViaArgv (pi: `pi [messages...]`). Argv
	// delivery replaces the PTY initial-nudge path below: a differential-render
	// TUI can redraw near-continuously, the idle window the typed nudge waits
	// for never opens, and the nudge times out leaving the agent taskless
	// forever (gh #26). The harness reads argv before its TUI even starts, so
	// there is no idle-window race. The command is copied, not appended in
	// place, so the caller's slice is never mutated; the stored a.Command
	// carries the prompt so restart-on-crash re-delivers it via re-exec.
	command := req.Command
	promptViaArgv := provider != nil && provider.InitialPromptViaArgv && req.InitialNudge != ""
	if promptViaArgv {
		command = append(append([]string(nil), req.Command...), req.InitialNudge)
		log.Printf("agent %s: delivering initial prompt via argv (provider %q)", req.Name, provider.ID)
	}

	cmd := exec.Command(command[0], command[1:]...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	// Inject agent identity env vars
	procName := ProcessName(req.Type, req.Name)
	injectedEnv := []string{
		"POGO_AGENT_NAME=" + req.Name,
		"POGO_AGENT_TYPE=" + string(req.Type),
		"POGO_PROCESS_NAME=" + procName,
	}
	if req.PromptFile != "" {
		injectedEnv = append(injectedEnv, "POGO_AGENT_PROMPT="+req.PromptFile)
	}
	cmd.Env = append(os.Environ(), append(injectedEnv, req.Env...)...)

	// Deliver the persona prompt to a ContextFile-injection provider (Codex
	// reads AGENTS.override.md from its working directory). Must happen before
	// the process starts so the harness picks it up on launch. A no-op for
	// flag/env-injection providers like Claude.
	if err := writeContextFilePrompt(provider, req.PromptFile, req.Dir); err != nil {
		return nil, fmt.Errorf("context-file prompt injection: %w", err)
	}

	// Resolve the nudge dialect and PTY winsize from this spawn's provider.
	nudge, winsize := spawnDefaults(provider)

	// Start with PTY — master fd returned, slave becomes controlling terminal.
	// Set a sensible default winsize so the agent's TUI doesn't render at the
	// kernel default (0×0, which Ink falls back to 80×24). Real attach clients
	// overwrite this via the resize frame on connect; the goal here is just to
	// avoid a degenerate initial size.
	//
	// Isolation guarantee (gh #22): pty.StartWithSize forces SysProcAttr
	// Setsid+Setctty, so every agent runs in its own session and process
	// group with the PTY slave as its controlling terminal. A signal aimed
	// at one agent's group (or at pogod's) therefore never cascades to
	// pogod or sibling agents. TestSpawnProcessGroupIsolation guards this.
	master, err := pty.StartWithSize(cmd, winsize)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	a := &Agent{
		Name:           req.Name,
		PID:            cmd.Process.Pid,
		Type:           req.Type,
		StartTime:      time.Now(),
		Command:        command,
		Status:         StatusRunning,
		PromptFile:     req.PromptFile,
		Dir:            req.Dir,
		WorktreeDir:    req.WorktreeDir,
		SourceRepo:     req.SourceRepo,
		RestartOnCrash: req.RestartOnCrash,
		WorkItemID:     req.WorkItemID,
		master:         master,
		cmd:            cmd,
		nudge:          nudge,
		provider:       provider,
		outputBuf:      NewRingBuffer(64 * 1024), // 64KB rolling buffer
		attachConns:    make(map[io.Writer]struct{}),
		socketPath:     filepath.Join(r.socketDir, req.Name+".sock"),
		done:           make(chan struct{}),
		outputDone:     make(chan struct{}),
	}

	// Bind the attach socket before the PTY plumbing goroutines start and
	// before the agent enters the registry, so a permanent bind failure can be
	// undone with a kill instead of a partial teardown.
	//
	// A permanent failure means this agent could never be attached to — the
	// socket path itself is unusable — and returning a live agent (and a 201)
	// for it would be a lie. Every other bind failure is transient from the
	// supervisor's point of view: it keeps retrying on its ticker, so an agent
	// spawned during fd exhaustion recovers attach once fds free up rather than
	// losing it for good (mg-d216). See mg-ef80.
	if err := a.startListener(); err != nil {
		if isFatalListenErr(err) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			a.Cleanup()
			return nil, fmt.Errorf("%w: agent %s at %s: %w", ErrAttachSocketUnusable, req.Name, a.socketPath, err)
		}
		log.Printf("agent %s: attach listener failed: %v — supervisor will retry", req.Name, err)
	}

	// Start output reader — sole reader of master fd, fans out to
	// ring buffer + any active attach connections
	go a.readOutput()

	// Start process reaper — waits for exit, fires onExit callback
	go r.waitAndHandle(a)

	r.agents[req.Name] = a
	log.Printf("agent %s: spawned pid=%d type=%s proc=%s", req.Name, a.PID, req.Type, procName)

	a.emitSpawned()

	// Run post-spawn hook (e.g. trust dialog dismissal) if this agent's
	// resolved provider declares one. Read off a.provider, not a registry
	// global, so each agent runs its own provider's hook.
	if a.provider != nil && a.provider.PostSpawnHook != nil {
		go a.provider.PostSpawnHook(a)
	}

	// Run lifetime session hook (e.g. modal-dismissal watcher per mg-4421).
	// Sibling lifecycle to postSpawnHook above; also sourced from a.provider.
	r.invokeSessionHook(a)

	// Send initial nudge to bypass the CLI interactive prompt.
	// Use wait-ready mode so the nudge fires only once the harness's
	// interactive input loop is genuinely ready (prompt-ready sentinel seen,
	// then output settled), not merely quiet — a harness is also quiet during
	// pre-TUI startup, and a nudge typed into that gap piles in the kernel
	// input buffer and gets absorbed into one un-re-tokenized paste block,
	// wedging the agent (mg-ce61). Providers without a sentinel fall back to
	// wait-idle. Gated on the provider's NeedsInitialNudge — a harness that
	// takes the persona prompt as a command-line arg needs no nudge — and
	// skipped when the prompt already went out via argv above (a.InitialNudge
	// then stays empty, so Respawn re-delivers via re-exec, not re-nudge).
	if req.InitialNudge != "" && a.nudge.NeedsInitialNudge && !promptViaArgv {
		a.InitialNudge = req.InitialNudge
		go func() {
			if err := a.NudgeWithMode(req.InitialNudge, NudgeWaitReady, a.nudge.InitialNudgeTimeout); err != nil {
				log.Printf("agent %s: initial nudge failed: %v", req.Name, err)
			}
		}()
	}

	return a, nil
}

// Get returns an agent by name, or nil if not found.
func (r *Registry) Get(name string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[name]
}

// GetByWorkItemOrName resolves an agent by matching id against either the
// agent's registry Name or its WorkItemID, returning nil if neither matches.
//
// This exists because a polecat registers under its bare id (agent Name, e.g.
// "d087") while a merge request it authors carries the full work-item id (e.g.
// "mg-d087"): a plain Get(mr.Author) misses. Matching WorkItemID == id is
// prefix-agnostic and robust — it works regardless of the mg-/ca- prefix in
// use — while the Name fallback preserves lookups keyed on the bare id. A fast
// direct Get is tried first; the WorkItemID scan runs only on a Name miss.
func (r *Registry) GetByWorkItemOrName(id string) *Agent {
	if id == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a := r.agents[id]; a != nil {
		return a
	}
	for _, a := range r.agents {
		if a.WorkItemID == id {
			return a
		}
	}
	return nil
}

// List returns all running agents sorted by type (crew first) then name.
func (r *Registry) List() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Type != agents[j].Type {
			return agents[i].Type == TypeCrew
		}
		return agents[i].Name < agents[j].Name
	})
	return agents
}

// Remove removes an agent from the registry. Does not stop it.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, name)
}

// Stop signals an agent to exit and waits up to timeout for it.
//
// For agents with RestartOnCrash=true, the registry entry is left intact
// after the process exits so the OnExit hook's Respawn() goroutine can
// find it and bring the agent back. This matches the "always-on" contract
// of restart_on_crash=true: the supervisor restarts the agent on any exit
// (clean or crash, including explicit Stop). To keep such an agent down
// permanently, park it — `pogo agent park <name>` (Registry.Park) persists
// a park flag that suppresses the respawn and survives pogod restarts.
// Registry teardown (StopAll) also bypasses respawn unconditionally.
//
// For agents with RestartOnCrash=false — and for parked agents, whose
// respawn the supervisor will refuse — Stop() owns teardown and removes
// the agent from the registry once the process is gone.
func (r *Registry) Stop(name string, timeout time.Duration) error {
	agent := r.Get(name)
	if agent == nil {
		return fmt.Errorf("agent %q not found", name)
	}

	// Dead-process registry semantics (gh #19): if the registered process is
	// already gone — a crew agent that exited cleanly without re-arming, a
	// crash whose respawn failed, etc. — the registry entry is stale. Clear it
	// unconditionally, even for RestartOnCrash agents that Stop otherwise
	// leaves intact for the supervisor, so a subsequent `start` is not blocked
	// by an "already running" error against a dead pid. Makes stop idempotent
	// and lets a single wedged agent be recovered without bouncing the daemon.
	if !agent.alive() {
		agent.Cleanup()
		r.Remove(name)
		log.Printf("agent %s: stopped (cleared stale registration; process already dead)", name)
		return nil
	}

	// Check if already exited before signaling to avoid
	// "os: process already finished" errors.
	select {
	case <-agent.done:
		// Already exited, skip signal/wait
	default:
		// Mark before signaling so waitAndHandle classifies the exit as
		// requested (agent_stopped) rather than a crash.
		agent.mu.Lock()
		agent.stopRequested = true
		agent.mu.Unlock()
		// Send SIGTERM via the process
		if err := agent.cmd.Process.Signal(os.Interrupt); err != nil {
			// Process may have exited between check and signal — that's OK
			<-agent.done
		} else {
			select {
			case <-agent.done:
				// Clean exit
			case <-time.After(timeout):
				// Force kill
				agent.cmd.Process.Kill()
				<-agent.done
			}
		}
	}

	if agent.RestartOnCrash && !IsParked(name) {
		// Lifecycle is owned by the OnExit hook — it scheduled (or will
		// schedule) a Respawn() that needs the registry entry to still
		// exist. Cleanup of the old PTY/socket happens inside Respawn().
		log.Printf("agent %s: stopped (restart_on_crash=true; supervisor will respawn)", name)
		return nil
	}

	agent.Cleanup()
	r.Remove(name)
	log.Printf("agent %s: stopped", name)
	return nil
}

// StopAll stops all agents and prevents subsequent Respawn() calls. Use
// this on registry teardown (pogod shutdown, test cleanup) to ensure that
// in-flight respawn goroutines scheduled by the OnExit hook do not bring
// agents back after StopAll returns.
func (r *Registry) StopAll(timeout time.Duration) {
	r.mu.Lock()
	r.shutdown = true
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	r.mu.Unlock()

	for _, name := range names {
		if err := r.Stop(name, timeout); err != nil {
			log.Printf("agent %s: stop error: %v", name, err)
		}
		// During shutdown, Stop() leaves restart_on_crash agents in the
		// registry expecting Respawn(); but Respawn() now refuses with
		// shutdown=true, so the agent stays gone. Drop the registry
		// entry explicitly to release resources.
		if a := r.Get(name); a != nil {
			a.Cleanup()
			r.Remove(name)
		}
	}
}

// Respawn restarts a stopped agent in-place, preserving its name and config.
// Used by crew monitoring to restart crashed agents.
//
// Returns an error without spawning if the registry has been shut down via
// StopAll — this lets late-firing OnExit respawn goroutines lose cleanly to
// teardown instead of resurrecting agents the user just stopped.
func (r *Registry) Respawn(name string) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.shutdown {
		return nil, fmt.Errorf("registry shut down")
	}

	// Backstop for the park race: a respawn goroutine scheduled by a crash
	// that predates the park must lose to the on-disk flag (the primary check
	// is ShouldRespawn in the OnExit hook, before the goroutine is scheduled).
	if IsParked(name) {
		return nil, fmt.Errorf("agent %q is parked", name)
	}

	old := r.agents[name]
	if old == nil {
		return nil, fmt.Errorf("agent %q not found", name)
	}

	old.mu.Lock()
	if old.Status == StatusRunning {
		old.mu.Unlock()
		return nil, fmt.Errorf("agent %q is still running", name)
	}
	restartCount := old.RestartCount + 1
	old.mu.Unlock()

	old.Cleanup()

	cmd := exec.Command(old.Command[0], old.Command[1:]...)
	if old.Dir != "" {
		cmd.Dir = old.Dir
	}
	procName := ProcessName(old.Type, old.Name)
	injectedEnv := []string{
		"POGO_AGENT_NAME=" + old.Name,
		"POGO_AGENT_TYPE=" + string(old.Type),
		"POGO_PROCESS_NAME=" + procName,
	}
	if old.PromptFile != "" {
		injectedEnv = append(injectedEnv, "POGO_AGENT_PROMPT="+old.PromptFile)
	}
	cmd.Env = append(os.Environ(), injectedEnv...)

	// A restart re-resolves to the agent's OWN provider (carried on old.provider
	// from its original spawn), never the registry default — so a Codex agent
	// restarts as Codex even when the fleet default is Claude. See mg-b31b
	// acceptance bar 9.
	provider := old.provider

	// Re-deliver the ContextFile persona for the respawned process (no-op for
	// Claude). Idempotent — overwrites the same AGENTS.override.md.
	if err := writeContextFilePrompt(provider, old.PromptFile, old.Dir); err != nil {
		return nil, fmt.Errorf("context-file prompt injection: %w", err)
	}

	nudge, winsize := spawnDefaults(provider)

	master, err := pty.StartWithSize(cmd, winsize)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	a := &Agent{
		Name:           old.Name,
		PID:            cmd.Process.Pid,
		Type:           old.Type,
		StartTime:      time.Now(),
		Command:        old.Command,
		Status:         StatusRunning,
		RestartCount:   restartCount,
		RestartOnCrash: old.RestartOnCrash,
		PromptFile:     old.PromptFile,
		Dir:            old.Dir,
		WorktreeDir:    old.WorktreeDir,
		SourceRepo:     old.SourceRepo,
		InitialNudge:   old.InitialNudge,
		WorkItemID:     old.WorkItemID,
		master:         master,
		cmd:            cmd,
		nudge:          nudge,
		provider:       provider,
		outputBuf:      NewRingBuffer(64 * 1024),
		attachConns:    make(map[io.Writer]struct{}),
		socketPath:     filepath.Join(r.socketDir, old.Name+".sock"),
		done:           make(chan struct{}),
		outputDone:     make(chan struct{}),
	}

	go a.readOutput()
	go r.waitAndHandle(a)

	if err := a.startListener(); err != nil {
		log.Printf("agent %s: attach listener failed on respawn: %v", a.Name, err)
	}

	r.agents[name] = a
	log.Printf("agent %s: respawned pid=%d restart=%d", name, a.PID, restartCount)

	// Re-arm the lifetime session hook (mg-4421) for the respawned PTY so the
	// modal-dismissal watcher covers the new process. (postSpawnHook is
	// intentionally not re-invoked here, matching pre-mg-4421 behavior: that
	// gap is tracked separately if it becomes a problem.)
	r.invokeSessionHook(a)

	a.emitSpawned()
	events.Emit(context.Background(), events.Event{
		EventType: "agent_restarted",
		Agent:     a.eventAgent(),
		Repo:      a.SourceRepo,
		Details: map[string]any{
			"previous_pid":  old.PID,
			"new_pid":       a.PID,
			"restart_count": restartCount,
		},
	})

	// Re-send initial nudge on respawn to bypass the CLI interactive prompt.
	// a.InitialNudge is only non-empty when the provider needed it at spawn.
	// Wait-ready mode for the same reason as the spawn path (mg-ce61).
	if a.InitialNudge != "" {
		go func() {
			if err := a.NudgeWithMode(a.InitialNudge, NudgeWaitReady, a.nudge.InitialNudgeTimeout); err != nil {
				log.Printf("agent %s: initial nudge on respawn failed: %v", a.Name, err)
			}
		}()
	}

	return a, nil
}

// Nudge writes a message to an agent's PTY master fd. The agent sees this as
// typed input followed by a submit (Enter).
//
// The body and the trailing submit terminator are written as two separate
// WriteString calls with a.nudge.SubmitDelay between them. The two writes must
// arrive in separate stdin read() calls on the receiver: when Claude Code's
// React/Ink input box gets the body and the terminator as one chunk, it treats
// the chunk as a paste and the terminator becomes a literal newline inside the
// input field instead of a submit. The result is a nudge that lands in the
// input box but never gets sent — observed on long-running crew agents where
// the keyboard protocol is fully initialized. Polecats accidentally avoided the
// bug only because their first nudges fire while the TUI is still in startup,
// before paste detection is armed. The Claude profile's 50ms gap is generous
// enough to span Node.js's read loop and Ink's re-render cycle (~16ms) without
// noticeably slowing nudge throughput. Both the gap and the terminator come
// from the provider's NudgeProfile (see provider.go).
func (a *Agent) Nudge(message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.master == nil {
		return fmt.Errorf("agent %q has no PTY", a.Name)
	}

	if message != "" {
		if _, err := a.master.WriteString(message); err != nil {
			return fmt.Errorf("write to PTY: %w", err)
		}
		time.Sleep(a.nudge.SubmitDelay)
	}

	if _, err := a.master.WriteString(a.nudge.SubmitTerminator); err != nil {
		return fmt.Errorf("write submit to PTY: %w", err)
	}
	return nil
}

// RecentOutput returns the most recent buffered output.
func (a *Agent) RecentOutput(n int) []byte {
	return a.outputBuf.Last(n)
}

// Subscribe registers w to receive a copy of every PTY-output chunk written
// after Subscribe returns. The writer is invoked synchronously from the PTY
// reader goroutine (alongside the attach-conn fanout), so its Write must be
// fast and non-blocking — slow writers stall the agent's output stream for
// every other consumer. Used by lifetime watchers (e.g. the modal-dismissal
// watcher in mg-4421) that need to byte-scan output as it arrives.
//
// The returned func deregisters w; callers MUST invoke it when they're done.
// (When the agent's PTY exits, readOutput drains and returns, so a stuck
// subscription leaks only a map entry until the registry forgets the agent.)
func (a *Agent) Subscribe(w io.Writer) func() {
	a.attachMu.Lock()
	a.attachConns[w] = struct{}{}
	a.attachMu.Unlock()
	return func() {
		a.attachMu.Lock()
		delete(a.attachConns, w)
		a.attachMu.Unlock()
	}
}

// EventAgent returns the canonical agent-identity string used in event log
// envelopes ("crew-<name>" or "cat-<name>"). Exported for adapters that need
// to query per-agent state in the global event log (mg-4421's modal watcher).
func (a *Agent) EventAgent() string {
	return a.eventAgent()
}

// SendRaw writes raw bytes to the PTY master without appending \r.
// Used for sending individual keypresses like Enter.
func (a *Agent) SendRaw(s string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.master == nil {
		return fmt.Errorf("agent %q has no PTY", a.Name)
	}
	_, err := a.master.WriteString(s)
	return err
}

// Done returns a channel that closes when the agent process exits.
func (a *Agent) Done() <-chan struct{} {
	return a.done
}

// ExitErr returns the process exit error, or nil if still running.
func (a *Agent) ExitErr() error {
	return a.exitErr
}

// readOutput is the sole reader of the PTY master fd.
// It fans out output to the ring buffer AND any active attach connections.
func (a *Agent) readOutput() {
	defer close(a.outputDone)
	buf := make([]byte, 4096)
	for {
		n, err := a.master.Read(buf)
		if n > 0 {
			data := buf[:n]
			a.outputBuf.Write(data)

			// Fan out to attached connections
			a.attachMu.Lock()
			for w := range a.attachConns {
				// Best-effort write — don't block output on slow clients
				w.Write(data)
			}
			a.attachMu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("agent %s: read error: %v", a.Name, err)
			}
			return
		}
	}
}

// waitAndHandle waits for the agent process to exit and fires the onExit callback.
func (r *Registry) waitAndHandle(a *Agent) {
	a.exitErr = a.cmd.Wait()

	// Wait for the output reader to drain all remaining PTY output before
	// signaling done, so callers of Done() see the complete output.
	<-a.outputDone

	a.mu.Lock()
	a.Status = StatusExited
	a.ExitTime = time.Now()
	if a.exitErr != nil {
		a.ExitCode = -1
		if exitErr, ok := a.exitErr.(*exec.ExitError); ok {
			a.ExitCode = exitErr.ExitCode()
		}
	}
	stopRequested := a.stopRequested
	exitCode := a.ExitCode
	duration := a.ExitTime.Sub(a.StartTime).Seconds()
	a.mu.Unlock()

	log.Printf("agent %s: exited (err=%v)", a.Name, a.exitErr)

	a.emitExit(stopRequested, exitCode, duration)

	// Fire onExit callback BEFORE closing done, so that callers waiting on
	// Done() (e.g. Stop/StopAll during shutdown) block until cleanup
	// (including worktree removal) has completed. Previously, done was closed
	// first, allowing the server to exit before onExit could clean up.
	r.mu.RLock()
	cb := r.onExit
	r.mu.RUnlock()
	if cb != nil {
		cb(a, a.exitErr)
	}

	close(a.done)
}

// emitExit records either agent_stopped (clean / requested exit) or
// agent_crashed (unexpected exit). Best-effort: errors never propagate.
func (a *Agent) emitExit(stopRequested bool, exitCode int, durationSeconds float64) {
	if stopRequested || exitCode == 0 {
		reason := "task_complete"
		if stopRequested {
			reason = "requested"
		}
		events.Emit(context.Background(), events.Event{
			EventType: "agent_stopped",
			Agent:     a.eventAgent(),
			Repo:      a.SourceRepo,
			Details: map[string]any{
				"pid":              a.PID,
				"exit_code":        exitCode,
				"reason":           reason,
				"duration_seconds": durationSeconds,
			},
		})
		return
	}
	details := map[string]any{
		"pid":       a.PID,
		"exit_code": exitCode,
	}
	if last := a.outputBuf.Last(512); len(last) > 0 {
		details["last_output"] = string(last)
	}
	events.Emit(context.Background(), events.Event{
		EventType: "agent_crashed",
		Agent:     a.eventAgent(),
		Repo:      a.SourceRepo,
		Details:   details,
	})
}

// Cleanup closes PTY and socket. Exported for use by lifecycle callbacks.
func (a *Agent) Cleanup() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.master != nil {
		a.master.Close()
		a.master = nil
	}
	a.retireListenerLocked()
}

// retireListenerLocked stops the supervisor and tears the listener down. Once it
// returns, no rebind can resurrect the socket: a supervisor that was already
// waiting on a.mu sees attachStopped and gives up. Idempotent — Cleanup runs
// more than once on some paths (stop, then the exit callback).
func (a *Agent) retireListenerLocked() {
	if !a.attachStopped {
		a.attachStopped = true
		if a.attachStop != nil {
			close(a.attachStop)
		}
	}
	if a.listener != nil {
		a.listener.Close()
		a.listener = nil
	}
	os.Remove(a.socketPath)
	a.socketInfo = nil
}

// attachSupervisorInterval is how often the attach-listener supervisor re-checks
// the socket file for a vanished or replaced inode. Overridden by tests.
var attachSupervisorInterval = 30 * time.Second

// Accept-retry backoff bounds for transient errors.
const (
	attachAcceptMinBackoff = 5 * time.Millisecond
	attachAcceptMaxBackoff = time.Second
)

// Rebind backoff bounds. A listener that dies the instant it is rebound — a
// recurring permanent Accept error — would otherwise drive the supervisor as a
// hot loop: a pegged core and one agent_attach_rebound event per iteration. The
// backoff resets once a listener has survived attachRebindResetAfter, so
// unrelated faults hours apart each still get an immediate repair.
const (
	attachRebindMinBackoff = 50 * time.Millisecond
	attachRebindMaxBackoff = 30 * time.Second
	attachRebindResetAfter = 5 * time.Minute
)

// acceptRetryLogInterval rate-limits the per-retry log line during a sustained
// accept-error streak, so a wedged listener cannot flood the daemon log.
const acceptRetryLogInterval = 30 * time.Second

// isRetryableAcceptErr reports whether Accept failed for a reason that may clear
// on its own, so the loop should back off and keep serving rather than retire
// the listener.
//
// Errno.Temporary() covers only EINTR, EMFILE, ENFILE and the timeout errnos.
// accept(2) also fails with ENOMEM/ENOBUFS under memory pressure, and with
// ECONNABORTED/ECONNRESET when a peer goes away between connect and accept — all
// recoverable, none reported as temporary. Anything else (EPERM under a sandbox
// policy, EBADF, EINVAL) is genuinely permanent: the loop returns and the
// supervisor rebinds on its own bounded backoff.
func isRetryableAcceptErr(err error) bool {
	var tmp interface{ Temporary() bool }
	if errors.As(err, &tmp) && tmp.Temporary() {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOMEM, syscall.ENOBUFS, syscall.ECONNABORTED, syscall.ECONNRESET:
			return true
		}
	}
	return false
}

// ErrAttachSocketUnusable reports a spawn abandoned because the agent's attach
// socket could never be bound. It is a property of the environment (a POGO_HOME
// deep enough to push the socket path past sun_path), not of the request, so the
// API handlers answer 500 rather than the legacy catch-all 409.
var ErrAttachSocketUnusable = errors.New("attach socket cannot be bound")

// isFatalListenErr reports whether binding the attach socket failed for a
// reason no retry can clear, because the socket path itself is unusable.
//
// The class that matters is a path that overruns sockaddr_un's sun_path.
// syscall.SockaddrUnix.sockaddr rejects such a path with EINVAL on darwin and
// linux alike, before the bind syscall is ever made, so EINVAL is the errno to
// key on rather than the ENAMETOOLONG one might expect. ENAMETOOLONG is checked
// too, for the paths a kernel does reject itself (a component past NAME_MAX).
//
// Everything else — EMFILE/ENFILE under fd exhaustion, EADDRINUSE against a
// racing daemon, a socket dir that a sweep removed out from under us — may clear
// on its own, and the supervisor rebinds when it does. Spawn kills the agent for
// a fatal error, so this set stays deliberately narrow: a false positive costs
// an agent that would otherwise have recovered.
func isFatalListenErr(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENAMETOOLONG)
}

// startListener creates a unix domain socket for attach connections and starts
// the supervisor that keeps it usable for the life of the process. The
// supervisor starts even when the first bind fails, so an agent spawned during
// fd exhaustion recovers attach once fds free up instead of losing it for good.
// Spawn checks the returned error with isFatalListenErr: a bind that can never
// succeed fails the spawn rather than leaving a live agent nobody can attach to.
func (a *Agent) startListener() error {
	a.mu.Lock()
	if a.attachStop == nil {
		a.attachStop = make(chan struct{})
	}
	if a.supervisorInterval == 0 {
		a.supervisorInterval = attachSupervisorInterval
	}
	stop := a.attachStop
	interval := a.supervisorInterval
	err := a.bindListenerLocked()
	a.mu.Unlock()

	go a.superviseListener(stop, interval)
	return err
}

// bindListenerLocked binds socketPath and starts a fresh accept loop, replacing
// any previous listener. Called with a.mu held.
func (a *Agent) bindListenerLocked() error {
	// Drop the old listener without letting it unlink the path we are about to
	// bind — Go's UnixListener unlinks by path on Close, not by inode.
	if a.listener != nil {
		disarmUnlinkOnClose(a.listener)
		a.listener.Close()
		a.listener = nil
	}
	// Remove the stale socket file: a leftover makes bind fail with EADDRINUSE.
	os.Remove(a.socketPath)

	l, err := net.Listen("unix", a.socketPath)
	if err != nil {
		// A nil listenerDead parks the supervisor's select on that arm, so it
		// retries on the ticker rather than spinning on a closed channel.
		a.listenerDead = nil
		a.socketInfo = nil
		return fmt.Errorf("listen: %w", err)
	}
	a.listener = l
	a.socketInfo, _ = os.Stat(a.socketPath)

	// Pass listener and dead channel directly so acceptLoop doesn't access
	// a.listener, avoiding a nil-pointer race when Cleanup nils it concurrently.
	dead := make(chan struct{})
	a.listenerDead = dead
	go a.acceptLoop(l, dead)
	return nil
}

// acceptLoop handles incoming attach connections.
// Each connection gets bidirectional bridging to the PTY master.
// Takes the listener as a parameter to avoid racing with Cleanup, and closes
// dead on exit so the supervisor learns the socket stopped being served.
func (a *Agent) acceptLoop(l net.Listener, dead chan struct{}) {
	defer close(dead)

	var backoff time.Duration
	var lastLog time.Time
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // listener closed by Cleanup or a rebind
			}
			// A recoverable accept error must not retire the listener. Under fd
			// exhaustion Accept returns EMFILE/ENFILE; returning here leaves the
			// socket file bound with nobody accepting, so the listen backlog
			// fills and every later attach gets ECONNREFUSED against a perfectly
			// healthy agent — the mg-d216 symptom.
			if isRetryableAcceptErr(err) {
				backoff = nextAcceptBackoff(backoff)
				if lastLog.IsZero() || time.Since(lastLog) >= acceptRetryLogInterval {
					log.Printf("agent %s: attach accept: %v — retrying in %s", a.Name, err, backoff)
					lastLog = time.Now()
				}
				time.Sleep(backoff)
				continue
			}
			log.Printf("agent %s: attach accept stopped: %v", a.Name, err)
			return // permanent — the supervisor rebinds while the process lives
		}
		backoff = 0
		lastLog = time.Time{}
		go a.handleAttach(conn)
	}
}

// nextAcceptBackoff doubles the accept-retry delay up to attachAcceptMaxBackoff.
func nextAcceptBackoff(cur time.Duration) time.Duration {
	return doubleBackoff(cur, attachAcceptMinBackoff, attachAcceptMaxBackoff)
}

// nextRebindBackoff doubles the rebind delay up to attachRebindMaxBackoff.
func nextRebindBackoff(cur time.Duration) time.Duration {
	return doubleBackoff(cur, attachRebindMinBackoff, attachRebindMaxBackoff)
}

// doubleBackoff returns min, then doubles toward max, saturating there.
func doubleBackoff(cur, min, max time.Duration) time.Duration {
	if cur <= 0 {
		return min
	}
	if next := cur * 2; next < max {
		return next
	}
	return max
}

// superviseListener ties the attach socket's lifetime to the process's. It
// repairs the two ways a live agent can end up unattachable (mg-d216):
//
//   - The accept loop stopped — a permanent Accept error, or a bind that failed
//     at spawn — while the process runs on. The socket file lingers, nothing
//     accepts, and once the backlog fills every attach gets ECONNREFUSED.
//   - The socket file was unlinked or replaced underneath a live listener (macOS
//     reaps stale entries under $TMPDIR), leaving the listener bound to an
//     orphaned inode and attach failing with ENOENT.
//
// It exits when the process exits or Cleanup retires the listener, and never
// recreates a socket for a torn-down agent.
func (a *Agent) superviseListener(stop chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Rebinding is repair, and repair that never succeeds must not become a hot
	// loop: an Accept error that recurs on every fresh listener would otherwise
	// spin the supervisor at syscall speed, one event written per iteration.
	var backoff time.Duration
	var lastRebind time.Time

	for {
		a.mu.Lock()
		dead := a.listenerDead
		a.mu.Unlock()

		select {
		case <-stop:
			return
		case <-a.done:
			return // process exited — Cleanup owns the socket now
		case <-dead:
			// accept loop stopped; deliberate closes land here too, and are
			// filtered out by the stop/alive checks below
		case <-ticker.C:
			// periodic check for a vanished or replaced socket file
		}

		if !a.alive() {
			return
		}
		select {
		case <-stop:
			return // Cleanup raced us to the dead channel
		default:
		}

		reason := a.attachUnhealthyReason()
		if reason == "" {
			continue
		}

		// A listener that has been healthy for a while makes this a fresh
		// incident, not a flap — repair it immediately.
		if !lastRebind.IsZero() && time.Since(lastRebind) >= attachRebindResetAfter {
			backoff = 0
		}

		rebound, err := a.rebindListener()
		switch {
		case err != nil:
			log.Printf("agent %s: attach listener rebind (%s) failed: %v", a.Name, reason, err)
		case !rebound:
			return // Cleanup retired the listener while we were deciding
		default:
			log.Printf("agent %s: attach listener rebound on %s (%s)", a.Name, a.socketPath, reason)
			a.emitAttachRebound(reason)
		}

		// Throttle the next repair — after both a failed bind and a rebind whose
		// listener may die again immediately. This bounds the loop's rate, and
		// with it the agent_attach_rebound event rate and the rebind-failure log.
		lastRebind = time.Now()
		backoff = nextRebindBackoff(backoff)
		if !a.sleepInterruptibly(backoff, stop) {
			return
		}
	}
}

// sleepInterruptibly waits for d, returning false if the agent was retired or
// its process exited first — so Cleanup never blocks behind a backoff.
func (a *Agent) sleepInterruptibly(d time.Duration, stop chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	case <-a.done:
		return false
	}
}

// attachUnhealthyReason names the reason attach connections can no longer land,
// or "" when the listener is healthy: a live accept loop bound to the socket
// file that is actually at socketPath.
func (a *Agent) attachUnhealthyReason() string {
	a.mu.Lock()
	l, dead, want := a.listener, a.listenerDead, a.socketInfo
	a.mu.Unlock()

	if l == nil || dead == nil {
		return "no_listener"
	}
	select {
	case <-dead:
		return "accept_loop_stopped"
	default:
	}
	if want == nil {
		return "" // identity unknown — nothing to compare against
	}
	cur, err := os.Stat(a.socketPath)
	if err != nil {
		return "socket_file_missing"
	}
	if !os.SameFile(cur, want) {
		return "socket_file_replaced"
	}
	return ""
}

// rebindListener re-creates the attach socket for a still-live agent. It reports
// whether it rebound; once Cleanup has retired the listener it does nothing and
// returns false, so a retired agent never gets its socket resurrected.
func (a *Agent) rebindListener() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attachStopped {
		return false, nil
	}
	if err := a.bindListenerLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// emitAttachRebound records that the attach socket was repaired under a live
// agent — the operator-visible signal that this fault occurred.
func (a *Agent) emitAttachRebound(reason string) {
	events.Emit(context.Background(), events.Event{
		EventType: "agent_attach_rebound",
		Agent:     a.eventAgent(),
		Repo:      a.SourceRepo,
		Details: map[string]any{
			"pid":    a.PID,
			"socket": a.socketPath,
			"reason": reason,
		},
	})
}

// disarmUnlinkOnClose stops a unix listener from unlinking its socket path on
// Close. Go unlinks by path, so a listener closed after the path was rebound
// would otherwise delete the new socket.
func disarmUnlinkOnClose(l net.Listener) {
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
}

// handleAttach bridges a unix socket connection to the PTY master.
// Output (master → conn) is handled by readOutput via the attach fanout.
// This method handles input (conn → master) and lifecycle.
func (a *Agent) handleAttach(conn net.Conn) {
	defer conn.Close()

	a.mu.Lock()
	master := a.master
	a.mu.Unlock()

	if master == nil {
		return
	}

	// Send recent output so the client sees current state
	recent := a.outputBuf.Last(a.outputBuf.Len())
	if len(recent) > 0 {
		conn.Write(recent)
	}

	// Register for output fanout (after replay, so we don't double-send)
	a.attachMu.Lock()
	a.attachConns[conn] = struct{}{}
	a.attachMu.Unlock()

	// Deregister on exit
	defer func() {
		a.attachMu.Lock()
		delete(a.attachConns, conn)
		a.attachMu.Unlock()
	}()

	// Read input from conn and forward to PTY master.
	// New clients send a leading FrameTypeResize byte to enter framed mode;
	// legacy clients send raw bytes. See attach_proto.go for the wire format.
	a.readAttachInput(conn, master)
}

// readAttachInput dispatches a unix-socket attach connection to either
// framed-mode parsing or legacy raw-byte streaming based on the first byte.
// Blocks until conn closes (user detaches) or master closes (agent exits).
func (a *Agent) readAttachInput(conn net.Conn, master io.Writer) {
	br := bufio.NewReaderSize(conn, 4096)

	first, err := br.ReadByte()
	if err != nil {
		return
	}
	if first != FrameTypeResize {
		// Legacy raw mode — write the byte back to the PTY and stream rest.
		if _, werr := master.Write([]byte{first}); werr != nil {
			return
		}
		io.Copy(master, br)
		return
	}

	// Framed mode — handshake resize frame is the remaining 4 bytes.
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return
	}
	a.applyResize(binary.LittleEndian.Uint16(hdr[0:2]), binary.LittleEndian.Uint16(hdr[2:4]))

	// Continue reading framed messages.
	for {
		typ, err := br.ReadByte()
		if err != nil {
			return
		}
		switch typ {
		case FrameTypeResize:
			if _, err := io.ReadFull(br, hdr[:]); err != nil {
				return
			}
			a.applyResize(binary.LittleEndian.Uint16(hdr[0:2]), binary.LittleEndian.Uint16(hdr[2:4]))
		case FrameTypeData:
			var lenBuf [2]byte
			if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
				return
			}
			n := int(binary.LittleEndian.Uint16(lenBuf[:]))
			if n == 0 {
				continue
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(br, data); err != nil {
				return
			}
			if _, err := master.Write(data); err != nil {
				return
			}
		default:
			// Unknown frame type — protocol error, drop the connection so the
			// client can reconnect cleanly.
			return
		}
	}
}

// applyResize updates the PTY winsize, but only when it actually changes.
//
// cols/rows of 0 mean "size unknown — keep the current winsize" and are
// ignored: the attach handshake sends 0×0 when the client can't read its
// terminal size, and a SIGWINCH with a stale 0 dimension would otherwise
// collapse the agent's TUI.
//
// A resize to the size the PTY already has is also skipped. TIOCSWINSZ
// signals SIGWINCH to the agent's foreground process group *unconditionally*
// — even when the dimensions are unchanged — and a TUI like Claude Code's Ink
// renderer answers every SIGWINCH with a full redraw. Those redraw bytes flow
// back through readOutput into outputBuf, bumping LastWriteTime; if a nudge's
// WaitIdle poll coincides with the redraw the agent never looks idle and the
// nudge fails with "context deadline exceeded". Making the resize idempotent
// keeps a connect-time handshake at the agent's current size (the common case
// once Spawn sets a sane default) from poking a healthy target.
//
// The whole operation runs under a.mu. The ioctls (TIOCGWINSZ/TIOCSWINSZ)
// reach into the *os.File's fd, and Cleanup closes that fd under the same
// lock — holding a.mu across the read-and-set is what keeps a concurrent
// detach/Stop from closing the PTY mid-ioctl (a race the original mg-5564
// code had, where Setsize ran after the lock was already released).
func (a *Agent) applyResize(cols, rows uint16) {
	if cols == 0 || rows == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	master := a.master
	if master == nil {
		return
	}
	if cur, err := pty.GetsizeFull(master); err == nil && cur.Cols == cols && cur.Rows == rows {
		// Already at this size — skip the redundant SIGWINCH/redraw.
		return
	}
	pty.Setsize(master, &pty.Winsize{Cols: cols, Rows: rows})
}

// SocketPath returns the unix socket path for attach.
func (a *Agent) SocketPath() string {
	return a.socketPath
}
