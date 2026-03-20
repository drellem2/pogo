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

// Agent represents a running agent process with its PTY.
type Agent struct {
	Name      string    `json:"name"`
	PID       int       `json:"pid"`
	Type      AgentType `json:"type"`
	StartTime time.Time `json:"start_time"`
	Command   []string  `json:"command"`

	// master is the PTY master file descriptor. Not exported (held by pogod).
	master *os.File
	cmd    *exec.Cmd

	// outputBuf holds recent output for monitoring.
	outputBuf *RingBuffer

	// socketPath is the unix domain socket for attach.
	socketPath string
	listener   net.Listener

	// done is closed when the agent process exits.
	done chan struct{}
	// exitErr holds the process exit error (nil on clean exit).
	exitErr error

	mu sync.Mutex
}

// Registry is the in-memory agent registry. Thread-safe.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*Agent

	// socketDir is where per-agent unix domain sockets live.
	socketDir string
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

// SpawnRequest contains everything needed to spawn an agent.
type SpawnRequest struct {
	Name    string
	Type    AgentType
	Command []string // e.g. ["claude", "--prompt-file", "/path/to/prompt.md"]
	Env     []string // additional env vars
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
	cmd.Env = append(os.Environ(), req.Env...)

	// Start with PTY — master fd returned, slave becomes controlling terminal
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	agent := &Agent{
		Name:       req.Name,
		PID:        cmd.Process.Pid,
		Type:       req.Type,
		StartTime:  time.Now(),
		Command:    req.Command,
		master:     master,
		cmd:        cmd,
		outputBuf:  NewRingBuffer(64 * 1024), // 64KB rolling buffer
		socketPath: filepath.Join(r.socketDir, req.Name+".sock"),
		done:       make(chan struct{}),
	}

	// Start output reader — reads from master, buffers for monitoring
	go agent.readOutput()

	// Start process reaper — waits for exit, cleans up
	go agent.waitExit()

	// Start attach listener
	if err := agent.startListener(); err != nil {
		// Non-fatal — agent runs fine, just can't attach
		log.Printf("agent %s: attach listener failed: %v", req.Name, err)
	}

	r.agents[req.Name] = agent
	log.Printf("agent %s: spawned pid=%d type=%s", req.Name, agent.PID, req.Type)
	return agent, nil
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

	agent.cleanup()
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

// Nudge writes a message to an agent's PTY master fd.
// The agent sees this as typed input.
func (a *Agent) Nudge(message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.master == nil {
		return fmt.Errorf("agent %q has no PTY", a.Name)
	}

	_, err := a.master.WriteString(message)
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

// readOutput reads from PTY master and buffers output.
func (a *Agent) readOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := a.master.Read(buf)
		if n > 0 {
			a.outputBuf.Write(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("agent %s: read error: %v", a.Name, err)
			}
			return
		}
	}
}

// waitExit waits for the agent process to exit.
func (a *Agent) waitExit() {
	a.exitErr = a.cmd.Wait()
	close(a.done)
	log.Printf("agent %s: exited (err=%v)", a.Name, a.exitErr)
}

// cleanup closes PTY and socket.
func (a *Agent) cleanup() {
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
// Data flows bidirectionally: conn ↔ PTY master.
func (a *Agent) handleAttach(conn net.Conn) {
	defer conn.Close()

	a.mu.Lock()
	master := a.master
	a.mu.Unlock()

	if master == nil {
		return
	}

	done := make(chan struct{}, 2)

	// conn → PTY master (user input → agent)
	go func() {
		io.Copy(master, conn)
		done <- struct{}{}
	}()

	// PTY master → conn (agent output → user)
	go func() {
		io.Copy(conn, master)
		done <- struct{}{}
	}()

	// Wait for either direction to close
	<-done
}

// SocketPath returns the unix socket path for attach.
func (a *Agent) SocketPath() string {
	return a.socketPath
}
