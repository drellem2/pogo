package refinery

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The three specimens below are the push refusals VERBATIM from
// drellem2/pogo#164: the first instance (drellem2/pogo main, 2026-08-26), the
// second (payitgov/cool-ui-libraries --target=develop, 2026-09-03) and the third
// (payitgov/.pogo main, 2026-09-10). Each was recorded class=contention,
// signal="target moved during merge", and spent 7 of 7 attempts.
const (
	gh013Main = "remote: error: GH013: Repository rule violations found for refs/heads/main.\n" +
		"remote: - Changes must be made through a pull request.\n" +
		" ! [remote rejected] main -> main (push declined due to repository rule violations)"
	gh013Develop = "remote: error: GH013: Repository rule violations found for refs/heads/develop.\n" +
		"remote: - Changes must be made through a pull request.\n" +
		"remote: - 2 of 2 required status checks are expected.\n" +
		"remote: - 4 of 4 required status checks are expected.\n" +
		" ! [remote rejected]     develop -> develop (push declined due to repository rule violations)"
	// A portable equivalent: a bare remote pre-receive hook, as measured in a
	// scratch bare repo by the triage (mg-c33d). No GitHub vocabulary at all.
	preReceiveDeclined = " ! [remote rejected] main -> main (pre-receive hook declined)\n" +
		"error: failed to push some refs to '/tmp/x/remote.git'"
	// GitLab's wording for a protected branch.
	gitlabProtected = "remote: GitLab: You are not allowed to push code to protected branches on this project.\n" +
		" ! [remote rejected] main -> main (pre-receive hook declined)"
)

// pushFailure builds the error exactly as land.go's vcs.StepPush arm does: a
// gitStepError carrying git's output, wrapped in retryableError. The wrap is the
// mechanism of the bug — it is what made every refusal look like contention.
func pushFailure(out string) error {
	return &retryableError{gitStepFail("push",
		fmt.Sprintf("push: %s: %v", out, errors.New("exit status 1")),
		[]string{"push", "origin", "HEAD:main"}, out, errors.New("exit status 1"))}
}

func TestProtectionRefusalIsTerminalNotContention(t *testing.T) {
	cases := []struct {
		name, out, ref, signal string
	}{
		{"gh013-main", gh013Main, "refs/heads/main", "gh013"},
		{"gh013-develop", gh013Develop, "refs/heads/develop", "gh013"},
		{"pre-receive", preReceiveDeclined, "main", "[remote rejected]"},
		{"gitlab", gitlabProtected, "main", "protected branch"},
		// Refnames carrying a transient word: the reason is a rule decline, so
		// the refname must not make it a lock race (review round 1, mg-940ac).
		{"lock-in-refname", " ! [remote rejected] HEAD -> feature/clock-skew (pre-receive hook declined)",
			"feature/clock-skew", "[remote rejected]"},
		{"unblock-in-refname", " ! [remote rejected] HEAD -> release/unblock-ci (pre-receive hook declined)",
			"release/unblock-ci", "[remote rejected]"},
		{"lock-in-source", " ! [remote rejected] lockfile-bump -> main (pre-receive hook declined)",
			"main", "[remote rejected]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pushFailure(tc.out)
			d := classifyFailure("push", rawOf(err), err)
			if d.Class != ClassProtected {
				t.Fatalf("class = %q signal = %q, want protected — a rule refusal is not a race", d.Class, d.Signal)
			}
			if d.Retryable {
				t.Error("retryable = true: no number of attempts changes a rule")
			}
			if d.Signal != tc.signal {
				t.Errorf("signal = %q, want %q", d.Signal, tc.signal)
			}
			if !strings.Contains(d.Reason, tc.ref) {
				t.Errorf("reason does not name the refused ref %q: %q", tc.ref, d.Reason)
			}
			if strings.Contains(d.Signal, "target moved") {
				t.Errorf("signal asserts a race nobody observed: %q", d.Signal)
			}
		})
	}
}

