package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
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

// AgentRoster returns the CONFIGURED crew/mayor set compared against pogod's
// registry — the one reading in which an agent that is not running is a row
// rather than a silence (mg-7d20).
//
// It is a separate call from ListAgents rather than more rows in it because
// eight callers consume that array and every one assumes a listed agent has a
// process behind it. A non-200 is an error: a roster pogod could not compute is
// not a complete one, and callers must not render it as though everybody were
// present.
func AgentRoster() (*agent.RosterReport, error) {
	r, err := http.Get(serverURL + "/agents/roster")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = r.Status
		}
		return nil, fmt.Errorf("roster unavailable: %s", msg)
	}
	var rep agent.RosterReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		return nil, err
	}
	return &rep, nil
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
	// The path lives under /agents on purpose. pogod mounts the agent sub-mux
	// under a fixed set of prefixes, and this endpoint spent two weeks at the
	// unmounted "/hostload", where every call 404'd (mg-c26d).
	u := serverURL + "/agents/hostload"
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
	return completeMGWorkItem(id, resultJSON, "")
}

// completeMGWorkItem is CompleteMGWorkItem with the `--successor` the caller
// may have resolved. It is unexported because only CloseMGWorkItemAtMerge is in
// a position to resolve one: the flag is meaningful for exactly the items that
// declare a remainder, and knowing that takes a store read the plain completion
// path deliberately does not make.
func completeMGWorkItem(id, resultJSON, successor string) error {
	args := []string{"done", id}
	if resultJSON != "" {
		args = append(args, "--result="+resultJSON)
	}
	if successor != "" {
		args = append(args, "--successor="+successor)
	}
	cmd := execCommand("mg", args...)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg done failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ErrMGWorkItemAlreadyDone reports that `mg done` did not apply BECAUSE the item
// is already terminal — the worker wrote its own result first, and that result
// stands. The close the caller wanted happened; somebody else performed it.
//
// It is the only refusal a caller may read as "the item is closed". Everything
// else CloseMGWorkItemAtMerge returns means the item is still open.
var ErrMGWorkItemAlreadyDone = errors.New("work item is already done")

// ErrMGWorkItemGated reports that the close was DECLINED rather than attempted:
// the item is unclaimed and assigned to a non-dispatchable executive (`human`,
// `parked`, `blocked:<agent>`), so nobody is working it and a merged branch is
// not evidence that it is finished. See CloseMGWorkItemAtMerge.
var ErrMGWorkItemGated = errors.New("work item is gated and was deliberately not closed")

// ErrMGRemainderNoSuccessorFiled reports that the close was refused because the
// item declares a remainder, names no successor, AND NOTHING IN THE STORE NAMES
// IT AS A PREDECESSOR — so there is no successor to resolve and none was ever
// filed. This is the "the worker skipped the step" cause.
var ErrMGRemainderNoSuccessorFiled = errors.New("item declares a remainder and NO item in the store names it as a predecessor, so no successor was ever filed")

// ErrMGRemainderAmbiguousSuccessor reports that the close was refused because
// SEVERAL items name this one as their predecessor and the item itself names
// none of them as its successor. A predecessor edge is not proof of succession —
// see resolveSuccessorFromStore — so pogod declines to guess which child carries
// the remainder, and says which ones it was choosing between.
var ErrMGRemainderAmbiguousSuccessor = errors.New("item declares a remainder and SEVERAL items name it as their predecessor, so the successor cannot be resolved without guessing")

// mgItemFacts is the part of `mg show --json` the merge close reads. Status and
// assignee decide whether the close may proceed at all; DeclaresRemainder and
// Successor decide whether `mg done` will refuse it for want of a successor.
type mgItemFacts struct {
	Status            string   `json:"status"`
	Assignee          string   `json:"assignee"`
	DeclaresRemainder bool     `json:"declares_remainder"`
	Successor         []string `json:"successor"`
}

// mgWorkItemFacts reads the fields of one work item that the merge close keys on.
func mgWorkItemFacts(id string) (mgItemFacts, error) {
	var item mgItemFacts
	out, err := mgShowJSON(id)
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return item, fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	return item, nil
}

// successorCandidate is one item that names the closing item as its predecessor.
// Created is carried so an ambiguous refusal can order the candidates for the
// human who has to pick between them — see resolveSuccessorFromStore.
type successorCandidate struct {
	ID      string `json:"id"`
	Created string `json:"created"`
}

// resolveSuccessorFromStore returns every item that names id in its
// `predecessor` field — the candidates for "the item that carries id's remainder
// forward" — newest first.
//
// # Why this exists
//
// `mg done` refuses an item tagged `declares-remainder` that names no successor,
// and that refusal is correct: it is the only thing standing between a
// recommendation and its silent discard. But on the merge path the two halves of
// satisfying it are held by different processes. The WORKER files the successor
// before it submits and then exits; POGOD performs the close at merge time and
// has never been told the id. Neither party can close the item and each has done
// its half correctly, which is why every declares-remainder item merged on the
// night of 2026-08-13 — four of four, mg-fa83/mg-bdc0/mg-365a/mg-cd8d — bounced
// back to available/ carrying a successor that already existed (mg-27c0).
//
// The link is already in the store: `mg done --successor` writes it on BOTH
// ends, so the child carries `predecessor:<parent>` from the moment it is filed.
// Nothing has to be passed anywhere; it has to be LOOKED UP.
//
// # WHY IT RETURNS CANDIDATES AND NOT AN ANSWER
//
// "an item whose predecessor names the closing item IS its successor" holds in
// every case that motivated this, and it is NOT a rule that can be applied
// blind. Measured over the live store on 2026-08-14: of 41 items named as a
// predecessor, 10 (24%) are named by TWO children, and in every one of those the
// parent's own `successor` field names exactly ONE of the two. So a predecessor
// edge is genuinely weaker than succession, a parent legitimately has several
// children, and a resolver that took the first match would pick the wrong one
// about half the time in a quarter of the population.
//
// Hence: this returns the set, and the caller resolves only when the set has
// exactly one member. Ambiguity is reported, never broken by a tiebreak — a
// wrong successor tag gates a live item on a ticket that will never carry the
// work, which is worse than the open item it would have replaced, and mg cannot
// catch it because a real-but-wrong id is a legal argument.
//
// # THE TIEBREAK THAT WAS MEASURED AND DELIBERATELY NOT TAKEN
//
// Two store facts were checked against those 10 ambiguous parents before
// settling on "refuse", because this function's own remedy is subject to the
// defect it remedies: declining while the answer sits unread in the store is the
// very complaint mg-27c0 was filed about.
//
//   - The `<parent>-successor` tag does NOT disambiguate. Both children carry it
//     in 9 of the 10 cases, so it marks membership in the chain, not succession.
//   - Creation order DOES, in every case checked: the parent's own `successor`
//     field named the MOST RECENTLY CREATED child 10 times out of 10.
//
// The second is not applied, and the reason is the denominator rather than the
// hit rate. All 10 are `onethird` chains from a single night, so it is one
// workflow observed ten times, not ten independent observations — and the
// mechanism that would break it is ordinary: any follow-up child filed after the
// merge and before the close is retried becomes "most recent" and would be
// linked silently. A guess that is right 100% of the time on one night's data
// and wrong invisibly is worse than a refusal that costs a coordinator one
// glance, which is why the candidates are merely ORDERED newest-first and the
// finding is stated in the refusal for the human who does have the context.
//
// # Why the field and not the tag
//
// The edge is visible as a `predecessor:<id>` tag too, and `mg list --tag=` would
// filter it server-side. mg's own help says not to: it carries `successor`,
// `predecessor` and `declares_remainder` as structured fields "so a check does
// not have to know the tag spelling to be written". A whole-store read costs
// ~0.06s against 2,889 items, which is not worth binding this to a spelling.
func resolveSuccessorFromStore(id string) ([]successorCandidate, error) {
	cmd := execCommand("mg", "list", "--all", "--json")
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("mg list --all --json: %s (%w)", detail, err)
	}
	// --all is required and not defensive: an archived or shelved child is still
	// a child, and hiding one turns a two-candidate ambiguity into a
	// one-candidate "resolution" that is a guess wearing a certainty.
	var candidates []successorCandidate
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			ID          string   `json:"id"`
			Created     string   `json:"created"`
			Predecessor []string `json:"predecessor"`
		}
		// One unparseable line is not a reason to abandon the scan, but it IS a
		// reason not to report a count: a dropped line can only ever turn an
		// ambiguity into a false resolution, so a parse failure fails the whole
		// lookup rather than silently shrinking the candidate set.
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("mg list --all --json: unparseable line %q: %w", line, err)
		}
		// An item is never its own successor — mg refuses one, and a self-edge
		// would also mask a genuine second candidate by making the set look
		// resolvable. Matched the same way as the edge below, so the two cannot
		// disagree about what counts as the same id.
		if strings.EqualFold(strings.TrimSpace(item.ID), id) {
			continue
		}
		for _, p := range item.Predecessor {
			if strings.EqualFold(strings.TrimSpace(p), id) {
				candidates = append(candidates, successorCandidate{ID: item.ID, Created: item.Created})
				break
			}
		}
	}
	// Newest first. RFC3339 sorts lexically, and an item with no `created` sorts
	// last rather than being dropped — the ordering is a convenience for a human
	// reading a refusal, so it must never decide which candidates are reported.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Created > candidates[j].Created })
	return candidates, nil
}

