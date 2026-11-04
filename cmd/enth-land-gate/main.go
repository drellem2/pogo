// Command enth-land-gate gates an enth land on the tree it would produce.
//
//	enth-land-gate --job=NAME --gate='CMD' [--ref=REF] [--remote=URL --repo=NAME] C1 [C2 ...]
//
// The steps, which the lander repeats for every land (pm-enth's recipe, mg-64f2e):
//
//  1. `enth land --dry-run C…` gives the tree T the land would produce,
//     hunk-merge included, and the Change N that has it.
//  2. If the newest attestation NAME on T is a pass, there is nothing to run
//     (tree-keyed reuse). The common case is a base that was the tip, which
//     an earlier gate already tested.
//  3. Otherwise check N out into a scratch directory and PROVE the checkout is
//     T: the checkout stands on N with no edits, and a dry run of N itself
//     reports tree T. Tree ids are content hashes, so equal ids mean equal
//     trees. On a mismatch (the trunk moved between the two dry runs), go
//     back to step 1 and never test a different tree. Then run CMD there and
//     `enth attest T NAME --result pass|fail`.
//  4. Print T. The caller lands with `enth land --require NAME C…`. enth
//     refuses that land (unattested_tree) unless the tree it actually
//     produces carries the pass, so a trunk that moved after this command
//     ran is caught, and the caller runs this again.
//
// The point is that a test binds to the tree that lands, not to the base the
// Change was made on. Two Changes can each be green on their own base and
// broken together (2026-11-04: one removed a function in one file, the other
// called it in another, enth merged both cleanly, and the trunk failed to
// build). Neither can land under this gate: their merged tree is what gets
// tested.
//
// A failure that is not a verdict on the tree attests NOTHING: the checkout
// failed, the gate could not start, ran out of time, died by a signal, or the
// host ran out of disk. Only a gate that ran and exited non-zero is attested
// --result fail.
//
// The tool is deliberately standalone. It depends on enth and a gate command
// only, and imports nothing from pogo.
//
// Output: one JSON line on stdout,
//
//	{"type":"gated","job":J,"tree":T,"new":N,"result":"pass"|"fail","reused":B,"ran":B}
//
// Exit codes: 0 pass (attested now, or reused); 1 fail (attested now, or the
// newest attestation on T is a fail: re-run with --retest); 2 usage;
// 3 enth refused the dry run (conflict, already_landed, ...): enth's lines are
// passed through, and its stderr carries the code; 4 infrastructure: nothing
// attested, retry later; 5 the trunk kept moving, so T could not be
// materialised (--max-moves); 6 the attestation could not be recorded;
// 7 the tree PASSED but this token may not attest NAME (enthd answered
// forbidden: the attest is the token probe), so nothing binds the land to T.
// A caller that lands anyway must first re-run the dry run and check that
// it still names T, then land without --require.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	exitPass     = 0
	exitFail     = 1
	exitUsage    = 2
	exitRefused  = 3
	exitInfra    = 4
	exitMoved    = 5
	exitNoAttest = 6
	exitTestOnly = 7
)

