package main

// The refutation of the metacharacter gate, and the positive control for the
// idiom that replaces it (mg-d91f).
//
// A tempting fix for the --body="..." hazard is to have the binary REFUSE or
// WARN when the body it receives contains backticks, $VAR or $(cmd) — "option
// 1" in the mg-e0ca design. It is not merely weaker than shipping a safe idiom;
// it is INVERTED, and the inversion is structural rather than a matter of
// tuning the pattern. The shell expands before pogo is executed, so a guard
// reading argv sits DOWNSTREAM of the corruption: the only inputs that still
// carry metacharacters when it looks are precisely the ones that were never
// corrupted.
//
//	A  --body="see `echo MANGLED` here"       -> argv: [see MANGLED here]        guard SILENT
//	B  --body="do NOT touch $NOVAR worktree"  -> argv: [do NOT touch  worktree]  guard SILENT
//	C  --body='see `literal backtick` here'   -> argv: unchanged                 guard FIRES
//
// Detection on real failures 0/2. False positive on correct usage 1/1.
//
// Case B is the dangerous class: an unset $VAR silently deletes the OBJECT of a
// safety constraint while the surrounding prose still reads as complete and
// intentional. "do NOT touch  worktree" looks like a sentence someone meant.
//
// This test exists so the refutation lives in the tree rather than only in a
// ticket. Anyone tempted to revisit the gate has to delete a passing test that
// says why, instead of rediscovering it. TestBodyFileHeredoc_CarriesAllThree
// below is the other half: the idiom that actually works, exercised through a
// real shell on the real binary.

import (
	"os/exec"
	"strings"
	"testing"
)

// backtick avoids fighting Go's raw-string rules in every fixture below.
const backtick = "`"

// argvMetacharGate is the hypothetical guard under refutation: the most
// generous version of it, run against what the process ACTUALLY receives. It is
// deliberately not shipped in the binary — it exists only to be measured.
func argvMetacharGate(body string) bool {
	return strings.ContainsAny(body, "`$")
}

