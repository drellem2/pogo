package client

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// AttachAgent connects the current terminal to a running agent's PTY
// via its unix domain socket. Returns when the connection closes or
// the user sends the escape sequence (Ctrl-\).
func AttachAgent(socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to agent socket: %w", err)
	}
	defer conn.Close()

	// Put terminal in raw mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Handle SIGWINCH to propagate terminal size changes
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	done := make(chan struct{}, 2)

	// stdin → socket (user input → agent)
	go func() {
		io.Copy(conn, os.Stdin)
		done <- struct{}{}
	}()

	// socket → stdout (agent output → user)
	go func() {
		io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()

	// Wait for either direction to close
	<-done
	return nil
}
