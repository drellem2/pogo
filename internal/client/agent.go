package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/workitem"
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
// repo, when non-empty, additionally asks for that repository's worker
// occupancy against the per-repo dispatch cap (mg-3977). The host sample and
// the repo count are independent answers and either can refuse a dispatch on
// its own.
func GetHostLoad(repo string) (*agent.HostLoadResponse, error) {
	u := serverURL + "/hostload"
	if strings.TrimSpace(repo) != "" {
		u += "?repo=" + url.QueryEscape(repo)
	}
	r, err := http.Get(u)
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

// RegisterMGMailbox creates an agent's macguffin mailbox so mail can be
// ADDRESSED to it (`mg mail register <name>`).
//
// It exists because `mg mail send` stopped inventing recipients. Until mg-d639
// a mailbox came into being on first delivery, so "register" was not a concept
// anyone had to hold: every send succeeded, and a typo'd recipient minted a dead
// drop that reported Delivered. mg-d639 replaced that with a refusal
// (no_such_mailbox, exit 3) — the right fix, and the reason this function is
// needed. A name nothing has registered is now unreachable rather than
// silently-reachable-by-nobody.
//
// The alternative spelling is `mg mail send --create`, and it is the wrong one
// for provisioning. --create at a SEND callsite says "deliver to this name
// whether or not anyone meant it", which is precisely the phantom-mailbox
// behaviour mg-d639 removed, re-entered under a new name: a typo in a recipient
// goes back to being invisible. Registering the recipient ahead of any message
// keeps the refusal meaningful — after this, a no_such_mailbox means you typed
// the name wrong, not that the recipient was never provisioned.
//
// IDEMPOTENT: registering an existing mailbox is exit 0 and changes nothing (it
// creates an empty Maildir and never touches mail), so callers may register
// unconditionally and re-register freely.
//
// mg canonicalizes the `mg-` prefix itself — `mg mail register mg-7dc1` creates
// the mailbox `7dc1`, the same box `mg mail list mg-7dc1` and `mg mail list
// 7dc1` both read (verified against mg v0.3.1-dev.19, 2026-08-07). Callers
// therefore need not strip the prefix, and two callers that disagree about it
// cannot end up provisioning two different boxes.
func RegisterMGMailbox(name string) error {
	cmd := execCommand("mg", "mail", "register", name)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg mail register failed: %s (%w)", string(out), err)
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

// ErrMGWorkItemNotDone reports that mg reopen refused because the item was
// never done — it is still claimed and in progress. The refinery reopens a work
// item after a failed merge so the author can retry, and a live polecat's item
// is claimed already, so the refusal means the item is in exactly the state the
// reopen wanted. Callers use errors.Is to log it as an expected outcome instead
// of a failure (mg-5d3f).
var ErrMGWorkItemNotDone = errors.New("work item is not done, it is already claimed (in progress)")

// alreadyClaimedOutput returns mg reopen's COMPLETE output for the "still
// claimed" refusal on the given id. ReopenMGWorkItem compares mg's trimmed
// output to this by exact string equality — the id is the message's only
// variable field, so binding it keeps the comparison a full-message match
// rather than a prefix or a substring.
//
// Deliberately exact, per mg-5d3f: matching loosely (on "reopen failed", or on
// the leading "Error: <id>:") would classify every future reopen refusal as
// benign, including ones that are not. Any other wording — a missing item, a
// corrupt store, a permissions problem — fails this comparison and is reported
// as a failure, which is where an unmeasured outcome belongs.
func alreadyClaimedOutput(id string) string {
	return fmt.Sprintf("Error: %s: not done — it is already claimed (in progress).", id)
}

// ReopenMGWorkItem calls mg reopen to move a done work item back to claimed/.
// Returns nil if the reopen succeeds. Non-fatal errors are returned as errors
// for the caller to log; the "already claimed (in progress)" refusal wraps
// ErrMGWorkItemNotDone so callers can tell it from a real failure.
func ReopenMGWorkItem(id string) error {
	cmd := execCommand("mg", "reopen", id)
	cmd.Stderr = nil
	raw, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.TrimSpace(string(raw))
		if out == alreadyClaimedOutput(id) {
			return fmt.Errorf("%w: %s", ErrMGWorkItemNotDone, out)
		}
		return fmt.Errorf("mg reopen failed: %s (%w)", out, err)
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

// workItemIDRe is the SHAPE of a macguffin work-item id: an `mg-` or `gh-`
// prefix and at least three id characters. It is deliberately a shape test and
// not a store lookup — see LooksLikeWorkItemID.
var workItemIDRe = regexp.MustCompile(`^(?i:(?:mg|gh)-[a-z0-9]{3,})(?:@[0-9]{4}-[0-9]{2})?$`)

// LooksLikeWorkItemID reports whether s is shaped like a work-item id.
//
// It exists because a merge request's `--author` is a free string, and TWO
// different kinds of thing arrive in it: a work-item id (`mg-56ac`) from a
// polecat or a coordinator submitting on an item's behalf, and an AGENT NAME
// (`mayor`, `pm-pogo`) from a crew agent submitting its own work. pogod's merge
// path closes the first and must not touch the second — `mg done mayor` is not a
// completion, it is an error that would be logged on every crew merge forever.
//
// IT IS A SHAPE TEST, NOT AN EXISTENCE TEST, and that is the safe direction.
// Asking the store would mean an `mg show` on the merge path whose FAILURE — a
// slow store, an ambiguous short id, mg missing from PATH — is indistinguishable
// from "not a work item", so a transient error would silently skip the close and
// re-open exactly the window this guards (mg-be37). A shape test cannot fail
// that way: a wrong yes costs one logged `mg done` error, a wrong no costs an
// item left open, and only the shape test makes the second impossible for every
// id the fleet actually issues.
//
// The optional `@YYYY-MM` suffix is mg's partition qualifier, which the store
// requires when two archived items share a short id.
func LooksLikeWorkItemID(s string) bool {
	return workItemIDRe.MatchString(strings.TrimSpace(s))
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

// MGWorkItemReviews returns the id of the BUILD work item that id's review
// covers — the `reviews:` line of its state carrier block — or "" when it
// declares none, which is the ordinary case for every item that is not a
// gh-issue review ticket.
//
// pogod's done-reaper uses it to exempt a builder whose item is already done
// while a review polecat is still running against it (mg-aaf6, gh#131). The
// declaration is written once by the coordinator when it files the review ticket
// and never cleared; see workitem.WorkItem.Reviews for why "never cleared" is
// the design rather than an omission.
//
// It reuses the SAME `mg show --json` shape as MGWorkItemDone and parses the
// body it already carries with the SAME parser that reads the file on disk
// (workitem.ParseCarrier), rather than a second regex that could disagree with
// it about what counts as a declaration.
//
// AN UNREACHABLE CARRIER BLOCK IS AN ERROR, NOT AN ABSENT DECLARATION, and the
// distinction is the point (mg-27d4). "This item declares no review" and "this
// item's declarations are somewhere I cannot read" have opposite meanings for a
// guard, and collapsing them is how a declaration that is plainly visible in
// `mg show` silently fails to protect anything. Callers must treat the error as
// "cannot tell" and log it — that log line is the only thing distinguishing a
// guard correctly not firing from a guard that could not see its input.
func MGWorkItemReviews(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("work item id is required")
	}
	cmd := execCommand("mg", "show", id, "--json")
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mg show %s failed: %s (%w)", id, strings.TrimSpace(string(out)), err)
	}
	var item struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return "", fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	c := workitem.ParseCarrier(item.Body)
	if c.Unreadable {
		return "", fmt.Errorf("mg show %s: carrier block is out of the parser's reach — "+
			"cannot tell whether it declares a review (mg-27d4)", id)
	}
	return c.Reviews, nil
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
