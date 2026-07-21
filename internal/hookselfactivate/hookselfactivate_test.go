package hookselfactivate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The controls are ordered deliberately. TestCatchesTheOriginalHook — the
// CAUGHT direction — runs first and on a real specimen, because a check that
// has only ever been observed staying quiet on a clean tree has not been
// tested. This repo has zero current violations, so silence is the expected
// result everywhere else and proves nothing on its own.

// specimen returns one of the two historical commit-msg hooks the controls
// below are built on, read from testdata.
//
// These were `git show <sha>:hooks/commit-msg` until mg-4e12. That passed here
// and failed in CI by construction: ci.yml checks out with actions/checkout@v4
// and no fetch-depth, i.e. a depth-1 shallow clone, which does not contain
// those objects — `git show` exited 128 on a machine that was behaving
// correctly. A control whose fixture is a literal SHA depends on clone depth,
// on the objects staying fetchable, and on history never being rewritten: three
// facts about the checkout rather than about the code under test.
//
// So the fixtures are checked in, the same way internal/closingref keeps
// e83f394's commit body. They are not transcriptions — each is the historical
// blob byte for byte, and `git hash-object` on them still returns the object id
// git records for that path at that commit:
//
//	4866a26-commit-msg.txt  9c140bb09f4ed4724a89d325caa112e49e7bbb2a
//	c0c203d-commit-msg.txt  f86237dff7b9c626f82050ae2a09f8008621f5a0
//
// What keeps them honest is not those hashes, though — it is the two controls
// themselves. Weaken the broken specimen and TestCatchesTheOriginalHook stops
// catching it; weaken the fixed one and TestPassesTheFixedHook starts flagging
// the remedy. Both fail loudly rather than quietly passing on anything.
func specimen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read specimen %s: %v", name, err)
	}
	return string(b)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func kinds(fs []Finding) []Kind {
	var out []Kind
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

// TestCatchesTheOriginalHook is the positive control, on the real thing.
//
// 4866a26's commit-msg is the hook that rejected every commit in the repo for
// the length of the merge-vs-deploy window. It is the specimen the rule exists
// for, and the check is worthless if it cannot name it.
//
// Note what it does NOT get caught on: 4866a26 HAS a `go run ./cmd/pogo`
// fallback. A check that only asked "is there a source route?" would have
// waved this through. The defect is that both arms above the fallback select
// the installed binary on presence, so the fallback never runs on any machine
// with a `pogo` on PATH — which is every machine that matters here.
func TestCatchesTheOriginalHook(t *testing.T) {
	src := specimen(t, "4866a26-commit-msg.txt")

	if !sourceRouteRe.MatchString(src) {
		t.Fatal("specimen precondition failed: 4866a26 should contain a `go run ./cmd/` " +
			"fallback — that is what makes it the interesting specimen rather than an easy one")
	}

	findings := Analyze("hooks/commit-msg@4866a26", src)
	if len(findings) == 0 {
		t.Fatal("the hook that broke the repo was reported CLEAN — the check does not " +
			"implement the rule")
	}

	var got bool
	for _, f := range findings {
		if f.Kind == KindPresenceNotCapability {
			got = true
		}
	}
	if !got {
		t.Fatalf("want a %s finding, got kinds %v", KindPresenceNotCapability, kinds(findings))
	}

	// Both presence-only arms should be named, not just the first: whoever
	// fixes this has to fix them both, and a report that stops at one sends
	// them back for a second round.
	var n int
	for _, f := range findings {
		if f.Kind == KindPresenceNotCapability {
			n++
		}
	}
	if n != 2 {
		t.Errorf("want both presence-only arms flagged (`[ -x ... ]` and `command -v`), got %d:\n%s",
			n, render(findings))
	}
	t.Logf("caught, as it must be:\n%s", render(findings))
}

// TestPassesTheFixedHook is the other half of the specimen pair. c0c203d
// (mg-d1f7) probes for the SUBCOMMAND rather than the binary, so a stale
// install falls through to source. Same file, same source fallback, opposite
// verdict — the difference is entirely in the guards.
func TestPassesTheFixedHook(t *testing.T) {
	src := specimen(t, "c0c203d-commit-msg.txt")
	if findings := Analyze("hooks/commit-msg@c0c203d", src); len(findings) != 0 {
		t.Fatalf("the fixed hook must pass; a check that flags the remedy is worse than "+
			"no check:\n%s", render(findings))
	}
}

// TestTrackedHooksSelfActivate is the gate itself: every tracked file under
// hooks/ must satisfy the rule, now and after every future edit.
//
// It enumerates via `git ls-files` rather than walking the directory, because
// the rule is about TRACKED files — those are the ones that go live at merge.
// An untracked scratch file in hooks/ is nobody's deploy hazard.
func TestTrackedHooksSelfActivate(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "hooks").Output()
	if err != nil {
		t.Fatalf("git ls-files hooks: %v", err)
	}

	var checked int
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		checked++
		for _, f := range Analyze(rel, string(b)) {
			t.Errorf("%s\n\n%s\n", f, f.Explain())
		}
	}

	// Scope is `hooks/` on purpose (see the package doc): scripts/ legitimately
	// builds and exercises sandbox binaries, and a check that fires there gets
	// switched off. But a scope that has silently emptied out is a check that
	// passes by finding nothing to look at, so assert it still has subjects.
	if checked == 0 {
		t.Fatal("no tracked files under hooks/ — this check is inspecting nothing")
	}
	t.Logf("%d tracked hook(s) checked", checked)
}

