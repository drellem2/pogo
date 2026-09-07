package refusalwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/refusalstreak"
)

// This file is the half of mg-6f3d that the incident of 2026-09-07 turned from a
// design preference into the finding.
//
// # An alarm with no recipient is not an alarm
//
// mg-3222 was filed that day claiming the wedge detector was slow. It was not:
// it fired in 14m30s and named all six agents and the exact cause. What it could
// not do was reach anybody. Between 11:06:10Z and 14:56:10Z it emitted sixteen
// further wedge_watch_fired, EVERY ONE carrying "routed_to": "nobody". The fleet
// was not short of detection — it detected the outage correctly sixteen times
// over 3h55m. It was short of a path from a correct detection to a person, at
// exactly the moment when the agents that would normally carry a message are the
// thing that has stopped.
//
// So two rules govern everything below:
//
//  1. NO SINK MAY REQUIRE AN AGENT TURN. Every path here is pogod writing to
//     disk. The `human` mailbox is the precedent that worked: the out-of-process
//     com.pogo.deadman launchd job polls it and raises a system notification, and
//     it kept carrying traffic through outages that stopped every agent.
//  2. "IT ALARMED" AND "SOMEONE LEARNED" ARE DIFFERENT QUESTIONS, and only the
//     second one matters. A sink therefore returns a [Receipt] that is CONFIRMED
//     only when the artefact was observed on disk AFTER the send — not when the
//     command exited 0. `mg mail send` exits 0 for a delivery nobody will ever
//     read; the file in <root>/mail/human/new is the thing the notifier polls,
//     and it is what gets stat'd.

// ErrNoRecipient is returned by [Deliver] when no sink confirmed. It is a hard
// error and never a warning: it is the state that produced sixteen accurate
// emissions and one five-and-a-half-hour outage.
var ErrNoRecipient = errors.New("the alarm reached NOBODY: no sink confirmed delivery")

// Receipt is one sink's evidence that it took the alarm.
type Receipt struct {
	// Sink names the channel ("mg-mail:human", "ledger").
	Sink string `json:"sink"`
	// Ref is the artefact: a MSG-ID, a file path. It is what a human follows to
	// find the alarm afterwards.
	Ref string `json:"ref,omitempty"`
	// Confirmed is true only when the artefact was OBSERVED after the write. A
	// receipt that is not confirmed is a claim, and this package does not count
	// claims as delivery.
	Confirmed bool `json:"confirmed"`
	// At is when the confirmation was taken.
	At time.Time `json:"at"`
	// Err is why an unconfirmed receipt is unconfirmed.
	Err string `json:"err,omitempty"`
}

// Alarm is one thing a person must be told.
type Alarm struct {
	// Agents names every agent whose transcript is streaking, sorted. It is a
	// LIST because this class is characteristically fleet-wide — one dead
	// credential is shared by every agent — and per-agent alarms would turn one
	// fact into an N-message storm at the exact moment a human needs to read one
	// clear thing.
	Agents []string
	// Reports is the reading behind each agent, keyed by name.
	Reports map[string]refusalstreak.Report
	// Worst is the agent with the longest run; the subject is written from it.
	Worst string
	// Now is when the alarm was raised.
	Now time.Time
}

// Subject is the line that TRAVELS — into a mail subject, into a notification
// banner, into a ledger line. Everything in it is absolute: no "14m ago", no
// "currently", nothing that becomes a lie the moment it is stored.
func (a Alarm) Subject() string {
	rep := a.Reports[a.Worst]
	who := a.Worst
	if n := len(a.Agents); n > 1 {
		who = fmt.Sprintf("%s +%d more", a.Worst, n-1)
	}
	reason := string(rep.Reason)
	if reason == "" {
		reason = string(refusalstreak.ReasonUnrecognised)
	}
	return fmt.Sprintf("FLEET STOPPED: %d consecutive failing turns (%s) — %s", rep.Streak, reason, who)
}

// Body is the page. It states what was measured, over what span, per agent, and
// what a human is being asked to do — because the remedies differ by mode and
// none of them is anything pogod can perform.
func (a Alarm) Body() string {
	var b strings.Builder
	rep := a.Reports[a.Worst]
	fmt.Fprintf(&b, "%d agent(s) have stopped completing turns.\n\n", len(a.Agents))
	fmt.Fprintf(&b, "%s\n\n", rep.Reason.Human())
	fmt.Fprintf(&b, "Raised at %s.\n\n", a.Now.UTC().Format(time.RFC3339))
	for _, name := range a.Agents {
		r := a.Reports[name]
		fmt.Fprintf(&b, "  %-16s %s\n", name, r.Brief())
		if r.Detail != "" {
			fmt.Fprintf(&b, "  %-16s   %s\n", "", r.Detail)
		}
	}
	b.WriteString("\nWhat this is: every one of those turns was answered LOCALLY by the harness,\n")
	b.WriteString("spending no tokens, flagged as an API error. The processes are alive and\n")
	b.WriteString("`pogo agent list` reports them running with correct uptime — presence is green\n")
	b.WriteString("throughout, which is why nothing else on this box can see it.\n")
	b.WriteString("\nNOTHING WILL RESTART THESE. A new session inherits the same credential, the\n")
	b.WriteString("same limit, the same disabled entitlement. This needs you or the passage of\n")
	b.WriteString("time. `pogo check-refusals` re-reads the transcripts on demand.\n")
	return b.String()
}