type options struct {
	job      string
	gate     string
	ref      string
	remote   string
	repo     string
	enth     string
	workdir  string
	keep     bool
	retest   bool
	timeout  time.Duration
	maxMoves int
	changes  []string
	stderr   io.Writer
	stdout   io.Writer
}

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "enth-land-gate: %v\n", err)
		os.Exit(exitUsage)
	}
	o.stdout, o.stderr = os.Stdout, os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	os.Exit(run(ctx, o))
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("enth-land-gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.job, "job", "", "attestation name the gate records, e.g. ci/selfland/build (required)")
	fs.StringVar(&o.gate, "gate", "", "the gate: a shell command run in the materialised tree (required)")
	fs.StringVar(&o.ref, "ref", "", "the ref the land moves (default: enth's trunk)")
	fs.StringVar(&o.remote, "remote", "", "enthd URL (default: $ENTH_REMOTE, or the checkout the cwd is in)")
	fs.StringVar(&o.repo, "repo", "", "repo on enthd (default: $ENTH_REPO, or the checkout the cwd is in)")
	fs.StringVar(&o.enth, "enth", "enth", "the enth executable")
	fs.StringVar(&o.workdir, "workdir", "", "where to materialise the tree (default: a new temp dir)")
	fs.BoolVar(&o.keep, "keep", false, "keep the materialised tree")
	fs.BoolVar(&o.retest, "retest", false, "run the gate even if the newest attestation on the tree is a fail")
	fs.DurationVar(&o.timeout, "timeout", 60*time.Minute, "kill the gate after this long (attests nothing)")
	fs.IntVar(&o.maxMoves, "max-moves", 3, "how many times to re-plan when the trunk moves under materialisation")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.changes = fs.Args()
	switch {
	case o.job == "":
		return o, errors.New("--job is required")
	case o.gate == "":
		return o, errors.New("--gate is required")
	case len(o.changes) == 0:
		return o, errors.New("no Change: pass C1 [C2 ...]")
	case o.maxMoves < 1:
		return o, errors.New("--max-moves must be at least 1")
	}
	return o, nil
}

// outcome is the one line on stdout.
type outcome struct {
	Type   string `json:"type"`
	Job    string `json:"job"`
	Tree   string `json:"tree"`
	New    string `json:"new"`
	Result string `json:"result"`
	Reused bool   `json:"reused"`
	Ran    bool   `json:"ran"`
	// Attested is false for a result this token could not record (exit 7,
	// or a red that is refused either way).
	Attested bool `json:"attested"`
}

func (o options) logf(format string, a ...any) {
	if o.stderr == nil {
		return
	}
	fmt.Fprintf(o.stderr, "enth-land-gate: "+format+"\n", a...)
}

func (o options) global() []string {
	var a []string
	if o.remote != "" {
		a = append(a, "--remote", o.remote)
	}
	if o.repo != "" {
		a = append(a, "--repo", o.repo)
	}
	return a
}

// enthErr is enth's stderr error line.
type enthErr struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type cmdErr struct {
	args           []string
	stdout, stderr []byte
	code           string
	err            error
}

func (e *cmdErr) Error() string {
	return fmt.Sprintf("enth %s: %v: %s", strings.Join(e.args, " "), e.err, bytes.TrimSpace(e.stderr))
}

func (o options) enthRun(ctx context.Context, args ...string) ([]byte, error) {
	full := append(o.global(), args...)
	cmd := exec.CommandContext(ctx, o.enth, full...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		ce := &cmdErr{args: full, stdout: so.Bytes(), stderr: se.Bytes(), err: err}
		for _, l := range strings.Split(se.String(), "\n") {
			var e enthErr
			if json.Unmarshal([]byte(strings.TrimSpace(l)), &e) == nil && e.Type == "error" && e.Code != "" {
				ce.code = e.Code
				break
			}
		}
		return so.Bytes(), ce
	}
	return so.Bytes(), nil
}

type landChange struct {
	Submitted string `json:"submitted"`
	Landed    string `json:"landed"`
}

type dryRun struct {
	Type    string       `json:"type"`
	Old     *string      `json:"old"`
	New     string       `json:"new"`
	Tree    string       `json:"tree"`
	Changes []landChange `json:"changes"`
}

func (o options) dryRun(ctx context.Context, changes []string) (dryRun, error) {
	args := []string{"land", "--dry-run"}
	if o.ref != "" {
		args = append(args, "--ref", o.ref)
	}
	out, err := o.enthRun(ctx, append(args, changes...)...)
	if err != nil {
		return dryRun{}, err
	}
	var d dryRun
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if err := json.Unmarshal([]byte(l), &d); err != nil {
			return dryRun{}, fmt.Errorf("enth land --dry-run: unreadable line %q: %v", l, err)
		}
	}
	switch {
	case d.Type != "dry_run":
		return dryRun{}, fmt.Errorf("enth land --dry-run printed no dry_run line: %q", bytes.TrimSpace(out))
	case len(d.Tree) != 64:
		return dryRun{}, fmt.Errorf("enth land --dry-run names no tree (an enth older than enth #9 item L, mg-b729a, cannot gate a tree): %q", bytes.TrimSpace(out))
	case d.New == "":
		return dryRun{}, fmt.Errorf("enth land --dry-run names no resulting Change: %q", bytes.TrimSpace(out))
	}
	return d, nil
}