// The control on the test above: the classifier must still separate a
// remote-side decline from a genuine lost race. git words a non-fast-forward
// with "[rejected]", not "[remote rejected]", and a concurrent ref update the
// remote could not LOCK is the one way the remote-rejected flag can carry a race.
func TestGenuineContentionIsNotMadeProtected(t *testing.T) {
	for name, out := range map[string]string{
		"non-fast-forward": " ! [rejected]        main -> main (fetch first)\n" +
			"error: failed to push some refs to 'git@github.com:o/r.git'\n" +
			"hint: Updates were rejected because the remote contains work that you do not have locally.",
		"lock-race":     " ! [remote rejected] main -> main (cannot lock ref 'refs/heads/main': is at abc but expected def)",
		"failed-lock":   " ! [remote rejected] main -> main (failed to lock)",
		"unpacker":      " ! [remote rejected] main -> main (unpacker error)",
		"update-failed": " ! [remote rejected] main -> main (failed to update ref)",
	} {
		t.Run(name, func(t *testing.T) {
			err := pushFailure(out)
			d := classifyFailure("push", rawOf(err), err)
			if d.Class != ClassContention || !d.Retryable {
				t.Errorf("class=%q retryable=%v signal=%q, want contention/true — this is not a rule refusal",
					d.Class, d.Retryable, d.Signal)
			}
		})
	}
}

// A refusal whose text is only in the error (no raw output captured) is still
// read, and the ref still named.
func TestProtectionRefusalReadFromTheErrorWhenRawIsEmpty(t *testing.T) {
	err := &retryableError{errors.New("push: " + gh013Main + ": exit status 1")}
	d := classifyFailure("push", "", err)
	if d.Class != ClassProtected || !strings.Contains(d.Reason, "refs/heads/main") {
		t.Errorf("class=%q reason=%q", d.Class, d.Reason)
	}
}

// A refused ref the output does not name is not guessed.
func TestRefusedRefIsNotGuessed(t *testing.T) {
	d := classifyFailure("push", "remote: error: GH013: something new", pushFailure("x"))
	if d.Class != ClassProtected || !strings.Contains(d.Reason, "the target") {
		t.Errorf("class=%q reason=%q", d.Class, d.Reason)
	}
}

// The remedy is PORTABLE (Daniel, closing drellem2/pogo#69, re-affirmed in the
// #164 GO): target a different branch. A host-specific "open a pull request"
// is exactly the wording that ruling rejected.
func TestProtectedRemedyIsPortable(t *testing.T) {
	err := pushFailure(gh013Main)
	d := classifyFailure("push", rawOf(err), err)
	for _, text := range []string{d.Reason, ClassProtected.TriageNote()} {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "pull request") || strings.Contains(lower, " pr ") {
			t.Errorf("remedy is host-specific: %q", text)
		}
		if !strings.Contains(lower, "different branch") || !strings.Contains(text, "--target") {
			t.Errorf("remedy does not say to target a different branch: %q", text)
		}
		if !strings.Contains(text, "do NOT dispatch") {
			t.Errorf("remedy does not say not to dispatch a fix: %q", text)
		}
	}
	if strings.Contains(ClassProtected.TriageNote(), "Resubmit;") {
		t.Errorf("the protected note carries contention's bare resubmit advice: %q", ClassProtected.TriageNote())
	}
}

// Terminal at attempt 1: the policy must not spend the contention budget on it.
// Measured before this class: 7 of 7 attempts in 52s, per submission.
func TestProtectedIsNotRetriedAtAttemptOne(t *testing.T) {
	err := pushFailure(gh013Main)
	d := classifyFailure("push", rawOf(err), err)
	retry, backoff, reason, _ := newRetryPolicy(defaultMaxAttempts).decide(d)
	if retry || backoff != 0 {
		t.Fatalf("retry=%v backoff=%s, want no retry at attempt 1", retry, backoff)
	}
	if !strings.Contains(reason, "refs/heads/main") {
		t.Errorf("not-retried reason does not carry the classifier's sentence: %q", reason)
	}
}

// A protected target is a property of the REPOSITORY: every branch submitted to
// it is refused identically, so it must not feed an escalation about a person.
func TestProtectedDoesNotCountAgainstAuthor(t *testing.T) {
	if countsAgainstAuthor(ClassProtected) {
		t.Error("a protected-target refusal counts against the author")
	}
}

// The refusal is the remote's answer to a ref UPDATE, so only the push stage is
// read for it. A non-push step whose output happens to carry the wording keeps
// its existing class.
func TestProtectionIsReadOnlyAtThePushStage(t *testing.T) {
	err := &retryableError{gitStepFail("merge-ff-only", "merge (ff-only): "+gh013Main,
		[]string{"merge", "--ff-only"}, gh013Main, errors.New("exit status 1"))}
	if d := classifyFailure("rebase", rawOf(err), err); d.Class == ClassProtected {
		t.Errorf("non-push stage classified protected: signal=%q", d.Signal)
	}
}
