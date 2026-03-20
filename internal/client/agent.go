package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// ListAgents returns all running agents from pogod.
func ListAgents() ([]agent.AgentInfo, error) {
	r, err := http.Get(serverURL + "/agents")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var agents []agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&agents); err != nil {
		return nil, err
	}
	return agents, nil
}

// GetAgent returns details for a specific agent.
func GetAgent(name string) (*agent.AgentInfo, error) {
	r, err := http.Get(serverURL + "/agents/" + name)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	var info agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// SpawnAgent asks pogod to spawn a new agent.
func SpawnAgent(req agent.SpawnAPIRequest) (*agent.AgentInfo, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r, err := http.Post(serverURL+"/agents", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		body := strings.TrimSpace(string(msg))
		if r.StatusCode == http.StatusNotFound || body == "greetings from pogo daemon" {
			return nil, fmt.Errorf("spawn failed: pogod does not support agent endpoints (restart pogod with an updated build)")
		}
		return nil, fmt.Errorf("spawn failed: %s", body)
	}
	var info agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// StartAgent asks pogod to start a crew agent by name.
// The prompt file is looked up from ~/.pogo/agents/crew/<name>.md.
func StartAgent(name string) (*agent.AgentInfo, error) {
	body, err := json.Marshal(agent.StartAPIRequest{Name: name})
	if err != nil {
		return nil, err
	}
	r, err := http.Post(serverURL+"/agents/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		body := strings.TrimSpace(string(msg))
		if r.StatusCode == http.StatusNotFound || body == "greetings from pogo daemon" {
			return nil, fmt.Errorf("start failed: pogod does not support agent endpoints (restart pogod with an updated build)")
		}
		return nil, fmt.Errorf("start failed: %s", body)
	}
	var info agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// StopAgent asks pogod to stop an agent.
func StopAgent(name string) error {
	req, err := http.NewRequest("DELETE", serverURL+"/agents/"+name, nil)
	if err != nil {
		return err
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return fmt.Errorf("agent %q not found", name)
	}
	if r.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("stop failed: %s", string(msg))
	}
	return nil
}

// NudgeOpts configures nudge delivery.
type NudgeOpts struct {
	Mode    string // "wait-idle" or "immediate"
	Timeout int    // seconds, for wait-idle mode
}

// ErrAgentNotRunning is returned when the target agent is not registered with pogod.
var ErrAgentNotRunning = fmt.Errorf("agent not running")

// NudgeAgent sends a message to an agent's PTY with the given options.
func NudgeAgent(name, message string, opts *NudgeOpts) error {
	req := agent.NudgeAPIRequest{Message: message}
	if opts != nil {
		req.Mode = opts.Mode
		req.Timeout = opts.Timeout
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.Post(serverURL+"/agents/"+name+"/nudge", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return ErrAgentNotRunning
	}
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return fmt.Errorf("nudge failed: %s", string(msg))
	}
	return nil
}

// SpawnPolecat asks pogod to spawn a polecat from a template.
func SpawnPolecat(req agent.SpawnPolecatAPIRequest) (*agent.AgentInfo, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r, err := http.Post(serverURL+"/agents/spawn-polecat", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(r.Body)
		body := strings.TrimSpace(string(msg))
		if r.StatusCode == http.StatusNotFound || body == "greetings from pogo daemon" {
			return nil, fmt.Errorf("spawn-polecat failed: pogod does not support agent endpoints (restart pogod with an updated build)")
		}
		return nil, fmt.Errorf("spawn-polecat failed: %s", body)
	}
	var info agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ListPrompts returns all discovered prompt files from pogod.
func ListPrompts() ([]agent.PromptInfo, error) {
	r, err := http.Get(serverURL + "/agents/prompts")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var prompts []agent.PromptInfo
	if err := json.NewDecoder(r.Body).Decode(&prompts); err != nil {
		return nil, err
	}
	return prompts, nil
}

// NudgeOrMail tries to nudge an agent via PTY. If the agent is not running,
// it falls back to sending a macguffin mail message via the gt CLI.
func NudgeOrMail(name, message string, opts *NudgeOpts) (fallback bool, err error) {
	err = NudgeAgent(name, message, opts)
	if err == nil {
		return false, nil
	}
	if err != ErrAgentNotRunning {
		return false, err
	}

	// Fallback: send via gt mail
	return true, sendMailFallback(name, message)
}

// sendMailFallback sends a nudge message via gt mail send.
func sendMailFallback(name, message string) error {
	return SendMail(name, "nudge", message)
}

// SendMail sends a mail message to the given address via gt mail send.
// The address is interpreted as a rig/role path (e.g. "mayor/", "pogo/polecats/chrome").
func SendMail(address, subject, body string) error {
	cmd := execCommand("gt", "mail", "send", address, "-s", subject, "-m", body)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mail send failed: %s (%w)", string(out), err)
	}
	return nil
}

// SendMGMail sends a mail message via macguffin (mg mail send).
// Used by non-agent components like the refinery that need to deliver mail
// to agents reading via mg mail list.
func SendMGMail(to, from, subject, body string) error {
	cmd := execCommand("mg", "mail", "send", to, "--from="+from, "--subject="+subject, "--body="+body)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg mail send failed: %s (%w)", string(out), err)
	}
	return nil
}

// execCommand is a variable for testability.
var execCommand = execCommandFunc

func execCommandFunc(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// GetAgentOutput returns recent output from an agent.
func GetAgentOutput(name string) (string, error) {
	r, err := http.Get(serverURL + "/agents/" + name + "/output")
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("agent %q not found", name)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
