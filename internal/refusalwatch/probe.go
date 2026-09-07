package refusalwatch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/refusalstreak"
)

// # THE FLEET-DOWN PROBE
//
// A notification mechanism verified against a healthy fleet is verified in the
// one condition where it is not needed. That is the acceptance criterion mg-6f3d
// was filed with, and it is the one most likely to be quietly satisfied the easy
// way: ship an alarm, watch it arrive while everything is fine, believe it works.
// That would reproduce 2026-09-07 exactly — accurate emissions, a 5h30m outage,
// and Daniel finding out when he looked — except this time with the belief that
// the channel had been tested, which is strictly worse than today, because today
// at least the silence was honest.
//
// So this probe constructs the delivery in the DOWN condition and nowhere else:
//
//   - a throwaway macguffin store with NO agents, no registry, no work items, and
//     nothing running that could carry a message;
//   - the alarm delivered by the same code path pogod uses, driving the same real
//     `mg` binary;
//   - the receipt confirmed by STATTING the maildir file the out-of-process
//     notifier polls, not by the sender's exit status.
//
// And it is a MATCHED PAIR, because an arm that only ever passes proves nothing
// about whether it can fail. The negative arm sends to a mailbox nobody has
// registered — the state a typo'd or renamed recipient leaves the channel in —
// and demands a REFUSAL. If both arms go green, the confirmation step is not
// confirming anything, which is the specific way an instrument becomes green
// because it cannot see rather than because there is nothing to see.
//
// # Why this lives in non-test code
//
// Same reason as internal/verdictwatch's: so it can be run at RUNTIME, on a
// deployed box, with no Go toolchain, by an operator looking at a quiet alarm
// channel and wondering whether to believe it. `pogo check-refusals --probe`.
// It is also exercised by `go test ./...`, which build.sh runs, which the
// refinery runs on every merge — so a future correct change that breaks the
// channel turns the next pogo merge red by name.
//
// A SETUP FAILURE IS NOT A PASS. It sets Blind, which renders as INSTRUMENT
// FAILURE everywhere this result is shown.

// ProbeArm is one constructed condition and what the channel did with it.
type ProbeArm struct {
	// Name states what was CONSTRUCTED, in the words of the input rather than
	// the expected output.
	Name string `json:"name"`
	// Want and Got are the delivery outcome.
	Want string `json:"want"`
	Got  string `json:"got"`
	OK   bool   `json:"ok"`
	// Detail carries the receipt's own words when an arm misses.
	Detail string `json:"detail,omitempty"`
}

// ProbeResult is one full run.
type ProbeResult struct {
	// Store is the throwaway macguffin root the arms were built in. Removed
	// before Probe returns; reported so a failure names where it happened.
	Store string `json:"store"`
	// MG is the binary that was driven.
	MG   string     `json:"mg"`
	Arms []ProbeArm `json:"arms,omitempty"`
	// Blind is set when the probe could not be CONSTRUCTED at all. Neither a
	// pass nor a failure: it is the probe reporting that it measured nothing.
	Blind string `json:"blind,omitempty"`
}

// InstrumentFailure reports whether the probe could not be constructed.
func (p ProbeResult) InstrumentFailure() bool { return p.Blind != "" }

// Passed reports whether every constructed arm landed on its expectation. A
// blind run has not passed.
func (p ProbeResult) Passed() bool {
	if p.InstrumentFailure() || len(p.Arms) == 0 {
		return false
	}
	for _, a := range p.Arms {
		if !a.OK {
			return false
		}
	}
	return true
}

// Failures returns the arms that missed.
func (p ProbeResult) Failures() []ProbeArm {
	var out []ProbeArm
	for _, a := range p.Arms {
		if !a.OK {
			out = append(out, a)
		}
	}
	return out
}

