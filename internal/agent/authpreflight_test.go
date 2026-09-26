package agent

import (
	"context"
	"errors"
	"testing"
)

func probeReturning(state AuthState, calls *int) func(context.Context, string) AuthReading {
	return func(context.Context, string) AuthReading {
		*calls++
		return AuthReading{State: state, Detail: "stub"}
	}
}

// drellem2/pogo#173: only a POSITIVE not-logged-in reading refuses, the probe
// runs once per sweep, and a command that is not the provider's own binary is
// never probed.
func TestAuthPreflightCheck(t *testing.T) {
	var calls int
	p := &Provider{ID: "claude", Binary: "claude", AuthPreflight: probeReturning(AuthNotLoggedIn, &calls)}
	pf := newAuthPreflight()

	err := pf.check("mayor", p, []string{"/opt/bin/claude", "--x"})
	if !errors.Is(err, ErrHarnessNotLoggedIn) {
		t.Fatalf("positive not-logged-in: err = %v, want ErrHarnessNotLoggedIn", err)
	}
	if err := pf.check("pm-pogo", p, []string{"/opt/bin/claude"}); !errors.Is(err, ErrHarnessNotLoggedIn) {
		t.Fatalf("second agent: err = %v", err)
	}
	if calls != 1 {
		t.Errorf("probe ran %d times in one sweep, want 1", calls)
	}

	// A sandbox's `sleep 600`, or a wrapper script, says nothing about the login.
	if err := pf.check("mayor", p, []string{"sleep", "600"}); err != nil {
		t.Errorf("non-provider binary was gated: %v", err)
	}
	// A new sweep re-checks: nothing is latched.
	if err := newAuthPreflight().check("mayor", p, []string{"claude"}); err == nil || calls != 2 {
		t.Errorf("a new sweep did not re-probe (calls=%d, err=%v)", calls, err)
	}

	for _, st := range []AuthState{AuthUnknown, AuthLoggedIn} {
		q := &Provider{ID: "claude", Binary: "claude", AuthPreflight: probeReturning(st, &calls)}
		if err := newAuthPreflight().check("mayor", q, []string{"claude"}); err != nil {
			t.Errorf("state %v refused the spawn: %v — only a positive not-logged-in may", st, err)
		}
	}

	// No probe declared, no preflight, or no provider: nothing to check.
	if err := newAuthPreflight().check("mayor", &Provider{ID: "codex", Binary: "codex"}, []string{"codex"}); err != nil {
		t.Error(err)
	}
	var nilPF *authPreflight
	if err := nilPF.check("mayor", p, []string{"claude"}); err != nil {
		t.Errorf("nil preflight (a manual start) was gated: %v", err)
	}
	if err := newAuthPreflight().check("mayor", nil, []string{"claude"}); err != nil {
		t.Error(err)
	}
}
