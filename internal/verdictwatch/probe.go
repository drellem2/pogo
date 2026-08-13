package verdictwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// # THE CONSTRUCTIVE PROBE
//
// A detector that has only ever been observed SILENT has not been observed
// working. This file constructs the failure on purpose, against a throwaway
// macguffin store driven by the REAL `mg` binary — no mock, no fixture
// hand-written to be detected — and checks that the detector goes RED on it and
// GREEN on its matched control.
//
// The matched pair is the whole point. One arm drops the verdict and the
// detector must report exactly one drop. The other arm sends it and the detector
// must report zero. A detector that fires on both is an alarm, not a detector;
// one that fires on neither is decoration.
//
// # Why this is in non-test code
//
// Because the reason this port exists is that the original's constructive probes
// DIED UNOBSERVED. They were killed roughly 22 hours after landing by a change
// that was CORRECT and would be made again — mg-d639 made an unknown mail
// recipient a refusal rather than a silent create, and every filer in a
// throwaway store is unknown by construction — and nobody noticed for two days,
// because the read-only census kept working throughout and stayed green.
//
// So the probe is exercised from two places that fail in different ways:
//
//  1. TestTheProbeGoesRedOnAKnownBadInputAndGreenOnAKnownGood, which is in
//     `go test ./...`, which `build.sh` runs, which the refinery gate runs on
//     EVERY MERGE. A future correct mg change that breaks the probe's setup
//     turns the next pogo merge red, by name, within one merge. It cannot be
//     dead for two days.
//  2. `pogo check-verdicts --probe`, which runs the same code against the same
//     kind of throwaway store at RUNTIME, with no Go toolchain — so the question
//     "can this detector still fire?" is answerable on a deployed box by the
//     operator who is looking at a green census and wondering whether to believe
//     it.
//
// A SETUP FAILURE IS NOT A PASS AND NOT A FAIL. It sets Blind, which reads as
// INSTRUMENT FAILURE everywhere this result is rendered, so the one thing that
// cannot happen is the thing that happened to the original: the probe quietly
// ceasing to prove anything while its report still looked fine.
//
// DECLARED RESIDUAL LIMIT: on a machine with no `mg` on PATH the Go test SKIPS,
// as every live mg-driven test in this suite always has. That case is visible
// rather than silent — `--probe` reports INSTRUMENT FAILURE and exits non-zero —
// but nothing in the merge gate distinguishes "the probe passed" from "the probe
// skipped for want of a binary". Removing mg from a fleet box would hide this
// probe exactly as it would hide the rest of the suite's live controls.

// ProbeArm is one constructed input and what the detector said about it.
type ProbeArm struct {
	// Name says what was constructed, in the words of the input rather than the
	// expected output: "the verdict is dropped on purpose".
	Name string `json:"name"`
	Item string `json:"item,omitempty"`
	// Want is the verdict the construction demands.
	Want Status `json:"want"`
	// Got is the verdict the detector actually reached.
	Got Status `json:"got"`
	OK  bool   `json:"ok"`
	// Detail carries anything the arm needs to say beyond want/got, such as a
	// count that came out wrong.
	Detail string `json:"detail,omitempty"`
}

// ProbeResult is one full run of the constructive probe.
type ProbeResult struct {
	// Store is the throwaway macguffin root the pair was built in. It is removed
	// before this returns; it is reported so a failure names where it happened.
	Store string `json:"store"`
	// MG is the binary that was driven.
	MG   string     `json:"mg"`
	Arms []ProbeArm `json:"arms,omitempty"`
	// Blind is set when the probe could not be CONSTRUCTED at all — no mg, a
	// store that would not initialise, a refused fixture command. It is neither
	// a pass nor a failure of the detector; it is the probe reporting that it
	// measured nothing.
	Blind string `json:"blind,omitempty"`
}

// InstrumentFailure reports whether the probe could not be constructed.
func (p ProbeResult) InstrumentFailure() bool { return p.Blind != "" }

// Passed reports whether every constructed arm landed on its declared
// expectation. A blind run has not passed.
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