// Render formats the probe for human reading.
func (p ProbeResult) Render() string {
	var b strings.Builder
	if p.InstrumentFailure() {
		fmt.Fprintf(&b, "INSTRUMENT FAILURE — the fleet-down probe could not be BUILT, so it\n"+
			"proved nothing either way:\n\n  %s\n\n"+
			"This is not a verdict on the alarm channel. Until it is green, the answer to\n"+
			"\"would anybody hear this?\" is unknown.\n", p.Blind)
		return b.String()
	}
	fmt.Fprintf(&b, "fleet-down alarm probe — mg %s — throwaway store %s\n\n", p.MG, p.Store)
	for _, a := range p.Arms {
		mark := "PASS"
		if !a.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", mark, a.Name)
		fmt.Fprintf(&b, "         want %s, got %s\n", a.Want, a.Got)
		if a.Detail != "" {
			fmt.Fprintf(&b, "         %s\n", a.Detail)
		}
	}
	if !p.Passed() {
		fmt.Fprintf(&b, "\n%d arm(s) missed. The alarm channel cannot be trusted to reach anybody\n"+
			"until this is green — which is the exact state the fleet was in on 2026-09-07,\n"+
			"when sixteen correct detections were routed to nobody.\n", len(p.Failures()))
	}
	return b.String()
}

// probeAlarm is the fixture alarm. It is shaped like the real one — a run of
// three, the entitlement mode, a fleet-wide roster — so the arms exercise the
// same Subject/Body rendering the live path uses.
func probeAlarm(now time.Time) Alarm {
	rep := refusalstreak.Report{
		State:     refusalstreak.StateStreaking,
		Streak:    3,
		Reason:    refusalstreak.ReasonEntitlement,
		Reasons:   map[refusalstreak.Reason]int{refusalstreak.ReasonEntitlement: 3},
		Detail:    "Your organization has disabled Claude subscription access for Claude Code",
		First:     now.Add(-11 * time.Minute),
		Last:      now,
		Turns:     3,
		MinStreak: refusalstreak.DefaultMinStreak,
		ScannedAt: now,
	}
	return Alarm{
		Agents:  []string{"mayor", "pm-pogo"},
		Reports: map[string]refusalstreak.Report{"mayor": rep, "pm-pogo": rep},
		Worst:   "mayor",
		Now:     now,
	}
}

