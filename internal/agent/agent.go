// Package agent manages agent processes with PTY allocation.
//
// pogod spawns each agent with its own PTY. The master file descriptor stays
// with pogod for interactive access (attach), input injection (nudge), and
// output monitoring. The slave end becomes the agent process's controlling
// terminal.
package agent

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
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

	// PromptFile is the path to the agent's prompt file (if any).
	PromptFile string `json:"prompt_file,omitempty"`

	// Dir is the working directory for the agent process.
	Dir string `json:"dir,omitempty"`

	// WorktreeDir is the git worktree path for polecat isolation (cleanup on exit).
	WorktreeDir string `json:"worktree_dir,omitempty"`
	// SourceRepo is the original repo path used to create/remove the worktree.
	SourceRepo string `json:"source_repo,omitempty"`

	// master is the PTY master file descriptor. Not exported (held by pogod).
	master *os.File
	cmd    *exec.Cmd

	// outputBuf holds recent output for monitoring.
	outputBuf *RingBuffer

	// socketPath is the unix domain socket for attach.
	socketPath string
	listener   net.Listener

	// attachConns tracks active attach connections.
	// readOutput fans out PTY output to these in addition to the ring buffer.
	attachConns map[net.Conn]struct{}
	attachMu    sync.Mutex

	// done is closed when the agent process exits and output is drained.
	done chan struct{}
	// outputDone is closed when the readOutput goroutine finishes.
	outputDone chan struct{}
	// exitErr holds the process exit error (nil on clean exit).
	exitErr error

	mu sync.Mutex
}

// GetStatus returns the agent's current status, safe for concurrent use.
func (a *Agent) GetStatus() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Status
}

// Registry is the in-memory agent registry. Thread-safe.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent

	// socketDir is where per-agent unix domain sockets live.
	socketDir string

	// onExit is called when an agent process exits. Set by the registry owner
	// (pogod) to handle crew restarts and polecat cleanup.
	onExit func(a *Agent, err error)
}

// SetOnExit sets the callback invoked when any agent exits.
func (r *Registry) SetOnExit(fn func(a *Agent, err error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onExit = fn
}

// NewRegistry creates an agent registry. socketDir is created if needed.
func NewRegistry(socketDir string) (*Registry, error) {
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	return &Registry{
		agents:    make(map[string]*Agent),
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
	Name       string
	Type       AgentType
	Command    []string // e.g. ["claude", "--append-system-prompt", "<prompt content>"]
	Env        []string // additional env vars
	PromptFile string   // path to prompt file (optional)
	Dir        string   // working directory for the process (optional)
}

// Spawn starts a new agent process with a PTY.
func (r *Registry) Spawn(req SpawnRequest) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[req.Name]; exists {
		return nil, fmt.Errorf("agent %q already running", req.Name)
	}

	if len(req.Command) == 0 {
		return nil, fmt.Errorf("command is required")
	}

	cmd := exec.Command(req.Command[0], req.Command[1:]...)
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

	// Start with PTY — master fd returned, slave becomes controlling terminal
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	a := &Agent{
		Name:        req.Name,
		PID:         cmd.Process.Pid,
		Type:        req.Type,
		StartTime:   time.Now(),
		Command:     req.Command,
		Status:      StatusRunning,
		PromptFile:  req.PromptFile,
		Dir:         req.Dir,
		master:      master,
		cmd:         cmd,
		outputBuf:   NewRingBuffer(64 * 1024), // 64KB rolling buffer
		attachConns: make(map[net.Conn]struct{}),
		socketPath:  filepath.Join(r.socketDir, req.Name+".sock"),
		done:        make(chan struct{}),
		outputDone:  make(chan struct{}),
	}

	// Start output reader — sole reader of master fd, fans out to
	// ring buffer + any active attach connections
	go a.readOutput()

	// Start process reaper — waits for exit, fires onExit callback
	go r.waitAndHandle(a)

	// Start attach listener
	if err := a.startListener(); err != nil {
		// Non-fatal — agent runs fine, just can't attach
		log.Printf("agent %s: attach listener failed: %v", req.Name, err)
	}

	r.agents[req.Name] = a
	log.Printf("agent %s: spawned pid=%d type=%s proc=%s", req.Name, a.PID, req.Type, procName)
	return a, nil
}

// Get returns an agent by name, or nil if not found.
func (r *Registry) Get(name string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[name]
}

// List returns all running agents.
func (r *Registry) List() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		agents = append(agents, a)
	}
	return agents
}