// Failures returns the arms that missed their expectation.
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
		fmt.Fprintf(&b, "INSTRUMENT FAILURE — the constructive probe could not be BUILT, so it\n"+
			"proved nothing either way:\n\n  %s\n\n"+
			"This is not a verdict on the detector. It is the state the original\n"+
			"verdictwatch was in for two days while its census stayed green.\n", p.Blind)
		return b.String()
	}
	fmt.Fprintf(&b, "constructive probe — mg %s — throwaway store %s\n\n", p.MG, p.Store)
	for _, a := range p.Arms {
		mark := "PASS"
		if !a.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", mark, a.Name)
		fmt.Fprintf(&b, "         want %s, got %s", a.Want, a.Got)
		if a.Item != "" {
			fmt.Fprintf(&b, "  (%s)", a.Item)
		}
		b.WriteString("\n")
		if a.Detail != "" {
			fmt.Fprintf(&b, "         %s\n", a.Detail)
		}
	}
	if p.Passed() {
		b.WriteString("\nTHE DETECTOR HAS BEEN SEEN TO FIRE ON A FAILURE CONSTRUCTED ON PURPOSE AND TO\n" +
			"STAY QUIET ON ITS MATCHED CONTROL. Same instrument, two inputs, two verdicts.\n")
	} else {
		fmt.Fprintf(&b, "\n%d arm(s) missed their declared expectation. The detector cannot be\n"+
			"trusted to fire until this is green.\n", len(p.Failures()))
	}
	return b.String()
}

// Probe builds a throwaway macguffin store, drives the real `mg` binary through
// new / claim / done, and checks the detector against a matched pair: one item
// whose verdict is dropped ON PURPOSE, and one whose verdict is delivered.
//
// The store is removed before this returns.
func Probe() ProbeResult {
	res := ProbeResult{}
	bin, err := mgLookup()
	if err != nil {
		res.Blind = err.Error()
		return res
	}
	res.MG = bin

	dir, err := os.MkdirTemp("", "verdictwatch-probe-")
	if err != nil {
		res.Blind = fmt.Sprintf("could not create a throwaway store: %v", err)
		return res
	}
	defer os.RemoveAll(dir)
	s := &store{bin: bin, root: filepath.Join(dir, "macguffin")}
	res.Store = s.root
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		res.Blind = fmt.Sprintf("could not create a throwaway store: %v", err)
		return res
	}
	if out, code, err := s.run("", "init"); err != nil {
		res.Blind = fmt.Sprintf("`mg init` on the throwaway store: %v", err)
		return res
	} else if code != 0 {
		res.Blind = fmt.Sprintf("`mg init` on the throwaway store exited %d:\n%s", code, out)
		return res
	}

	arms, err := s.matchedPair()
	if err != nil {
		// A fixture that could not be built. THIS IS THE BRANCH THAT MATTERS:
		// mg-d639 landed here, and reporting it as blind rather than as a pass
		// is the whole reason this result type has three states.
		res.Blind = fmt.Sprintf("the matched pair could not be constructed: %v", err)
		return res
	}
	res.Arms = arms
	return res
}