// TestMechanismObeysItsOwnRule closes the loop the sibling tickets kept
// leaving open: mg-2627 shipped a closing-keyword detector in a commit body
// that itself carried the hazard, and mg-2894's own subject is a rule about
// gates that call the installed binary. So this gate must not call it either.
//
// It does not, and this is why: the check is a Go package run by `go test`,
// which compiles the tree in front of it. It reads files and shells out to
// `git`. Nothing in its path goes stale between a merge and a self-deploy —
// there is no compiled pogo artifact anywhere in it to go stale.
//
// Asserted rather than asserted-in-prose, because a comment does not survive
// someone adding a convenient `pogo` call here later.
func TestMechanismObeysItsOwnRule(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil || len(entries) == 0 {
		t.Fatalf("glob own sources: %v (%d found)", err, len(entries))
	}
	// What counts as a violation here is EXECUTING the installed binary —
	// argv[0] of an exec being pogo or pogod. Not merely naming it: this file
	// is full of synthetic shell fixtures that contain the string `pogo ...`,
	// and they are data handed to Analyze, never run. An earlier draft matched
	// on the bare string and flagged those fixtures, which would have forced a
	// file-level exemption and blinded the test to a real call added later —
	// the one thing it exists to catch.
	//
	// The pattern is ASSEMBLED rather than written out, because the first run
	// flagged the line that declared it. That was the check working correctly
	// on a literal that really was present; the fix is to stop writing the
	// literal, not to except the file.
	execRe := regexp.MustCompile(`exec\.Command(Context)?\([^)]*?"(pog` + `o|pog` + `od)"`)

	// Prove the pattern can fire before trusting that it stays quiet. Without
	// this, a typo in the regex above turns the whole test into a no-op that
	// reports success — which is the exact failure shape this package is about.
	for _, sample := range []string{
		`out, err := exec.Command("pog` + `o", "check-commit-body").Output()`,
		`cmd := exec.CommandContext(ctx, "pog` + `od", "gate")`,
	} {
		if !execRe.MatchString(sample) {
			t.Fatalf("self-check pattern does not match a known violation: %s", sample)
		}
	}
	// ...and that it does NOT fire on the shell fixtures, which are data.
	if execRe.MatchString(`src: "#!/bin/sh\npog` + `o check-commit-body \"$1\"\n"`) {
		t.Fatal("self-check pattern fires on a shell fixture string; it would force an exemption")
	}

	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if execRe.MatchString(line) {
				t.Errorf("%s:%d execs the installed binary — this check would then break "+
					"in the very window it exists to police:\n\t%s",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The synthetic cases below cover shapes the repo does not currently contain.
// They are the reason this is preventive rather than archaeological: the next
// tracked gate someone writes is far more likely to look like these than like
// either specimen.
func TestSyntheticShapes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Kind
	}{
		{
			name: "bare call with no source route at all",
			src: "#!/bin/sh\nset -e\n" +
				"pogo check-commit-body \"$1\"\n",
			want: []Kind{KindNoSourceRoute},
		},
		{
			name: "pogod is covered too, not just pogo",
			src:  "#!/bin/sh\npogod refinery-gate \"$1\"\n",
			want: []Kind{KindNoSourceRoute},
		},
		{
			name: "presence guard with a source fallback below it — the 4866a26 shape",
			src: "#!/bin/sh\n" +
				"if command -v pogo >/dev/null 2>&1; then\n" +
				"    pogo check-commit-body \"$1\"\n" +
				"else\n" +
				"    go run ./cmd/pogo check-commit-body \"$1\"\n" +
				"fi\n",
			want: []Kind{KindPresenceNotCapability},
		},
		{
			name: "capability guard with a source fallback — the c0c203d shape",
			src: "#!/bin/sh\n" +
				"if command -v pogo >/dev/null 2>&1 && pogo check-commit-body --help >/dev/null 2>&1; then\n" +
				"    pogo check-commit-body \"$1\"\n" +
				"else\n" +
				"    go run ./cmd/pogo check-commit-body \"$1\"\n" +
				"fi\n",
			want: nil,
		},
		{
			name: "capability probe split across a wrapped guard",
			src: "#!/bin/sh\n" +
				"if [ -x \"$root/bin/pogo\" ] &&\n" +
				"    \"$root/bin/pogo\" check-commit-body --help >/dev/null 2>&1; then\n" +
				"    \"$root/bin/pogo\" check-commit-body \"$1\"\n" +
				"else\n" +
				"    go run ./cmd/pogo check-commit-body \"$1\"\n" +
				"fi\n",
			want: nil,
		},
		{
			name: "source-only gate, the shape the rule is asking for",
			src:  "#!/bin/sh\ngo run ./cmd/pogo check-commit-body \"$1\"\n",
			want: nil,
		},
		{
			name: "no pogo at all — hooks/pre-commit's shape",
			src:  "#!/bin/sh\ngofmt -l .\ngo build ./...\n",
			want: nil,
		},
		{
			name: "prose in a comment is not a code path",
			src: "#!/bin/sh\n" +
				"# The old version asked `command -v pogo` and stopped there.\n" +
				"go run ./cmd/pogo check-commit-body \"$1\"\n",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(Analyze("synthetic", tc.src))
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func render(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("  " + f.String() + "\n")
	}
	return b.String()
}