// Remove removes an agent from the registry. Does not stop it.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, name)
}

// Stop signals an agent to exit and waits up to timeout for it.
func (r *Registry) Stop(name string, timeout time.Duration) error {
	agent := r.Get(name)
	if agent == nil {
		return fmt.Errorf("agent %q not found", name)
	}

	// Send SIGTERM via the process
	if err := agent.cmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal agent: %w", err)
	}

	select {
	case <-agent.done:
		// Clean exit
	case <-time.After(timeout):
		// Force kill
		agent.cmd.Process.Kill()
		<-agent.done
	}

	agent.Cleanup()
	r.Remove(name)
	log.Printf("agent %s: stopped", name)
	return nil
}

// StopAll stops all agents.
func (r *Registry) StopAll(timeout time.Duration) {
	r.mu.RLock()
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	r.mu.RUnlock()

	for _, name := range names {
		if err := r.Stop(name, timeout); err != nil {
			log.Printf("agent %s: stop error: %v", name, err)
		}
	}
}

// Respawn restarts a stopped agent in-place, preserving its name and config.
// Used by crew monitoring to restart crashed agents.
func (r *Registry) Respawn(name string) (*Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

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

	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	a := &Agent{
		Name:         old.Name,
		PID:          cmd.Process.Pid,
		Type:         old.Type,
		StartTime:    time.Now(),
		Command:      old.Command,
		Status:       StatusRunning,
		RestartCount: restartCount,
		PromptFile:   old.PromptFile,
		Dir:          old.Dir,
		WorktreeDir:  old.WorktreeDir,
		SourceRepo:   old.SourceRepo,
		master:       master,
		cmd:          cmd,
		outputBuf:    NewRingBuffer(64 * 1024),
		attachConns:  make(map[net.Conn]struct{}),
		socketPath:   filepath.Join(r.socketDir, old.Name+".sock"),
		done:         make(chan struct{}),
		outputDone:   make(chan struct{}),
	}

	go a.readOutput()
	go r.waitAndHandle(a)

	if err := a.startListener(); err != nil {
		log.Printf("agent %s: attach listener failed on respawn: %v", a.Name, err)
	}

	r.agents[name] = a
	log.Printf("agent %s: respawned pid=%d restart=%d", name, a.PID, restartCount)
	return a, nil
}

// Nudge writes a message to an agent's PTY master fd.
// The agent sees this as typed input.
func (a *Agent) Nudge(message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.master == nil {
		return fmt.Errorf("agent %q has no PTY", a.Name)
	}

	_, err := a.master.WriteString(message + "\r")
	if err != nil {
		return fmt.Errorf("write to PTY: %w", err)
	}
	return nil
}

// RecentOutput returns the most recent buffered output.
func (a *Agent) RecentOutput(n int) []byte {
	return a.outputBuf.Last(n)
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
			for conn := range a.attachConns {
				// Best-effort write — don't block output on slow clients
				conn.Write(data)
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
	a.mu.Unlock()

	close(a.done)
	log.Printf("agent %s: exited (err=%v)", a.Name, a.exitErr)

	r.mu.RLock()
	cb := r.onExit
	r.mu.RUnlock()
	if cb != nil {
		cb(a, a.exitErr)
	}
}

// Cleanup closes PTY and socket. Exported for use by lifecycle callbacks.
func (a *Agent) Cleanup() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.master != nil {
		a.master.Close()
		a.master = nil
	}
	if a.listener != nil {
		a.listener.Close()
		a.listener = nil
	}
	os.Remove(a.socketPath)
}

// startListener creates a unix domain socket for attach connections.
func (a *Agent) startListener() error {
	// Remove stale socket if it exists
	os.Remove(a.socketPath)

	l, err := net.Listen("unix", a.socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	a.listener = l

	go a.acceptLoop()
	return nil
}

// acceptLoop handles incoming attach connections.
// Each connection gets bidirectional bridging to the PTY master.
func (a *Agent) acceptLoop() {
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go a.handleAttach(conn)
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

	// conn → PTY master (user input → agent)
	// Blocks until conn closes (user detaches) or master closes (agent exits)
	io.Copy(master, conn)
}

// SocketPath returns the unix socket path for attach.
func (a *Agent) SocketPath() string {
	return a.socketPath
}