// matchedPair constructs the known-bad and known-good inputs and reads the
// detector's verdict on each.
//
// FOUR ARMS, NOT TWO (mg-4e02). The pair that matters most is no longer
// mailed/not-mailed: it is the pogod Creator-notify against the coordinator RELAY.
// Those two look nearly identical on disk — macguffin mail, in the filer's own
// box, naming the item — and the detector must call the first a delivery and the
// second a drop, because the question is who discharged the obligation. A
// predicate that cannot separate them either misses every notified item (what
// this was) or counts every mention of a verdict as one (the widening it must not
// take).
func (s *store) matchedPair() ([]ProbeArm, error) {
	const filer = "filer-a"

	// KNOWN-BAD INPUT: the work lands and nobody mails the filer at all. THE
	// POSITIVE CONTROL: whatever else changes, this must stay RED, or the fix for
	// the false positives has silenced the true ones.
	bad, err := s.newItem(filer, "arm A: the verdict is dropped on purpose",
		"This worker finishes the work and never mails the filer.\n")
	if err != nil {
		return nil, err
	}
	if err := s.land(bad, "wa1"); err != nil {
		return nil, err
	}

	// KNOWN-GOOD INPUT: the same shape, with the verdict mailed before landing.
	good, err := s.newItem(filer, "arm B: the verdict is delivered",
		"This worker mails its verdict before the item lands.\n")
	if err != nil {
		return nil, err
	}
	if _, err := s.sendMail(filer, "wb1", good+" VERDICT: the control arm",
		good+" verdict body — this is what delivery looks like.\n"); err != nil {
		return nil, err
	}
	if err := s.land(good, "wb1"); err != nil {
		return nil, err
	}

	// KNOWN-GOOD INPUT, THE CHANNEL THIS DETECTOR WAS BLIND TO: the worker never
	// mails anyone and pogod's Creator-notification (mg-f120) tells the filer. The
	// subject is the shape internal/filernotify emits; the coupling to that
	// package's real output is pinned by a test, because a fixture is free to be
	// wrong about a format in exactly the way this detector already was.
	notified, err := s.newItem(filer, "arm C: the worker never mailed, pogod's Creator-notify did",
		"The backstop covers this one. The worker mails nobody.\n")
	if err != nil {
		return nil, err
	}
	if err := s.land(notified, "wc1"); err != nil {
		return nil, err
	}
	if _, err := s.sendMail(filer, pogodSender,
		fmt.Sprintf("COMPLETED: %s — arm C: the worker never mailed, pogod's Creator-notify did", notified),
		"The work item you filed has completed.\n"); err != nil {
		return nil, err
	}

	// KNOWN-BAD INPUT THAT LOOKS LIKE A DELIVERY: the coordinator relays a
	// headline to the filer. Same transport, same mailbox, the item named in the
	// subject — and it is not a verdict. This arm is what stops the fix for arm C
	// being "count anything that mentions the item".
	relayed, err := s.newItem(filer, "arm D: a relayed headline is not a verdict",
		"The coordinator forwards word of this one. Nobody sends a verdict.\n")
	if err != nil {
		return nil, err
	}
	// This one lands WITH its verdict recorded, so the report has to place it in
	// ROUTING and print the outcome it is holding, while arm A — closed with no
	// verdict at all — is the LOST class.
	if err := s.landRecording(relayed, "wd1", "partial"); err != nil {
		return nil, err
	}
	if _, err := s.sendMail(filer, DefaultCoordinator,
		fmt.Sprintf("FYI: %s is done — passing on what I heard", relayed),
		"Heard your item finished. No verdict attached; I do not have one.\n"); err != nil {
		return nil, err
	}

	rep, err := Scan(Options{Root: s.root, Filer: filer})
	if err != nil {
		return nil, fmt.Errorf("scan the throwaway store: %w", err)
	}
	if rep.InstrumentFailure() {
		return nil, fmt.Errorf("the scan of the throwaway store reported itself blind: %s",
			strings.Join(rep.Blind, "; "))
	}

	arms := []ProbeArm{
		{
			Name: "known-bad input: work landed, nobody mailed the filer",
			Item: bad, Want: Dropped, Got: statusOf(rep, bad),
		},
		{
			Name: "known-good input: the same work, verdict mailed to the filer by the WORKER",
			Item: good, Want: Delivered, Got: statusOf(rep, good),
		},
		{
			Name: "known-good input: the worker mailed nobody and POGOD's Creator-notify told the filer",
			Item: notified, Want: Delivered, Got: statusOf(rep, notified),
		},
		{
			Name: "known-bad input: the COORDINATOR relayed a headline — a mention of a verdict is not one",
			Item: relayed, Want: Dropped, Got: statusOf(rep, relayed),
		},
	}
	for i := range arms {
		arms[i].OK = arms[i].Got == arms[i].Want
	}
	// DELIVERED must name the channel, so the two delivered arms assert WHICH one
	// carried them. A detector that counts pogod's notice as the worker's mail has
	// stopped being able to answer the question the census exists for: did a
	// polecat do its job, or did a backstop catch it?
	arms = append(arms,
		channelArm("the WORKER's mail is attributed to the worker channel", rep, good, ChannelWorker),
		channelArm("POGOD's notice is attributed to the pogod channel, not the worker's", rep, notified, ChannelPogod))

	// The counts are asserted as well as the per-row verdicts: rows landing
	// correctly while the report claims a different number of drops would be a
	// report nobody could act on, and the row check alone would pass.
	wantCounts := "2 dropped / 2 delivered"
	gotCounts := fmt.Sprintf("%d dropped / %d delivered", rep.Dropped, rep.Delivered)
	countArm := ProbeArm{
		Name: "the report's own counts agree with its rows",
		Want: Status(wantCounts), Got: Status(gotCounts), OK: gotCounts == wantCounts,
	}
	if !countArm.OK {
		countArm.Detail = fmt.Sprintf("scanned %d row(s); a count that disagrees with the rows is a report nobody can act on",
			rep.Scanned)
	}
	arms = append(arms, countArm)

	// The recoverability axis, over the same population and never added to the
	// first: arm D's sidecar records a verdict and arm A's does not, so the two
	// dropped rows are one ROUTING and one LOST. A single collapsed DROPPED number
	// cannot say which, and that is the difference between a row somebody can
	// discharge in one pass and a row that is gone.
	wantClasses := "1 routing / 1 lost"
	gotClasses := fmt.Sprintf("%d routing / %d lost", rep.Routing, rep.Lost)
	classArm := ProbeArm{
		Name: "the dropped rows split into ROUTING (verdict on disk) and LOST (nothing recorded)",
		Want: Status(wantClasses), Got: Status(gotClasses), OK: gotClasses == wantClasses,
	}
	if !classArm.OK {
		classArm.Detail = "arm D was closed WITH a verdict in its sidecar and arm A without one; a report that " +
			"calls arm D LOST would send its reader hunting for an outcome the report is holding"
	}
	arms = append(arms, classArm)

	// And the verdict is PRINTED, not merely counted. The row that says nobody was
	// told must also say what they should have been told, or recovery is one item
	// at a time out of a report that already had the answer open.
	rendered := rep.Render(false, false)
	printsIt := strings.Contains(rendered, "partial") && strings.Contains(rendered, relayed+".result.json")
	arms = append(arms, ProbeArm{
		Name: "the dropped row PRINTS the verdict its sidecar holds, with the explicit path",
		Item: relayed, Want: "verdict printed", Got: Status(printed(printsIt)), OK: printsIt,
		Detail: "the worker's identity comes out of that same sidecar, so a report that omits the verdict " +
			"is looking past an answer it has in hand",
	})
	return arms, nil
}

