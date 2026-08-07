package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// closingRefRepo builds origin + a clone with one commit on main, and returns
// the clone dir. Callers add branch commits on top.
//
// It also installs a stub `gh` that reports no pull request. The gate asks gh
// for the branch's PR body (mg-f9e0), and a developer machine with a real,
// authenticated gh on PATH would otherwise send these commit-message tests out
// to the network — where the answer depends on someone else's GitHub state.
// Tests that care about the PR half install their own stub over this one.
func closingRefRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	fakeGH(t, `echo 'no pull requests found for branch' >&2; exit 1`)
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")
	return workDir
}

// commitWithBody commits a file change carrying the given multi-line message.
// The message goes through a file, not -m, so the wrap is preserved exactly.
func commitWithBody(t *testing.T, dir, filename, message string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, filename), []byte("package main\n// "+filename+"\n"), 0644)
	run(t, dir, "git", "add", ".")
	msgFile := filepath.Join(t.TempDir(), "msg.txt")
	if err := os.WriteFile(msgFile, []byte(message), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "commit", "-F", msgFile)
}

// realIncidentMessage reproduces e83f394's wrap: "nobody closed" ends one line
// and the reference opens the next. This is the only real instance we have of
// the failure, and it is the case a same-line check misses.
const realIncidentMessage = `feat(ghteardown): detect gh-issue carriers that reached done while their issue stayed OPEN

mg-07ba reached ` + "`status=done, stage: merge`" + ` on 2026-07-17. The work genuinely
completed and every promise in the thread was fulfilled — but nobody closed
drellem2/pogo#89, and it sat OPEN from Jul 17 to Jul 21. No alarm fired, because
from the outside a carrier that completed its teardown and one that skipped it
are the same three characters: ` + "`done`" + `.
`

// TestClosingRefGateCatchesWrappedProse is the acceptance criterion's catch
// half, exercised through real git rather than a string: the message has to
// survive being written, committed and read back by `git log --format=%B` with
// its newlines intact, because the newline IS the defect.
func TestClosingRefGateCatchesWrappedProse(t *testing.T) {
	workDir := closingRefRepo(t)
	run(t, workDir, "git", "checkout", "-b", "polecat-2627")
	commitWithBody(t, workDir, "teardown.go", realIncidentMessage)

	err := checkClosingRefs(workDir, "main", "polecat-2627")
	if err == nil {
		t.Fatal("MISS: the refinery would have merged the commit that closed drellem2/pogo#89")
	}
	msg := err.Error()
	for _, want := range []string{"drellem2/pogo#89", "WRAPPED", "Refs drellem2/pogo#89"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message omits %q — an author cannot act on it:\n%s", want, msg)
		}
	}
}

// TestClosingRefGatePassesNeutralForms is the other required half. A check only
// observed catching has not been shown to permit, and one that blocks every
// issue reference will be disabled within a week.
func TestClosingRefGatePassesNeutralForms(t *testing.T) {
	cases := []struct {
		name    string
		message string
	}{
		{
			"refs trailer",
			"feat: land the detector\n\nSurfaces the miss without acting on it.\n\nRefs drellem2/pogo#89\n",
		},
		{
			"prose mentioning an issue with no keyword nearby",
			"fix: route teardown misses to the fleet\n\nThe thread behind pogo issue 89 went quiet for four days. See\ndrellem2/pogo#89 for the reporter's transcript.\n",
		},
		{
			"acknowledged deliberate closure",
			"chore: complete the carrier teardown\n\nCloses #89 now that the work has landed.\n\nClosing-ref-ack: #89 — intentional; teardown lands with this commit\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := closingRefRepo(t)
			run(t, workDir, "git", "checkout", "-b", "polecat-pass")
			commitWithBody(t, workDir, "pass.go", tc.message)

			if err := checkClosingRefs(workDir, "main", "polecat-pass"); err != nil {
				t.Errorf("false positive — this commit closes nothing on GitHub:\n%v", err)
			}
		})
	}
}

// TestClosingRefGateIgnoresCommitsAlreadyOnTarget: the gate judges what the
// branch ADDS. e83f394 is already on main; flagging it would wedge every
// subsequent MR behind a message nobody can rewrite.
func TestClosingRefGateIgnoresCommitsAlreadyOnTarget(t *testing.T) {
	workDir := closingRefRepo(t)
	// Land the hazard on main itself, as history the branch inherits.
	commitWithBody(t, workDir, "landed.go", realIncidentMessage)
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "polecat-clean")
	commitWithBody(t, workDir, "clean.go", "feat: something unrelated\n\nRefs drellem2/pogo#89\n")

	if err := checkClosingRefs(workDir, "main", "polecat-clean"); err != nil {
		t.Errorf("gate flagged inherited history rather than the branch's own commits:\n%v", err)
	}
}

// TestClosingRefGateFailsClosedOnUnreadableHistory: a failed enumeration is not
// an all-clear. Same reasoning as ghteardown's "indeterminate" class — an
// unreadable answer and a clean one are indistinguishable to a careless check.
func TestClosingRefGateFailsClosedOnUnreadableHistory(t *testing.T) {
	workDir := closingRefRepo(t)
	if err := checkClosingRefs(workDir, "main", "branch-that-does-not-exist"); err == nil {
		t.Fatal("gate passed a range it could not read")
	}
}