// Probe constructs the alarm channel in the DOWN condition and checks it.
//
// The store is removed before this returns.
func Probe() ProbeResult {
	res := ProbeResult{}
	bin, err := mgLookup()
	if err != nil {
		res.Blind = err.Error() + "; the fleet-down probe drives the real macguffin CLI and cannot be built without it"
		return res
	}
	res.MG = bin

	dir, err := os.MkdirTemp("", "refusalwatch-probe-")
	if err != nil {
		res.Blind = fmt.Sprintf("could not create a throwaway store: %v", err)
		return res
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "macguffin")
	res.Store = root
	if err := os.MkdirAll(root, 0o755); err != nil {
		res.Blind = fmt.Sprintf("could not create a throwaway store: %v", err)
		return res
	}
	if out, err := runMG(bin, root, "init"); err != nil {
		res.Blind = fmt.Sprintf("`mg init` on the throwaway store: %v\n%s", err, out)
		return res
	}

	now := time.Now().UTC()
	alarm := probeAlarm(now)

	// -------- ARM A: the acceptance criterion, stated as a construction.
	//
	// A store with NO agents in it — no registry, no work items, nothing running
	// — and a `human` mailbox registered the way `pogo service` provisions one.
	// This is the fleet-down condition: the only thing that can carry the alarm
	// is pogod writing a file.
	if out, err := runMG(bin, root, "mail", "register", "human"); err != nil {
		res.Blind = fmt.Sprintf("could not register the `human` mailbox on the throwaway store: %v\n%s", err, out)
		return res
	}
	mail := MailSink{Root: root, Bin: bin, To: "human", From: "pogod"}
	recA, errA := mail.Deliver(alarm)
	res.Arms = append(res.Arms, deliveryArm(
		"FLEET DOWN: no agents exist at all, and the alarm is delivered to `human`",
		true, recA, errA))

	// The receipt is not enough on its own — it is this package's own claim. The
	// file the out-of-process notifier polls is the independent fact.
	landed := ""
	if recA.Confirmed {
		if b, err := os.ReadFile(recA.Ref); err == nil {
			landed = string(b)
		}
	}
	hasSubject := strings.Contains(landed, "FLEET STOPPED")
	res.Arms = append(res.Arms, ProbeArm{
		Name: "the delivered bytes are in the maildir the notifier polls, carrying the subject",
		Want: "subject present in <root>/mail/human/new/<msg-id>",
		Got:  presence(hasSubject),
		OK:   hasSubject,
		Detail: "the receipt is this package's claim; the file is the notifier's input. " +
			"On 2026-09-07 sixteen findings reported themselves sent.",
	})

	// -------- ARM B: the matched control.
	//
	// The same code, the same store, a recipient nobody registered. mg refuses an
	// unknown recipient (mg-d639) and the sink must report NOT CONFIRMED. If this
	// arm goes green, arm A is green for a reason that has nothing to do with
	// delivery, and the whole probe is decoration.
	ghost := MailSink{Root: root, Bin: bin, To: "nobody-registered-this", From: "pogod"}
	recB, errB := ghost.Deliver(alarm)
	res.Arms = append(res.Arms, deliveryArm(
		"CONTROL: the same send to a mailbox nobody registered must be REFUSED",
		false, recB, errB))

	// -------- ARM C: the ledger, which needs no mg and no mailbox at all.
	ledgerPath := filepath.Join(dir, "alarms", "refusal-streak.log")
	recC, errC := LedgerSink{Path: ledgerPath}.Deliver(alarm)
	res.Arms = append(res.Arms, deliveryArm(
		"the ledger sink writes with no `mg` and no mailbox in the path",
		true, recC, errC))

	// -------- ARM D: the ledger's own control.
	//
	// A path under a REGULAR FILE cannot be created, so this exercises the
	// write-failure branch rather than asserting it in a comment.
	blocked := filepath.Join(ledgerPath, "cannot", "exist.log")
	recD, errD := LedgerSink{Path: blocked}.Deliver(alarm)
	res.Arms = append(res.Arms, deliveryArm(
		"CONTROL: a ledger path that cannot be created must NOT report confirmed",
		false, recD, errD))

	// -------- ARM E: the routed_to=nobody path itself.
	//
	// Every sink failing must return ErrNoRecipient. This is the branch that was
	// live for 3h55m on 2026-09-07, and a package that only ever exercised its
	// success path would be asserting the same thing that incident already
	// disproved.
	_, errE := Deliver([]Sink{ghost, LedgerSink{Path: blocked}}, alarm)
	nobody := errors.Is(errE, ErrNoRecipient)
	res.Arms = append(res.Arms, ProbeArm{
		Name: "CONTROL: with EVERY sink failing, Deliver returns ErrNoRecipient rather than nil",
		Want: "ErrNoRecipient",
		Got:  errString(errE),
		OK:   nobody,
		Detail: "an alarm that reached nobody must not be indistinguishable from one that " +
			"reached somebody; that indistinguishability is the whole of mg-3222",
	})

	return res
}

func deliveryArm(name string, wantConfirmed bool, rec Receipt, err error) ProbeArm {
	got := rec.Confirmed && err == nil
	arm := ProbeArm{
		Name: name,
		Want: confirmation(wantConfirmed),
		Got:  confirmation(got),
		OK:   got == wantConfirmed,
	}
	if !arm.OK || rec.Err != "" {
		arm.Detail = rec.Err
	}
	if arm.Detail == "" && err != nil {
		arm.Detail = err.Error()
	}
	return arm
}

func confirmation(ok bool) string {
	if ok {
		return "CONFIRMED delivered"
	}
	return "NOT delivered"
}

func presence(ok bool) string {
	if ok {
		return "present"
	}
	return "ABSENT"
}

func errString(err error) string {
	if err == nil {
		return "nil — Deliver reported success"
	}
	return err.Error()
}

// runMG drives the real binary against a throwaway store. The --root flag and
// MG_ROOT are both set, matching internal/mgcontract: the flag carries the same
// authority per-command, so a probe running beside anything else in the process
// cannot be raced onto the caller's live store by an environment variable.
func runMG(bin, root string, args ...string) (string, error) {
	cmd := exec.Command(bin, append([]string{"--root", root}, args...)...)
	cmd.Env = append(os.Environ(), "MG_ROOT="+root, "POGO_AGENT_NAME=", "MG_ACTOR=pogod")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mgLookup is MGBinary, indirected so a test can drive the BLIND branch on
// purpose. A probe whose blind branch has never been exercised is a probe whose
// most important state is the untested one.
var mgLookup = MGBinary