// TestMetacharGateIsRefuted_ArgvAssertions drives the three cases through a
// REAL shell into the REAL binary and asserts on the argv the binary received.
// exec.Command would prove nothing here — it invokes no shell, so the hazard
// would be absent and every case would pass unmangled.
func TestMetacharGateIsRefuted_ArgvAssertions(t *testing.T) {
	// CASE A — command substitution. The shell runs `echo MANGLED` and splices
	// the output in. What reaches argv is a plausible-looking sentence.
	var capA spawnCapture
	_, stderr, code := runShPogo(t, &capA, "",
		pogoBin+` agent spawn-polecat cat-d91f --id mg-d91f --body="see `+backtick+`echo MANGLED`+backtick+` here"`)
	if code != 0 {
		t.Fatalf("case A must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if capA.req.Body != "see MANGLED here" {
		t.Fatalf("case A control failed: the shell did not substitute, so this test proves nothing.\n got: %q", capA.req.Body)
	}
	if argvMetacharGate(capA.req.Body) {
		t.Errorf("case A: the gate fired on a body that WAS corrupted — the refutation's premise changed, re-derive it")
	}

	// CASE B — the dangerous class. An unset variable deletes the object of a
	// constraint and leaves prose that still reads as intentional.
	var capB spawnCapture
	_, stderr, code = runShPogo(t, &capB, "",
		pogoBin+` agent spawn-polecat cat-d91f --id mg-d91f --body="do NOT touch $NONEXISTENT_VAR worktree"`)
	if code != 0 {
		t.Fatalf("case B must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if strings.Contains(capB.req.Body, "NONEXISTENT_VAR") {
		t.Fatalf("case B control failed: the shell did not expand the unset var.\n got: %q", capB.req.Body)
	}
	if capB.req.Body != "do NOT touch  worktree" {
		t.Errorf("case B: want the silently-hollowed sentence %q, got %q", "do NOT touch  worktree", capB.req.Body)
	}
	if argvMetacharGate(capB.req.Body) {
		t.Errorf("case B: the gate fired on a body that WAS corrupted — the refutation's premise changed, re-derive it")
	}

	// CASE C — correct usage. Single quotes stop the shell, so the body arrives
	// intact WITH its metacharacters. This is the only case the gate can see,
	// and it is the one case that needed no help.
	var capC spawnCapture
	_, stderr, code = runShPogo(t, &capC, "",
		pogoBin+` agent spawn-polecat cat-d91f --id mg-d91f --body='see `+backtick+`literal backtick`+backtick+` here'`)
	if code != 0 {
		t.Fatalf("case C must exit 0, got %d\nstderr: %s", code, stderr)
	}
	const wantC = "see `literal backtick` here"
	if capC.req.Body != wantC {
		t.Errorf("case C: single-quoted body must arrive unchanged.\n want: %q\n  got: %q", wantC, capC.req.Body)
	}
	if !argvMetacharGate(capC.req.Body) {
		t.Errorf("case C: the gate did NOT fire on correct usage — the false-positive half of the refutation changed, re-derive it")
	}

	// The scoreboard, asserted rather than asserted-about: 0/2 detection on the
	// real failures, 1/1 false positive on the correct one.
	detected := 0
	for _, c := range []*spawnCapture{&capA, &capB} {
		if argvMetacharGate(c.req.Body) {
			detected++
		}
	}
	if detected != 0 {
		t.Errorf("metachar gate detected %d/2 real corruptions; the refutation assumed 0/2", detected)
	}
}

// TestBodyFileHeredoc_CarriesAllThree is the positive control for the idiom
// this ticket ships: --body-file - fed by a QUOTED heredoc, through a real
// shell, on the real binary. All three hazard classes must arrive byte-exact.
//
// The quoting is the entire property — see TestBodyFileUnquotedHeredocIsUnsafe
// below, which is why the shipped examples say <<'EOF' and not <<EOF.
func TestBodyFileHeredoc_CarriesAllThree(t *testing.T) {
	want := "CASE A: see " + backtick + "echo MANGLED" + backtick + " here\n" +
		"CASE B: do NOT touch $NONEXISTENT_VAR worktree\n" +
		"CASE C: literal 'single' and \"double\" quotes\n"

	var cap spawnCapture
	_, stderr, code := runShPogo(t, &cap, "",
		pogoBin+" agent spawn-polecat cat-d91f --id mg-d91f --body-file - <<'EOF'\n"+want+"EOF\n")
	if code != 0 {
		t.Fatalf("quoted-heredoc spawn must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !cap.got {
		t.Fatal("stub pogod never received a spawn request")
	}
	if cap.req.Body != want {
		t.Errorf("--body-file - with <<'EOF' must transmit bytes verbatim.\n want: %q\n  got: %q", want, cap.req.Body)
	}

	// ...and through template expansion into the prompt the polecat reads,
	// which is the only surface that actually matters.
	prompt := renderPromptFile(t, cap.req.Body)
	for _, frag := range []string{
		backtick + "echo MANGLED" + backtick,
		"$NONEXISTENT_VAR",
		`'single' and "double"`,
	} {
		if !strings.Contains(prompt, frag) {
			t.Errorf("prompt lost %q — the quoted heredoc did not arrive literal", frag)
		}
	}
}

// TestBodyFileUnquotedHeredocIsUnsafe is why every shipped example must be
// <<'EOF'. A bare <<EOF expands identically to --body="..." — the body is
// corrupted before pogo is executed, the CLI reports success, and the diff
// looks like the fix was applied. This is the regression a reviewer waves
// through, so it is asserted, and internal/agent/bodyratchet_test.go fails any
// prompt template that teaches it.
func TestBodyFileUnquotedHeredocIsUnsafe(t *testing.T) {
	body := "do NOT touch $NONEXISTENT_VAR worktree\n"

	var cap spawnCapture
	_, stderr, code := runShPogo(t, &cap, "",
		pogoBin+" agent spawn-polecat cat-d91f --id mg-d91f --body-file - <<EOF\n"+body+"EOF\n")
	if code != 0 {
		t.Fatalf("unquoted-heredoc spawn must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if cap.req.Body == body {
		t.Fatal("control failed: <<EOF did not expand, so this test cannot show why the quoting matters")
	}
	if cap.req.Body != "do NOT touch  worktree\n" {
		t.Errorf("want the hollowed body %q, got %q", "do NOT touch  worktree\n", cap.req.Body)
	}
}

// TestSpawnPolecatHelp_LeadsWithQuotedHeredoc pins the ordering half of
// mg-d91f. Help text is read top-down and copied from the top, so whichever
// form appears FIRST is the one that propagates. The quoted-heredoc one-liner
// dominates on every axis — one command, inline authoring, no temp file, safe —
// so it leads, and the ./file form follows.
func TestSpawnPolecatHelp_LeadsWithQuotedHeredoc(t *testing.T) {
	out, err := exec.Command(pogoBin, "agent", "spawn-polecat", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, out)
	}
	help := string(out)

	heredoc := strings.Index(help, "--body-file - <<'EOF'")
	if heredoc < 0 {
		t.Fatalf("--help must demonstrate the quoted-heredoc idiom, got:\n%s", help)
	}
	file := strings.Index(help, "--body-file ./task.md")
	if file < 0 {
		t.Fatalf("--help must still show the ./file form, got:\n%s", help)
	}
	if heredoc > file {
		t.Error("the quoted-heredoc example must come FIRST, above the ./file form")
	}

	// The quoting warning has to travel with the example. Without it the next
	// author copies the shape, drops the quotes, and silently reintroduces the
	// bug in a diff that looks like the fix.
	if !strings.Contains(help, "bare <<EOF expands") {
		t.Errorf("--help must say why the delimiter is quoted, got:\n%s", help)
	}

	// --body is NOT deprecated. Gating or removing it was refuted (see
	// TestMetacharGateIsRefuted_ArgvAssertions); it is demoted, not condemned.
	if !strings.Contains(help, "not deprecated") {
		t.Errorf("--help must keep --body documented as a supported shortcut, got:\n%s", help)
	}
}