// Sink is one out-of-band delivery channel. Implementations must not depend on
// any agent being alive.
type Sink interface {
	// Name identifies the channel in events and receipts.
	Name() string
	// Deliver writes the alarm and returns a receipt. A returned error is a
	// delivery failure; a returned receipt with Confirmed false is the same
	// thing said more precisely, and both are treated as non-delivery.
	Deliver(Alarm) (Receipt, error)
}

// Deliver hands the alarm to every sink and returns every receipt.
//
// It tries ALL sinks rather than stopping at the first success. Two channels
// that fail independently is the only reason to have two, and a chain that
// short-circuits has one channel plus a spare that is never exercised — which
// is how a fallback path is discovered to be broken at the moment it is needed.
//
// It returns ErrNoRecipient when nothing confirmed. It never returns nil for a
// set of unconfirmed receipts: an unconfirmed receipt is the routed_to=nobody
// state, and this function's whole contract is that the caller cannot fail to
// notice it.
func Deliver(sinks []Sink, a Alarm) ([]Receipt, error) {
	var receipts []Receipt
	confirmed := 0
	var failures []string
	for _, s := range sinks {
		if s == nil {
			continue
		}
		r, err := s.Deliver(a)
		if r.Sink == "" {
			r.Sink = s.Name()
		}
		if err != nil {
			r.Confirmed = false
			if r.Err == "" {
				r.Err = err.Error()
			}
		}
		if r.At.IsZero() {
			r.At = a.Now
		}
		if r.Confirmed {
			confirmed++
		} else {
			failures = append(failures, fmt.Sprintf("%s: %s", r.Sink, orUnknown(r.Err)))
		}
		receipts = append(receipts, r)
	}
	if confirmed == 0 {
		if len(failures) == 0 {
			failures = append(failures, "no sinks were configured")
		}
		return receipts, fmt.Errorf("%w (%s)", ErrNoRecipient, strings.Join(failures, "; "))
	}
	return receipts, nil
}

// Confirmed returns the names of the sinks that took the alarm, sorted. It is
// what an event's routed_to carries: a list that can be empty, and an empty one
// is the finding rather than a formatting detail.
func Confirmed(receipts []Receipt) []string {
	var out []string
	for _, r := range receipts {
		if r.Confirmed {
			out = append(out, r.Sink)
		}
	}
	sort.Strings(out)
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unconfirmed, with no reason given"
	}
	return s
}

// ------------------------------------------------------------------ mg mail

// MailSink delivers into a macguffin mailbox — by default `human`, the identity
// the out-of-process apple-side notifier surfaces.
//
// It is the channel with a measured record of working while the fleet was down:
// com.pogo.deadman polls <root>/mail/human/new from launchd, owns no agent, and
// carried notifications through the outages that stopped everything else.
type MailSink struct {
	// Root is the macguffin store. Empty means the live default.
	Root string
	// Bin is the mg binary. Empty means resolved from PATH, then ~/go/bin/mg.
	Bin string
	// To is the recipient. Empty means "human".
	To string
	// From is the sender. Empty means "pogod".
	From string
	// Create passes --create, registering the mailbox. It is for a THROWAWAY
	// store built four lines ago, where every recipient is unknown by
	// construction; against the live store it must stay false, because the
	// refusal of an unknown recipient is what stops a typo minting a dead drop.
	Create bool
	// now is the clock, for tests.
	now func() time.Time
}

func (m MailSink) Name() string { return "mg-mail:" + m.recipient() }

func (m MailSink) recipient() string {
	if m.To != "" {
		return m.To
	}
	return "human"
}

func (m MailSink) sender() string {
	if m.From != "" {
		return m.From
	}
	return "pogod"
}