// describeCandidates renders the ambiguous set for the human who has to choose,
// newest first and with the creation stamps that order them.
func describeCandidates(candidates []successorCandidate) string {
	parts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.Created == "" {
			parts = append(parts, c.ID+" (created unknown)")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (created %s)", c.ID, c.Created))
	}
	return strings.Join(parts, ", ")
}

// CloseMGWorkItemAtMerge closes a work item whose branch has just merged, and
// REPORTS WHETHER IT IS CLOSED. It is what pogod's OnMerged reap calls instead of
// CompleteMGWorkItem (mg-2b71).
//
// # THE DEFECT
//
// `mg done` refuses an item that is not in claimed/ (exit 4, "not claimed, so it
// cannot be completed"). mg-be37 added the close-at-merge path for exactly the
// case where no polecat is running — a branch submitted by hand, whose item is
// therefore usually NOT claimed — so that path failed by construction in the case
// it was built for. It ran `mg done`, captured the refusal, logged it verbatim,
// and then mailed the filer that the item had COMPLETED. Reproduced live on
// 2026-08-13 against mg-479c: `mg show` said `status=available, assignee=parked`
// while pm-onethird held a mail asserting the item was closed.
//
// # WHAT THIS DOES ABOUT IT
//
// Three decisions, in the order they are made, each keyed on a fact READ FROM THE
// STORE rather than on the wording of mg's refusal:
//
//   - ALREADY TERMINAL -> ErrMGWorkItemAlreadyDone, without running `mg done`.
//     The worker won the race; its result stands and must not be replaced.
//   - UNCLAIMED AND GATED -> ErrMGWorkItemGated, without claiming or closing.
//     A `parked`/`human`/`blocked:` item that nobody holds is work somebody
//     deliberately stopped; merging a hand-submitted branch is not a decision
//     that it is finished, and it is not this daemon's decision to make. This is
//     the mg-479c case, and leaving the item alone is what actually happened
//     there — the defect was the report, not the outcome.
//   - UNCLAIMED AND DISPATCHABLE -> claim it, then close it. This is what makes
//     mg-be37 work as intended rather than merely reporting that it did: the
//     stranded branch of a dead polecat leaves its item in available/, where
//     priority-wake advertises work that is already on the target.
//
// The gate is read off `assignee` only. `mg show --json` carries no stage/carrier
// field — that state is pogod's own parse of the body — so config.IsStageGated
// has nothing to read here and is deliberately not consulted.
//
// A store that cannot be READ is not a licence to skip the close: the probe's
// failure falls through to the plain `mg done`, whose own outcome is then
// classified below. The one thing that never happens is a close reported as
// having applied when it did not.
func CloseMGWorkItemAtMerge(id, resultJSON string) error {
	claimNote, tookClaim := "", false
	facts, probeErr := mgWorkItemFacts(id)
	status, assignee := facts.Status, facts.Assignee
	if probeErr == nil {
		switch {
		case status == "done" || status == "archived":
			return fmt.Errorf("%w: mg show %s reports status=%s, so there is nothing left to close", ErrMGWorkItemAlreadyDone, id, status)
		case status == "available" && config.IsDispatchGated(assignee, nil):
			return fmt.Errorf("%w: %s is unclaimed and assigned to %q — no worker holds it, so its branch merging is not evidence that it is finished",
				ErrMGWorkItemGated, id, strings.TrimSpace(assignee))
		case status == "available":
			// The claim `mg done` requires. pogod's pid is the honest owner of
			// record for the same reason it is at spawn (mg-7254): pogod is the
			// process taking the claim and there is no worker whose pid to name.
			if out, err := execCommand("mg", "claim", id, "--pid", strconv.Itoa(os.Getpid())).CombinedOutput(); err != nil {
				// Not fatal on its own — the item may have been claimed by
				// somebody else between the probe and here, in which case the
				// close below still applies. Carried forward so that if the
				// close DOES fail, the report says why it could not be taken.
				claimNote = fmt.Sprintf(" (claiming it first also failed: %s)", strings.TrimSpace(string(out)))
			} else {
				tookClaim = true
			}
		}
	}

	// RESOLVE THE SUCCESSOR THE WORKER ALREADY FILED (mg-27c0). An item that
	// declares a remainder and names no successor is one `mg done` will refuse,
	// and on the merge path the id it wants is in the store already — the worker
	// filed the child before submitting, and `mg done --successor` wrote the
	// reverse link, so the child carries `predecessor:<this item>`.
	//
	// THE REFUSAL IS NOT WEAKENED, and that is deliberate. Nothing below skips
	// `mg done`, suppresses its exit status, or closes an item mg declined to
	// close. The only thing that changes is whether pogod can SUPPLY the
	// argument mg is asking for. When it cannot, mg refuses exactly as before
	// and the refusal is annotated with WHICH of the two causes applied —
	// nothing filed, or several candidates and no way to choose — because those
	// were previously indistinguishable without reading the result sidecar by
	// hand, which is how mg-5058 (a genuinely successorless item) looked
	// identical to four items whose links merely went unstated.
	successor, remainderNote := "", ""
	var remainderCause error
	if probeErr == nil && facts.DeclaresRemainder && len(facts.Successor) == 0 {
		candidates, rerr := resolveSuccessorFromStore(id)
		switch {
		case rerr != nil:
			remainderNote = fmt.Sprintf(" (%s declares a remainder and names no successor; the store could not be searched for one: %v)", id, rerr)
		case len(candidates) == 1:
			successor = candidates[0].ID
			remainderNote = fmt.Sprintf(" (--successor=%s was NOT supplied by the author: %s declares a remainder, named no successor, "+
				"and %s is the only item in the store naming %s as its predecessor, so the refinery resolved the link the author had "+
				"already filed — mg-27c0)", successor, id, successor, id)
		case len(candidates) == 0:
			remainderCause = ErrMGRemainderNoSuccessorFiled
			remainderNote = fmt.Sprintf(" (%s declares a remainder, names no successor, and NO item in the store names it as a predecessor — "+
				"nothing was filed to carry its remainder forward, so this is not a lost link but missing work; file the successor, then "+
				"`mg claim %s; mg done %s --successor=<id>`)", id, id, id)
		default:
			remainderCause = ErrMGRemainderAmbiguousSuccessor
			remainderNote = fmt.Sprintf(" (%s declares a remainder and names no successor; %d items name it as their predecessor — %s — "+
				"and a predecessor edge is not proof of succession, so the refinery declined to guess. Listed newest first: over the 10 "+
				"ambiguous parents in the live store on 2026-08-14 the most recently created child was the right successor 10 times out "+
				"of 10, but all 10 are one workflow's chains from one night, so that is a hint for you and not a rule this code applies. "+
				"Pick the one that carries the remainder and run `mg claim %s; mg done %s --successor=<id>`)",
				id, len(candidates), describeCandidates(candidates), id, id)
		}
	}

	// The RESOLUTION IS RECORDED WHERE IT SURVIVES, and that is the sidecar
	// rather than a log line or this function's return. A link pogod inferred
	// and one the author stated leave the store in the same state — two tags,
	// on both ends — and the inferred half is the one that can be wrong, so a
	// reader with no way to tell them apart has no way to audit it.
	//
	// It goes NEXT TO `completed_by: refinery`, which is the existing record of
	// "the refinery did this, not the worker", and it never displaces the
	// caller's payload: a result that is empty or is not a JSON object is passed
	// through untouched, because a provenance note is not worth breaking a
	// verdict for.
	if successor != "" {
		resultJSON = withResolvedSuccessor(resultJSON, successor)
	}

	err := completeMGWorkItem(id, resultJSON, successor)
	if err == nil {
		return nil
	}
	// ASK THE STORE, DO NOT READ THE MESSAGE. "already done" and "not claimed"
	// are both exit 4 (macguffin's frozen conflict code covers every wrong-state
	// refusal), so the exit status cannot separate them and prose matching breaks
	// the first time a sentence is reworded. The question that matters is not
	// which error mg printed but whether the item is closed, and only the store
	// answers that.
	if done, perr := MGWorkItemDone(id); perr == nil && done {
		return fmt.Errorf("%w: %v", ErrMGWorkItemAlreadyDone, err)
	}
	// ROLL THE CLAIM BACK. This function's own remedy is subject to the defect
	// it remedies: the claim above was taken for one purpose, that purpose has
	// just failed, and an item left in claimed/ under pogod's pid with no worker
	// is STRICTLY WORSE than the open item this started with. available/ is
	// where stall-watch looks and where dispatch reads; claimed/ under a pid
	// that will never work it is the mg-fb13 shape, invisible to both.
	//
	// Only when this call took the claim. A claim somebody else holds is not
	// ours to release, and releasing it is how a live worker loses its item.
	if tookClaim {
		if out, uerr := execCommand("mg", "unclaim", id).CombinedOutput(); uerr != nil {
			claimNote += fmt.Sprintf(" (and the claim pogod took in order to close it could NOT be released: %s — "+
				"%s is now STRANDED in claimed/ under pid %d, where dispatch will not see it and stall-watch does not "+
				"look; release it by hand with `mg unclaim %s`)", strings.TrimSpace(string(out)), id, os.Getpid(), id)
		} else {
			claimNote += fmt.Sprintf(" (the claim pogod took in order to close it has been released; %s is back in available/ "+
				"exactly as it was before this attempt)", id)
		}
	}
	// NAME THE CAUSE, when the store answered which one it was. mg's own
	// refusal is carried verbatim either way — this only prefixes a sentinel a
	// caller can branch on, and prose a reader can act on without opening the
	// result sidecar by hand.
	if remainderCause != nil {
		return fmt.Errorf("%w: %w%s%s", remainderCause, err, remainderNote, claimNote)
	}
	return fmt.Errorf("%w%s%s", err, remainderNote, claimNote)
}

