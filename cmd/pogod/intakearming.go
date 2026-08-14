package main

// The gh-issue INTAKE detector's arming preconditions, as a decision separated
// from the wiring that acts on it (mg-fb29).
//
// # Why this is a named function and not three lines of `if` inside main()
//
// The wiring in main() is not reachable from a test: it lives inside a 1000-line
// startup path that opens sockets, spawns agents and installs launchd jobs. So a
// gate written inline is a gate whose only evidence is that somebody read it —
// which is the mg-a03d shape, a control that is present, correct-looking, and
// never actually asserted. Pulling the DECISION out costs one type and makes the
// precondition table a thing a test can enumerate; main() keeps the actions.
//
// # The preconditions, and why the credential is one of them
//
// The original gate was `exec.LookPath("gh")`, with the argument written beside
// it: "A missing gh is a precondition, not a finding." Without the binary every
// repo lookup fails, and the runner would faithfully report one environment gap
// as a wall of unreadable repos — noise that gets a detector muted before the run
// that matters.
//
// mg-fb29 extends that accepted argument from the BINARY to the CREDENTIAL,
// because it is the same shape: one global cause, invisible from any per-repo
// view, amplified into N findings. The credential case was measured to cost more
// than the PATH case ever did — the N-unreadable-repos message lists "expired or
// missing gh auth" first among four guesses, and nine days of a person's queue
// went into chasing that guess on a host whose token was valid the whole time.
type intakeArming string

const (
	// intakeArmed: gh is reachable and a credential exists. Run the detector.
	intakeArmed intakeArming = "armed"
	// intakeBlockedNoGH: `gh` is not on pogod's PATH. Remedy is the launchd
	// plist's EnvironmentVariables.PATH.
	intakeBlockedNoGH intakeArming = "no_gh_binary"
	// intakeBlockedNoCredential: gh is here and cannot authenticate. Remedy is
	// `gh auth login` (or an export somewhere a non-interactive shell reads).
	intakeBlockedNoCredential intakeArming = "no_credential"
)

// decideIntakeArming picks the arming outcome from the two preconditions.
//
// ORDER IS LOAD-BEARING: a missing binary is reported ahead of a missing
// credential, and it is not merely a stylistic precedence. When `gh` is absent,
// `gh auth token` cannot run either, so the credential predicate is false as a
// CONSEQUENCE of the same fault — and telling that host to run `gh auth login`
// would hand its operator a command they do not have. Reporting the downstream
// symptom instead of the cause is precisely the defect this ticket exists to fix,
// so the gate must not commit it one level up.
func decideIntakeArming(ghOnPath, credentialOK bool) intakeArming {
	switch {
	case !ghOnPath:
		return intakeBlockedNoGH
	case !credentialOK:
		return intakeBlockedNoCredential
	default:
		return intakeArmed
	}
}