func (m MailSink) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Deliver sends the alarm and then STATS THE MAILDIR FILE.
//
// The two steps are separate on purpose. `mg mail send` exiting 0 says the
// command ran; the file under <root>/mail/<to>/new is the artefact the notifier
// polls, and only its presence makes this a delivery rather than a claim. That
// distinction is the entire difference between this and the sixteen
// wedge_watch_fired events of 2026-09-07 that each reported themselves sent.
func (m MailSink) Deliver(a Alarm) (Receipt, error) {
	rec := Receipt{Sink: m.Name(), At: m.clock()}
	bin, err := m.binary()
	if err != nil {
		rec.Err = err.Error()
		return rec, err
	}
	root := m.Root
	if root == "" {
		root = DefaultMGRoot()
	}
	if root == "" {
		rec.Err = "no macguffin store root could be resolved (MG_ROOT unset and no home directory)"
		return rec, errors.New(rec.Err)
	}

	args := []string{"--root", root, "mail", "send", m.recipient(),
		"--from", m.sender(), "--subject", a.Subject(), "--body-file", "-", "--json"}
	if m.Create {
		args = append(args, "--create")
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(a.Body())
	// MG_ROOT is set alongside the flag, and POGO_AGENT_NAME is cleared so mg
	// cannot attribute this to whatever agent's environment pogod inherited.
	cmd.Env = append(os.Environ(), "MG_ROOT="+root, "POGO_AGENT_NAME=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		rec.Err = fmt.Sprintf("`mg mail send %s` failed: %v: %s", m.recipient(), err, strings.TrimSpace(string(out)))
		return rec, errors.New(rec.Err)
	}

	var sent struct {
		MsgID string `json:"msg_id"`
		To    string `json:"to"`
	}
	if err := json.Unmarshal([]byte(firstJSONLine(string(out))), &sent); err != nil || sent.MsgID == "" {
		rec.Err = fmt.Sprintf("`mg mail send %s` printed no msg_id to confirm against: %s",
			m.recipient(), strings.TrimSpace(string(out)))
		return rec, errors.New(rec.Err)
	}
	rec.Ref = sent.MsgID

	// The confirmation. mg canonicalises the recipient (it strips an `mg-`
	// prefix), so the box to stat is the one mg says it delivered to, never the
	// one we asked for.
	box := sent.To
	if box == "" {
		box = m.recipient()
	}
	path := filepath.Join(root, "mail", box, "new", sent.MsgID)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		rec.Err = fmt.Sprintf("mg reported msg_id %s delivered to %q, but nothing is at %s — "+
			"the send succeeded and the notifier has nothing to poll", sent.MsgID, box, path)
		return rec, errors.New(rec.Err)
	}
	rec.Ref = path
	rec.Confirmed = true
	return rec, nil
}

func (m MailSink) binary() (string, error) {
	if m.Bin != "" {
		return m.Bin, nil
	}
	return MGBinary()
}

// firstJSONLine returns the first line that looks like a JSON object. mg writes
// warnings to stderr and CombinedOutput interleaves them, so the object is not
// reliably the whole of what comes back.
func firstJSONLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			return line
		}
	}
	return ""
}

// MGBinary resolves the macguffin CLI: PATH first, then the fleet's install
// location, so pogod under launchd — with a minimal PATH — still finds it.
func MGBinary() (string, error) {
	if p, err := exec.LookPath("mg"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, "go", "bin", "mg")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("no `mg` binary on PATH or at ~/go/bin/mg")
}

// DefaultMGRoot resolves the macguffin store the way mg and the dispatch gates
// do: an explicit MG_ROOT wins, then $HOME/.macguffin.
func DefaultMGRoot() string {
	if r := strings.TrimSpace(os.Getenv("MG_ROOT")); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}

// ------------------------------------------------------------------- ledger

// LedgerSink appends the alarm to a plain-text file.
//
// It is the sink with the fewest moving parts on purpose: no subprocess, no
// second binary, no mailbox registry, no notifier. When `mg` is missing or the
// store is unwritable, this is the difference between an alarm that is somewhere
// and an alarm that is nowhere.
//
// It is emphatically NOT a substitute for the mail sink, and the watcher does
// not treat it as one: a file nobody polls is routed_to=nobody with extra steps.
// Its job is to make the mail sink's failure RECONSTRUCTABLE afterwards — the
// question "was this ever raised?" has an answer on disk even when the answer to
// "did anyone see it?" is no.
type LedgerSink struct {
	// Path is the file to append to. Empty means DefaultLedgerPath().
	Path string
	now  func() time.Time
}

func (l LedgerSink) Name() string { return "ledger" }

func (l LedgerSink) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Deliver appends one line and then READS IT BACK. The read-back is the same
// rule as the mail sink's stat: a successful write() to a full or read-only
// filesystem is not an alarm anybody can find.
func (l LedgerSink) Deliver(a Alarm) (Receipt, error) {
	rec := Receipt{Sink: l.Name(), At: l.clock()}
	path := l.Path
	if path == "" {
		path = DefaultLedgerPath()
	}
	if path == "" {
		rec.Err = "no ledger path could be resolved"
		return rec, errors.New(rec.Err)
	}
	rec.Ref = path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		rec.Err = err.Error()
		return rec, err
	}
	line := fmt.Sprintf("%s\t%s\t%s\n", a.Now.UTC().Format(time.RFC3339),
		strings.Join(a.Agents, ","), a.Subject())
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		rec.Err = err.Error()
		return rec, err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		rec.Err = err.Error()
		return rec, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		rec.Err = err.Error()
		return rec, err
	}
	if err := f.Close(); err != nil {
		rec.Err = err.Error()
		return rec, err
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), strings.TrimSuffix(line, "\n")) {
		rec.Err = fmt.Sprintf("the line was written to %s and is not readable back from it", path)
		return rec, errors.New(rec.Err)
	}
	rec.Confirmed = true
	return rec, nil
}

// DefaultLedgerPath is the alarm ledger under the pogo state dir.
func DefaultLedgerPath() string {
	home := pogoHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "alarms", "refusal-streak.log")
}
