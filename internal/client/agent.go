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
		return nil, interpretSpawnFailure("spawn", r)
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
		return nil, interpretSpawnFailure("start", r)
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

// ParkAgent asks pogod to park a crew agent: stop it, persist a park flag
// that suppresses respawn and auto-start, and pause its schedules.
func ParkAgent(name string) (*agent.ParkAPIResponse, error) {
	r, err := http.Post(serverURL+"/agents/"+name+"/park", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("park failed: %s", strings.TrimSpace(string(msg)))
	}
	var resp agent.ParkAPIResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WakeAgent asks pogod to wake a parked crew agent: start it, restore its
// recorded schedules, and clear the park flag.
func WakeAgent(name string) (*agent.WakeAPIResponse, error) {
	r, err := http.Post(serverURL+"/agents/"+name+"/wake", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("wake failed: %s", strings.TrimSpace(string(msg)))
	}
	var resp agent.WakeAPIResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
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

// GetHostLoad asks pogod what share of this host the fleet is holding.
//
// It goes through pogod rather than measuring locally on purpose: pogod is the
// process every agent descends from, so it is the only vantage point where
// "the fleet's share" is a well-defined subtree, and it is the same gate the
// spawn path consults.
func GetHostLoad() (*agent.HostLoadResponse, error) {
	r, err := http.Get(serverURL + "/hostload")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("host load: %s: %s", r.Status, strings.TrimSpace(string(b)))
	}
	var resp agent.HostLoadResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
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
		return nil, interpretSpawnFailure("spawn-polecat", r)
	}
	var info agent.AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// interpretSpawnFailure converts a non-2xx response from an agent-spawn
// endpoint into a user-facing error. Three cases, in priority order:
//
//  1. Structured JSON body (new pogod, e.g. {"reason":"prompt-not-found",
//     "message":"..."}): the Message field is surfaced verbatim.
//  2. Plain-text body (old pogod): the body is surfaced verbatim — pogod's
//     text bodies already include the missing path and the suggested fix
//     command for prompt-not-found.
//  3. 404 with no body, the Go default "404 page not found" body, or the
//     "greetings from pogo daemon" sentinel served at /: only here do we
//     suggest rebuilding pogod, since these are the shapes that indicate the
//     endpoint truly isn't implemented or the wrong process answered.
//
// Fix for GitHub Issue #15 / mg-be51: previously every 404 was reported as
// "rebuild pogod", which hid pogod's "prompt file not found" message when a
// crew prompt wasn't installed (a common fresh-install failure mode).
func interpretSpawnFailure(op string, r *http.Response) error {
	raw, _ := io.ReadAll(r.Body)
	body := strings.TrimSpace(string(raw))

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && len(raw) > 0 {
		var se agent.StartErrorResponse
		if err := json.Unmarshal(raw, &se); err == nil && se.Message != "" {
			return fmt.Errorf("%s failed: %s", op, se.Message)
		}
	}

	if r.StatusCode == http.StatusNotFound && isEndpointMissingBody(body) {
		return fmt.Errorf("%s failed: pogod does not support agent endpoints (restart pogod with an updated build)", op)
	}

	if body == "" {
		return fmt.Errorf("%s failed: HTTP %d", op, r.StatusCode)
	}
	return fmt.Errorf("%s failed: %s", op, body)
}

// isEndpointMissingBody reports whether body is one of the well-known shapes
// returned when /agents/* doesn't exist — Go's default ServeMux 404 page, an
// empty body, or the "greetings from pogo daemon" sentinel that pogod's root
// handler serves. pogod's own structured 404s (prompt-not-found, etc.) never
// match these.
func isEndpointMissingBody(body string) bool {
	switch body {
	case "", "404 page not found", "greetings from pogo daemon":
		return true
	}
	return false
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

// ArchiveMGDoneItems triggers macguffin to archive all done work items
// immediately (--days=0). Called by the refinery after a successful merge
// so the merged item moves from done/ to archive/ at its natural lifecycle
// endpoint rather than waiting for time-based cleanup.
func ArchiveMGDoneItems() (string, error) {
	cmd := execCommand("mg", "archive", "--days=0")
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mg archive failed: %s (%w)", string(out), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ReopenMGWorkItem calls mg reopen to move a done work item back to claimed/.
// Returns nil if the reopen succeeds. Non-fatal errors (e.g. item not in done/)
// are returned as errors for the caller to log.
func ReopenMGWorkItem(id string) error {
	cmd := execCommand("mg", "reopen", id)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg reopen failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CompleteMGWorkItem calls mg done to move a claimed work item to done/,
// recording the result JSON as a sidecar. pogod's OnMerged hook uses this to
// record completion on a merged polecat's behalf before stopping it (gh #35):
// a polecat stopped at merge time never sees the merged status, so it never
// gets to run mg done itself. An "already done" error just means the polecat
// won the race — callers should log and move on.
func CompleteMGWorkItem(id, resultJSON string) error {
	args := []string{"done", id}
	if resultJSON != "" {
		args = append(args, "--result="+resultJSON)
	}
	cmd := execCommand("mg", args...)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg done failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// MGWorkItemDone reports whether a work item has reached a terminal state
// (done or archived). pogod's defer-done backstop uses it to tell a polecat
// that finished its post-merge flow but is still alive — the polecat protocol
// tells it to stay up until the mayor stops it — from one that genuinely never
// completed. Without the distinction, every PR-flow polecat would draw a
// "never called mg done" escalation 15 minutes after merging (mg-7746).
//
// An unparseable or failing lookup returns an error; callers should treat that
// as "unknown" and fall back to the conservative path rather than assuming
// completion.
func MGWorkItemDone(id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("work item id is required")
	}
	cmd := execCommand("mg", "show", id, "--json")
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("mg show %s failed: %s (%w)", id, strings.TrimSpace(string(out)), err)
	}
	var item struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return false, fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	return item.Status == "done" || item.Status == "archived", nil
}

// PostMergeWorkTag is how a work item DECLARES that merging its branch is a
// step rather than completion (mg-d86e).
//
// The refinery cannot know whether a merge finishes a ticket; the ticket knows.
// Merging is a good completion signal for the common case, where the branch IS
// the deliverable — and a wrong one whenever real work follows the merge: a
// release that still has to tag and publish, a change that must be verified in
// its installed state, anything with a post-merge external side effect. On
// 2026-07-29 two release items (mg-ca3c, mg-9f17) merged, were marked done by
// pogod, and had their polecats stopped before either reached the tag step. Both
// releases read as complete from every angle and neither existed.
//
// The declaration is a plain tag rather than a new item field for the same
// reasons macguffin's `declares-remainder` is one: `mg list --tag=post-merge-work`
// finds every outstanding one, `mg show` renders it, `mg edit --add-tags` reaches
// an item that already exists, and no schema changes. It composes with
// `declares-remainder` rather than duplicating it — that tag says "something ELSE
// must carry this forward", this one says "THIS item is not finished yet".
//
// Unlike `pogo refinery submit --defer-done`, which the submitting polecat must
// remember to pass, this marker is set by the FILER, at filing time, on the item
// that knows. A polecat that never learns the flag exists still cannot be
// truncated.
const PostMergeWorkTag = "post-merge-work"

// MGWorkItemDeclaresPostMergeWork reports whether a work item carries
// PostMergeWorkTag. pogod's merge-completion path consults it before marking an
// item done and stopping its polecat (mg-d86e).
//
// Matching is case-folded and space-trimmed, as macguffin's own tag predicates
// are: a generous match can at worst leave an item to be completed by the
// polecat itself (the pre-gh #35 behaviour, with a bounded backstop), while a
// stingy one silently truncates a ticket, which is the defect this exists to
// close.
//
// A failing or unparseable lookup returns an error rather than false, and
// callers must treat that as "cannot tell" — never as "no declaration".
func MGWorkItemDeclaresPostMergeWork(id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("work item id is required")
	}
	cmd := execCommand("mg", "show", id, "--json")
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("mg show %s failed: %s (%w)", id, strings.TrimSpace(string(out)), err)
	}
	var item struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return false, fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	for _, t := range item.Tags {
		if strings.EqualFold(strings.TrimSpace(t), PostMergeWorkTag) {
			return true, nil
		}
	}
	return false, nil
}

// execCommand is a variable for testability.
var execCommand = execCommandFunc

func execCommandFunc(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// DiagnoseAgent returns diagnostic information for a specific agent,
// including stall detection, process health, and recent activity.
func DiagnoseAgent(name string) (*agent.DiagnoseInfo, error) {
	r, err := http.Get(serverURL + "/agents/" + name + "/diagnose")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	var info agent.DiagnoseInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// MailLoopReport returns the fleet-wide read of which agents have no mail-check
// schedule — the same judgement DiagnoseAgent reports per agent, asked of every
// agent at once (mg-032b).
//
// A 503 is returned as an error rather than as an empty report: pogod answers
// it when it has no basis to judge, and a caller that rendered that as "nothing
// missing" would be doing exactly what this whole feature exists to prevent.
func MailLoopReport() (*agent.MailLoopReport, error) {
	r, err := http.Get(serverURL + "/agents/mail-loops")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusServiceUnavailable {
		body, _ := io.ReadAll(r.Body)
		return nil, fmt.Errorf("pogod cannot judge mail loops: %s", strings.TrimSpace(string(body)))
	}
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pogod returned %s for /agents/mail-loops", r.Status)
	}
	var rep agent.MailLoopReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// GetAgentOutput returns recent output from an agent.
// If plain is true, ANSI escape sequences are stripped server-side.
func GetAgentOutput(name string, plain bool) (string, error) {
	url := serverURL + "/agents/" + name + "/output"
	if plain {
		url += "?plain=true"
	}
	r, err := http.Get(url)
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