func printed(ok bool) string {
	if ok {
		return "verdict printed"
	}
	return "verdict withheld"
}

// channelArm asserts which channel a delivered row was attributed to.
func channelArm(name string, rep Report, id string, want Channel) ProbeArm {
	got := Channel("ABSENT")
	for _, row := range rep.Rows {
		if row.ID == id {
			got = row.DeliveredBy
			if got == "" {
				got = "not-delivered"
			}
			break
		}
	}
	return ProbeArm{
		Name: name, Item: id,
		Want: Status(want), Got: Status(got), OK: got == want,
	}
}

func statusOf(rep Report, id string) Status {
	for _, row := range rep.Rows {
		if row.ID == id {
			return row.Status
		}
	}
	return "ABSENT"
}

// ---------------------------------------------------------------- the store

// store is a throwaway macguffin workspace driven through the real binary.
//
// Every command is addressed with the --root FLAG as well as MG_ROOT, matching
// internal/mgcontract: the flag carries the same authority per-command, so a
// probe running beside anything else in the process cannot be raced onto the
// caller's store by an environment variable.
type store struct {
	bin  string
	root string
}

func (s *store) run(actor string, args ...string) (string, int, error) {
	return s.runIn(actor, "", args...)
}

func (s *store) runIn(actor, stdin string, args ...string) (string, int, error) {
	cmd := exec.Command(s.bin, append([]string{"--root", s.root}, args...)...)
	// POGO_AGENT_NAME would make mg attribute these fixture commands to whatever
	// agent happens to be running the probe. MG_ACTOR is set per-command so the
	// fixture's filer and worker are the ones the construction names.
	//
	// Assigned as one append(os.Environ(), ...) rather than built up in a
	// variable: internal/gitceiling's inheritance check reads this shape, and the
	// ceiling it is protecting is ambient — a sealed environment here would let
	// mg's own git lookups walk out of POGO_HOME.
	overrides := []string{"MG_ROOT=" + s.root, "POGO_AGENT_NAME=", "MG_ACTOR=" + actor}
	cmd.Env = append(os.Environ(), overrides...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	if err != nil {
		return string(out), -1, fmt.Errorf("run `mg %s`: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), 0, nil
}

var probeItemID = regexp.MustCompile(`\bmg-[0-9a-f]{4,}\b`)

// newItem files an item as `filer`, with --no-repo so the fixture does not
// record whichever ephemeral worktree the caller happens to be running in, and
// --no-declares-remainder so `mg done` will not refuse it for want of a
// successor (which is a real mg refusal and not the subject of this probe).
func (s *store) newItem(filer, title, body string) (string, error) {
	out, code, err := s.runIn(filer, body, "new", "--type", "task", "--no-repo",
		"--no-declares-remainder", "--title", title, "--body-file", "-")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg new` exited %d:\n%s", code, out)
	}
	id := probeItemID.FindString(out)
	if id == "" {
		return "", fmt.Errorf("`mg new` printed no work item id:\n%s", out)
	}
	return id, nil
}

// land takes an item to done exactly as the fleet does it: claim it as the
// worker, then close it, writing the refinery-shaped result sidecar that names
// the worker's branch. That sidecar is the ONLY worker identity macguffin
// records, so a fixture that skipped it would land in UNDECIDABLE and the probe
// would be asserting on the wrong bucket.
func (s *store) land(id, worker string) error { return s.landRecording(id, worker, "") }

// landRecording lands an item and, when verdict is non-empty, records that
// verdict in the result sidecar the way `pogo refinery submit --verdict-file`
// reaches it.
//
// The two forms are what separate the ROUTING class from the LOST one: a dropped
// row whose sidecar holds the outcome can be discharged from the report itself,
// and a dropped row with nothing recorded is the only actual loss. A probe that
// only ever constructed one of them could not tell whether the report's split
// means anything.
func (s *store) landRecording(id, worker, verdict string) error {
	if out, code, err := s.run(worker, "claim", id); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("`mg claim` exited %d:\n%s", code, out)
	}
	side := map[string]interface{}{
		"branch": "polecat-" + worker, "completed_by": "refinery",
		"mr": "mr-constructed", "target": "main",
	}
	if verdict != "" {
		side["verdict"] = map[string]interface{}{
			"verdict": verdict,
			"summary": "constructed by the verdictwatch probe: the outcome exists on disk",
		}
	}
	result, err := json.Marshal(side)
	if err != nil {
		return err
	}
	if out, code, err := s.run("daniel", "done", id, "--result", string(result)); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("`mg done` exited %d:\n%s", code, out)
	}
	return nil
}

// archiveItem takes an already-done item to archived/.
func (s *store) archiveItem(id string) error {
	if out, code, err := s.run("daniel", "archive", id); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("`mg archive` exited %d:\n%s", code, out)
	}
	return nil
}

// sendMail delivers one message and returns its MSG-ID.
//
// --create IS LOAD-BEARING AND IT IS NOT A WAY PAST A REFUSAL. mg-d639 made an
// unknown recipient a refusal rather than a silent create, and every filer in a
// store `mg init` made four lines ago is unknown by construction — so this
// genuinely IS the first mail to a new correspondent, which is exactly what that
// flag is reserved for. The refusal protects a typo'd recipient in the LIVE
// store; there is no live store here to typo into.
//
// The MSG-ID is read from the documented `--json` contract rather than scraped
// out of `mg mail send`'s prose. The original scraped it, matched nothing (what
// mg prints is a PATH), silently returned no id, and one of its edge cases then
// asserted DELIVERED against a verdict that had never been archived — a vacuous
// pass, in the file whose entire job was to prove the detector was not vacuous.
// This RAISES instead.
func (s *store) sendMail(to, from, subject, body string) (string, error) {
	out, code, err := s.runIn(from, body, "mail", "send", to,
		"--from", from, "--subject", subject, "--body-file", "-", "--create")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg mail send %s --create` exited %d:\n%s", to, code, out)
	}
	list, code, err := s.run("", "mail", "list", to, "--all", "--json")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("`mg mail list --all --json` exited %d:\n%s", code, list)
	}
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec struct {
			ID      string `json:"id"`
			From    string `json:"from"`
			Subject string `json:"subject"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Subject == subject && rec.From == from {
			return rec.ID, nil
		}
	}
	return "", fmt.Errorf("could not recover the MSG-ID of the message just sent to %s:\n%s", to, list)
}

// archiveMail moves a delivered message out of the active mailbox.
func (s *store) archiveMail(box, msgID string) error {
	if out, code, err := s.run("", "mail", "archive", box+"/"+msgID); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("`mg mail archive` exited %d:\n%s", code, out)
	}
	return nil
}

// mgLookup is mgBinary, indirected so a test can drive the BLIND branch on
// purpose. A probe whose blind branch has never been exercised is a probe whose
// most important state is the untested one: it is the state the original spent
// two days in.
var mgLookup = mgBinary

// mgBinary resolves the macguffin CLI. PATH first, then the fleet's install
// location, so a probe run from a minimal environment (launchd, cron) still
// finds it.
func mgBinary() (string, error) {
	if p, err := exec.LookPath("mg"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, "go", "bin", "mg")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("no `mg` binary on PATH or at ~/go/bin/mg; the constructive probe drives the real macguffin CLI and cannot be built without it")
}