// cleanBranch puts a branch on workDir whose commits close nothing, so a
// failure can only have come from the PR body.
func cleanBranch(t *testing.T, workDir, branch string) {
	t.Helper()
	run(t, workDir, "git", "checkout", "-b", branch)
	commitWithBody(t, workDir, "pr.go", "feat: the change under review\n\nRefs drellem2/pogo#111\n")
}

// TestClosingRefGateCatchesPRBody is the defect this gate was blind to
// (mg-f9e0): the shipped build-pr template told every builder to write
// "Resolves <owner>/<repo>#<n>" in the PR BODY, which closes the whole issue on
// merge, and the gate read commit messages only. The commits here are clean, so
// a pass means the gate is still pointed at the wrong artifact.
func TestClosingRefGateCatchesPRBody(t *testing.T) {
	workDir := closingRefRepo(t)
	cleanBranch(t, workDir, "polecat-f9e0")
	fakeGH(t, `printf '%s\n' '{"number":57,"body":"Adds the config key.\n\nResolves drellem2/pogo#111\n\nWork item: mg-f9e0\n"}'`)

	err := checkClosingRefs(workDir, "main", "polecat-f9e0")
	if err == nil {
		t.Fatal("MISS: the refinery would have merged a PR body that closes drellem2/pogo#111")
	}
	msg := err.Error()
	// The report has to name the artifact and the command that edits it. An
	// author told to amend a commit will go looking for a string that is in no
	// commit, and conclude the check is broken.
	for _, want := range []string{"pull request #57 body", "drellem2/pogo#111", "gh pr edit", "Refs owner/repo#N"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message omits %q — an author cannot act on it:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "amend the message and re-push") {
		t.Errorf("failure message offers the COMMIT remedy for a PR body:\n%s", msg)
	}
}

// TestClosingRefGatePRBodyAckPasses: the escape has to work in the artifact it
// is written in, or the only way past the gate is to drop the closure the
// author meant. gh#111's split was released by a human on exactly this
// judgement; the trailer is where that judgement gets recorded.
func TestClosingRefGatePRBodyAckPasses(t *testing.T) {
	workDir := closingRefRepo(t)
	cleanBranch(t, workDir, "polecat-acked")
	fakeGH(t, `printf '%s\n' '{"number":57,"body":"Resolves drellem2/pogo#111\n\nClosing-ref-ack: drellem2/pogo#111 — intentional; this PR discharges the issue in full\n"}'`)

	if err := checkClosingRefs(workDir, "main", "polecat-acked"); err != nil {
		t.Errorf("acknowledged closure rejected — the deliberate path is unavailable:\n%v", err)
	}
}

// TestClosingRefGatePRBodyNeutralPasses: `Refs` is the template's default now,
// so the default path must merge. A gate that bounced it would be reverted.
func TestClosingRefGatePRBodyNeutralPasses(t *testing.T) {
	workDir := closingRefRepo(t)
	cleanBranch(t, workDir, "polecat-neutral")
	fakeGH(t, `printf '%s\n' '{"number":57,"body":"Adds the config key.\n\nRefs drellem2/pogo#111\n\nWork item: mg-f9e0\n"}'`)

	if err := checkClosingRefs(workDir, "main", "polecat-neutral"); err != nil {
		t.Errorf("false positive on the template's own default body:\n%v", err)
	}
}

// TestClosingRefGatePRBodyFailsSoftOnBrokenGH pins the stated residual rather
// than leaving it to be discovered. Every internal-track branch has no PR, and
// a gh that is missing, unauthenticated or offline is not a property of the
// branch under judgement — so an unreadable PR body must not stop the merge.
// The log line is the only record that the body went unguarded.
func TestClosingRefGatePRBodyFailsSoftOnBrokenGH(t *testing.T) {
	workDir := closingRefRepo(t)
	cleanBranch(t, workDir, "polecat-nogh")
	fakeGH(t, `echo "gh: could not determine base repo" >&2; exit 1`)

	if err := checkClosingRefs(workDir, "main", "polecat-nogh"); err != nil {
		t.Errorf("a broken gh stopped a merge whose commits are clean:\n%v", err)
	}
}

// TestClosingRefGateReportsBothArtifacts: a branch can carry the hazard in both
// places, and a report that stops at the first one sends the author round twice.
func TestClosingRefGateReportsBothArtifacts(t *testing.T) {
	workDir := closingRefRepo(t)
	run(t, workDir, "git", "checkout", "-b", "polecat-both")
	commitWithBody(t, workDir, "both.go", realIncidentMessage)
	fakeGH(t, `printf '%s\n' '{"number":57,"body":"Resolves drellem2/pogo#111\n"}'`)

	err := checkClosingRefs(workDir, "main", "polecat-both")
	if err == nil {
		t.Fatal("gate passed a branch carrying the hazard in both artifacts")
	}
	for _, want := range []string{"drellem2/pogo#89", "pull request #57 body", "drellem2/pogo#111"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("report omits %q — the author fixes one artifact and bounces on the other:\n%s", want, err)
		}
	}
}
