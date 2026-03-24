package agent

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/creack/pty"
	"nhooyr.io/websocket"
)

// terminalControl is a JSON control message sent over text WebSocket frames.
type terminalControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// HandleTerminal upgrades an HTTP request to a WebSocket and bridges it
// to an agent's PTY master fd. Binary frames carry raw PTY data in both
// directions. Text frames carry JSON control messages (e.g. resize).
func (r *Registry) HandleTerminal(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	agent := r.Get(name)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	agent.mu.Lock()
	master := agent.master
	status := agent.Status
	agent.mu.Unlock()

	if master == nil || status != StatusRunning {
		http.Error(w, "agent not running", http.StatusConflict)
		return
	}

	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		// Allow any origin — pogod is localhost-only and the React dashboard
		// may be served from a different port.
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("agent %s: websocket accept: %v", name, err)
		return
	}
	defer conn.CloseNow()

	ctx := req.Context()

	// Send scrollback so the client sees current terminal state.
	scrollback := agent.outputBuf.Last(agent.outputBuf.Len())
	if len(scrollback) > 0 {
		if err := conn.Write(ctx, websocket.MessageBinary, scrollback); err != nil {
			return
		}
	}

	// Create a pipe so the output fanout can write to us without blocking.
	// The fanout goroutine writes to pw; we read from pr and send to WS.
	pr, pw := io.Pipe()

	// Register for output fanout (after scrollback replay to avoid duplication).
	agent.attachMu.Lock()
	agent.attachConns[pw] = struct{}{}
	agent.attachMu.Unlock()

	defer func() {
		agent.attachMu.Lock()
		delete(agent.attachConns, pw)
		agent.attachMu.Unlock()
		pw.Close()
	}()

	// PTY output → WebSocket (binary frames)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					pr.Close()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → PTY master (input + control messages)
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		switch typ {
		case websocket.MessageBinary:
			// Raw terminal input → PTY master
			agent.mu.Lock()
			m := agent.master
			agent.mu.Unlock()
			if m == nil {
				conn.Close(websocket.StatusGoingAway, "agent exited")
				return
			}
			if _, err := m.Write(data); err != nil {
				return
			}

		case websocket.MessageText:
			// JSON control message
			var ctrl terminalControl
			if err := json.Unmarshal(data, &ctrl); err != nil {
				continue
			}
			switch ctrl.Type {
			case "resize":
				if ctrl.Cols > 0 && ctrl.Rows > 0 {
					agent.mu.Lock()
					m := agent.master
					agent.mu.Unlock()
					if m != nil {
						pty.Setsize(m, &pty.Winsize{
							Cols: ctrl.Cols,
							Rows: ctrl.Rows,
						})
					}
				}
			}
		}
	}
}

// handleTerminal is the route handler registered in RegisterHandlers.
func (r *Registry) handleTerminal(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	r.HandleTerminal(w, req)
}