// withResolvedSuccessor records in the result sidecar that the successor link
// was RESOLVED BY THE REFINERY rather than named by the item's author, beside
// the `completed_by` key that already says who performed the close.
//
// It is deliberately total: a result that is empty, or is not a JSON object, is
// returned unchanged. The provenance note is worth having and is not worth
// corrupting a worker's verdict for, and this function's failure mode has to be
// "the note is missing" rather than "the payload is".
func withResolvedSuccessor(resultJSON, successor string) string {
	if strings.TrimSpace(resultJSON) == "" {
		return resultJSON
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &obj); err != nil || obj == nil {
		return resultJSON
	}
	obj["successor_resolved_by"] = "refinery"
	obj["successor_resolved"] = successor
	out, err := json.Marshal(obj)
	if err != nil {
		return resultJSON
	}
	return string(out)
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
	out, err := mgShowJSON(id)
	if err != nil {
		return false, err
	}
	var item struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return false, fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	return item.Status == "done" || item.Status == "archived", nil
}

// mgShowJSON runs `mg show <id> --json` and returns its STDOUT.
//
// STDOUT ONLY, AND THAT IS THE WHOLE POINT OF THE HELPER. These probes used
// `CombinedOutput`, which merges stderr into the buffer that then goes to
// `json.Unmarshal`. `mg show --json` writes valid JSON to stdout and exits 0
// while ALSO writing an advisory line to stderr whenever a live id happens to
// name an archived item too:
//
//	note: mg-4b2a also names an archived item at work/archive/2026-04/mg-4b2a.md
//	{ "id": "mg-4b2a", ... }
//
// Merged, that fails to parse — `invalid character 'o' in literal null` — for an
// item the store answered perfectly. Every caller here reads a parse failure as
// "cannot tell" and falls back, so the probe silently stopped working for those
// items while the store was healthy the whole time.
//
// It is not rare and it grows. Swept over the live store on 2026-08-12, 4 of 545
// live items collide with an archived id (0.7%); against 2,176 archived ids in a
// 4-hex-digit space, about 3% of newly-filed items will land on one, and the
// archive only grows. For the mg-aaf6 review exemption the consequence is that
// the guard never fires on ANY tick for an affected ticket, and the natural
// diagnosis — "the coordinator must have forgotten the `reviews:` line" — points
// away from the cause.
//
// stderr is still captured, via exec's ExitError, so a genuine failure reports
// what mg said rather than an empty string. It just never reaches the parser.
func mgShowJSON(id string) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("work item id is required")
	}
	cmd := execCommand("mg", "show", id, "--json")
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("mg show %s failed: %s (%w)", id, detail, err)
	}
	return out, nil
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
	out, err := mgShowJSON(id)
	if err != nil {
		return false, err
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

// MGWorkItemFiling returns the CREATOR and the TITLE of a work item — who
// commissioned it and what they asked for.
//
// pogod's completion notifier uses it to answer the question nothing in the
// completion path used to ask: who is waiting for this? Until mg-f120 the
// refinery mailed the coordinator, pogod closed the item, the coordinator
// archived it, and the agent that FILED it was told by nobody — it learned only
// if the worker volunteered a mail, which made the omission invisible in exactly
// the cases where it mattered.
//
// Creator is the `creator:` frontmatter field, which mg surfaces in `mg show
// --json`; it is an AGENT NAME (`pm-onethird`, `mayor`, or a polecat's bare
// name), never a work-item id. An item filed before mg recorded creators comes
// back with an empty creator and no error — absent is not a failure, and a
// caller must be able to tell "nobody is recorded as waiting" from "the store
// could not be read".
func MGWorkItemFiling(id string) (creator, title string, err error) {
	out, err := mgShowJSON(id)
	if err != nil {
		return "", "", err
	}
	var item struct {
		Creator string `json:"creator"`
		Title   string `json:"title"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return "", "", fmt.Errorf("mg show %s: unparseable JSON: %w", id, err)
	}
	return strings.TrimSpace(item.Creator), strings.TrimSpace(item.Title), nil
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
	out, err := mgShowJSON(id)
	if err != nil {
		return "", err
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

// AgentOutputOptions selects how much of an agent's PTY ring to retrieve, and
// in what form. The zero value asks for the server's default window
// (agent.DefaultOutputBytes) with escape sequences intact.
//
// Bytes and Lines are mutually exclusive; sending both is rejected by pogod.
type AgentOutputOptions struct {
	// Plain strips ANSI escape sequences server-side.
	Plain bool
	// Bytes, when > 0, requests the last N bytes. pogod clamps it to the
	// ring's capacity (agent.OutputRingBytes, 64KB), so a caller that wants
	// everything retained can name a large number rather than guess.
	Bytes int
	// Lines, when > 0, requests the last N newline-separated lines out of the
	// whole retained ring.
	Lines int
}

// GetAgentOutput returns recent output from an agent.
func GetAgentOutput(name string, opts AgentOutputOptions) (string, error) {
	q := url.Values{}
	if opts.Plain {
		q.Set("plain", "true")
	}
	if opts.Bytes > 0 {
		q.Set("bytes", strconv.Itoa(opts.Bytes))
	}
	if opts.Lines > 0 {
		q.Set("lines", strconv.Itoa(opts.Lines))
	}
	u := serverURL + "/agents/" + name + "/output"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.Get(u)
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
	// Any other non-200 is an error, not output. Returning the body as if it
	// were PTY content printed pogod's rejection where the agent's screen
	// belongs and exited 0 — the same accepted-and-ignored shape mg-8a56 was
	// filed about, one layer up.
	if r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pogod returned %s for %s: %s", r.Status, u, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}