type attestation struct {
	Type   string `json:"type"`
	Entry  int    `json:"entry"`
	Tree   string `json:"tree"`
	Name   string `json:"name"`
	Result string `json:"result"`
}

// newest is the newest attestation of job on tree, or ok=false for none.
func (o options) newest(ctx context.Context, tree string) (attestation, bool, error) {
	out, err := o.enthRun(ctx, "query", "attestations", "--tree", tree, "--name", o.job)
	if err != nil {
		return attestation{}, false, err
	}
	var best attestation
	found := false
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var a attestation
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			return attestation{}, false, fmt.Errorf("enth query attestations: unreadable line %q: %v", l, err)
		}
		// The filter is enth's; check it anyway, so a reader that ignored
		// --name could never hand us another job's or tree's pass.
		if a.Type != "attestation" || a.Tree != tree || a.Name != o.job {
			continue
		}
		if !found || a.Entry > best.Entry {
			best, found = a, true
		}
	}
	return best, found, nil
}

// materialise checks d.New out into dir and proves the checkout is d.Tree.
// moved=true means the trunk moved under it: nothing was proven, and the
// caller re-plans.
func (o options) materialise(ctx context.Context, d dryRun, dir string) (moved bool, err error) {
	out, err := o.enthRun(ctx, "checkout", d.New, dir)
	if err != nil {
		return false, fmt.Errorf("checking out the dry run's result %s: %w", d.New, err)
	}
	var co struct {
		Type   string  `json:"type"`
		Change *string `json:"change"`
	}
	if json.Unmarshal(bytes.TrimSpace(firstLine(out)), &co) != nil || co.Type != "checkout" || co.Change == nil || *co.Change != d.New {
		return false, fmt.Errorf("enth checkout %s did not report checking out %s: %q", d.New, d.New, bytes.TrimSpace(out))
	}
	// status reads the checkout itself; enth refuses it --remote.
	st, err := options{enth: o.enth}.enthRun(ctx, "status", dir)
	if err != nil {
		return false, fmt.Errorf("enth status %s: %w", dir, err)
	}
	var s struct {
		Type string `json:"type"`
		Base string `json:"base"`
		Ops  int    `json:"ops"`
	}
	if json.Unmarshal(bytes.TrimSpace(firstLine(st)), &s) != nil || s.Type != "status" || s.Base != d.New || s.Ops != 0 {
		return false, fmt.Errorf("the checkout in %s is not exactly %s with no edits: %q", dir, d.New, bytes.TrimSpace(st))
	}
	// The proof: enth's own tree id of what was checked out. N's parent is
	// the tip the first dry run read. While that is still the tip, N lands as
	// it is, and the dry run's tree is tree(N).
	again, err := o.dryRun(ctx, []string{d.New})
	if err != nil {
		var ce *cmdErr
		if errors.As(err, &ce) && ce.code == "stale_tip" {
			return true, nil
		}
		return false, fmt.Errorf("re-reading the tree of %s: %w", d.New, err)
	}
	if len(again.Changes) != 1 || again.Changes[0].Submitted != d.New || again.Changes[0].Landed != d.New {
		// N would be rebased: its parent is no longer the tip.
		o.logf("%s would no longer land as it is (the trunk moved); re-planning, NOT testing it", short(d.New))
		return true, nil
	}
	if again.Tree != d.Tree {
		o.logf("the checkout of %s is tree %s, not %s: the trunk moved; re-planning, NOT testing it", short(d.New), short(again.Tree), short(d.Tree))
		return true, nil
	}
	return false, nil
}

func firstLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// gateResult is a gate run: a verdict (pass or fail) or infrastructure.
type gateResult struct {
	verdict  bool // the gate ran and returned a result about the tree
	pass     bool
	exit     int
	why      string // infrastructure: what happened instead of a verdict
	duration time.Duration
	tail     string
}

// hostSignals are output that says the HOST failed, not the tree.
var hostSignals = []string{
	"no space left on device",
	"disk quota exceeded",
	"cannot allocate memory",
	"too many open files",
}

func (o options) runGate(ctx context.Context, dir string) gateResult {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	cmd := exec.Command("/bin/sh", "-c", o.gate)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ENTH_LAND_GATE_TREE_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuffer{max: 4096}
	w := io.MultiWriter(o.stderr, tail)
	cmd.Stdout, cmd.Stderr = w, w
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return gateResult{why: "the gate could not start: " + err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		// Kill the whole group: a gate's children must not outlive it.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		r := gateResult{duration: time.Since(start), tail: tail.String()}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.why = fmt.Sprintf("the gate was killed at its timeout (%s) and returned no verdict", o.timeout)
		} else {
			r.why = "the gate was interrupted and returned no verdict"
		}
		return r
	}
	r := gateResult{duration: time.Since(start), tail: tail.String()}
	if err == nil {
		r.verdict, r.pass = true, true
		return r
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		r.why = "the gate did not run to an exit status: " + err.Error()
		return r
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.why = fmt.Sprintf("the gate was killed by signal %s and returned no verdict", ws.Signal())
		return r
	}
	low := strings.ToLower(tail.String())
	for _, s := range hostSignals {
		if strings.Contains(low, s) {
			r.why = fmt.Sprintf("the gate failed with %q in its output: the host failed, not the tree", s)
			return r
		}
	}
	// sh's own codes for a command it could not run: the gate never started.
	if c := ee.ExitCode(); c == 126 || c == 127 {
		r.why = fmt.Sprintf("the gate could not be run (sh exit %d: not executable, or not found)", c)
		return r
	}
	r.verdict, r.exit = true, ee.ExitCode()
	return r
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }

func (o options) attest(ctx context.Context, d dryRun, g gateResult) error {
	result := "fail"
	if g.pass {
		result = "pass"
	}
	host, _ := os.Hostname()
	tail := g.tail
	if len(tail) > 1024 {
		tail = tail[len(tail)-1024:]
	}
	detail, _ := json.Marshal(map[string]any{
		"tool":       "enth-land-gate",
		"change":     d.New,
		"gate":       o.gate,
		"exit":       g.exit,
		"duration_s": int(g.duration.Seconds()),
		"host":       host,
		"tail":       tail,
	})
	_, err := o.enthRun(ctx, "attest", d.Tree, o.job, "--result", result, "--detail", string(detail))
	return err
}

func (o options) emit(d dryRun, result string, reused, ran bool) {
	o.emitA(d, result, reused, ran, true)
}

func (o options) emitA(d dryRun, result string, reused, ran, attested bool) {
	b, _ := json.Marshal(outcome{Type: "gated", Job: o.job, Tree: d.Tree, New: d.New, Result: result, Reused: reused, Ran: ran, Attested: attested})
	fmt.Fprintln(o.stdout, string(b))
}

func run(ctx context.Context, o options) int {
	for move := 1; ; move++ {
		// 1. The tree the land would produce.
		d, err := o.dryRun(ctx, o.changes)
		if err != nil {
			var ce *cmdErr
			if errors.As(err, &ce) && ce.code != "" && ce.code != "unreachable" && ce.code != "bad_response" && ce.code != "interrupted" {
				o.stdout.Write(ce.stdout)
				o.stderr.Write(ce.stderr)
				o.logf("the dry run was refused (%s): nothing to gate", ce.code)
				return exitRefused
			}
			o.logf("dry run: %v", err)
			return exitInfra
		}
		o.logf("the land would make tree %s (Change %s)", short(d.Tree), short(d.New))

		// 2. Tree-keyed reuse.
		a, found, err := o.newest(ctx, d.Tree)
		if err != nil {
			o.logf("reading %s's attestations of %s: %v", short(d.Tree), o.job, err)
			return exitInfra
		}
		if found && a.Result == "pass" {
			o.logf("tree %s already carries a pass of %s (entry %d): not running the gate again", short(d.Tree), o.job, a.Entry)
			o.emit(d, "pass", true, false)
			return exitPass
		}
		if found && a.Result == "fail" && !o.retest {
			o.logf("tree %s's newest %s is a FAIL (entry %d): it is red; fix it, or --retest if that run was wrong", short(d.Tree), o.job, a.Entry)
			o.emit(d, "fail", true, false)
			return exitFail
		}

		// 3. Materialise T, proven, then gate it.
		code, again := o.gateOnce(ctx, d)
		if !again {
			return code
		}
		if move >= o.maxMoves {
			o.logf("the trunk moved %d time(s) while tree %s was being materialised; giving up (--max-moves)", move, short(d.Tree))
			return exitMoved
		}
	}
}

// gateOnce materialises d and gates it. again=true means the trunk moved and
// the caller should plan again.
func (o options) gateOnce(ctx context.Context, d dryRun) (code int, again bool) {
	base := o.workdir
	if base == "" {
		var err error
		if base, err = os.MkdirTemp("", "enth-land-gate-"); err != nil {
			o.logf("making a directory to materialise the tree in: %v — nothing tested, nothing attested", err)
			return exitInfra, false
		}
		if !o.keep {
			defer os.RemoveAll(base)
		}
	} else if err := os.MkdirAll(base, 0o755); err != nil {
		o.logf("making %s: %v — nothing tested, nothing attested", base, err)
		return exitInfra, false
	}
	dir := filepath.Join(base, "tree-"+short(d.Tree))
	os.RemoveAll(dir)
	if !o.keep {
		defer os.RemoveAll(dir)
	}
	moved, err := o.materialise(ctx, d, dir)
	if err != nil {
		o.logf("materialising tree %s: %v — nothing tested, nothing attested", short(d.Tree), err)
		return exitInfra, false
	}
	if moved {
		return 0, true
	}
	o.logf("running the gate on tree %s in %s: %s", short(d.Tree), dir, o.gate)
	g := o.runGate(ctx, dir)
	if !g.verdict {
		o.logf("INFRASTRUCTURE: %s — nothing attested; run again once it is fixed", g.why)
		return exitInfra, false
	}
	if err := o.attest(ctx, d, g); err != nil {
		var ce *cmdErr
		if errors.As(err, &ce) && ce.code == "forbidden" {
			// The token probe: the attest itself. Forbidden means this token
			// cannot attest (none minted with --attest NAME). The result is
			// still a real one, so a red is red. A pass is reported as
			// unattested (exit 7): enth cannot hold the land to this tree, and
			// the caller must re-check the tree itself before landing.
			o.logf("this token may not attest %s (it needs `enthd token add USER --attest %s`): the result is NOT recorded", o.job, o.job)
			if !g.pass {
				o.logf("tree %s FAILED %s (exit %d); not attested", short(d.Tree), o.job, g.exit)
				o.emitA(d, "fail", false, true, false)
				return exitFail, false
			}
			o.logf("tree %s PASSED %s; not attested (test-only)", short(d.Tree), o.job)
			o.emitA(d, "pass", false, true, false)
			return exitTestOnly, false
		} else {
			o.logf("the gate ran (%s) but the attestation was not recorded: %v", map[bool]string{true: "pass", false: "fail"}[g.pass], err)
		}
		return exitNoAttest, false
	}
	if g.pass {
		o.logf("tree %s PASSED %s; attested", short(d.Tree), o.job)
		o.emit(d, "pass", false, true)
		return exitPass, false
	}
	o.logf("tree %s FAILED %s (exit %d); attested --result fail", short(d.Tree), o.job, g.exit)
	o.emit(d, "fail", false, true)
	return exitFail, false
}
