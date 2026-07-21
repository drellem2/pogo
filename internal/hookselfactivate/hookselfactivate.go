// Package hookselfactivate checks that a tracked gate in this repo activates
// from SOURCE rather than from the installed pogo/pogod binary.
//
// # The class
//
// Tracked files — hooks, prompts, templates — go live the instant a merge
// lands. Compiled `pogo` and `pogod` go live only when self-deploy runs. Those
// are two different clocks, and anything that couples a tracked file to
// compiled behaviour is broken for the whole window between them.
//
// mg-2627 walked into it: a tracked commit-msg hook shipped calling a CLI
// subcommand that existed only in a binary nobody had deployed, so it rejected
// EVERY commit in the repo — benign and hazardous alike — and it looked fine to
// whoever merged it. That last part is the defining property of the class.
//
// The rule, from the mg-2894 architect finding: a gate that must be live on
// merge should SELF-ACTIVATE FROM SOURCE rather than call the installed binary.
//
// # Why two rules and not one
//
// The obvious check — "does the hook have a `go run ./cmd/...` fallback?" — is
// not the rule, and would have PASSED the hook that broke the repo. The
// original (4866a26) had a source fallback sitting right there in its `else`
// arm. It was never reached, because the arms above it selected the installed
// binary on PRESENCE:
//
//	if [ -x "$repo_root/bin/pogo" ]; then ...
//	elif command -v pogo >/dev/null 2>&1; then ...
//	else (cd "$repo_root" && go run ./cmd/pogo ...)
//
// A stale `pogo` was on PATH, so arm two won and died on `unknown command`.
// The fix (c0c203d, mg-d1f7) changed the guards to probe CAPABILITY — it asks
// each candidate whether it actually HAS the subcommand — so a stale binary
// falls through to source instead of winning.
//
// So the check enforces both halves:
//
//  1. A file that invokes pogo/pogod must have a source route at all.
//  2. Every guard that SELECTS an installed binary must probe capability, not
//     mere presence. A presence-only guard makes the source route unreachable,
//     which is indistinguishable from not having one.
//
// Rule 2 is the one with teeth. It is what separates 4866a26 from c0c203d, and
// TestCatchesTheOriginalHook proves that on the real specimens.
//
// # No window figure appears anywhere in this package
//
// The finding that prompted this observed the installed binary three days
// behind origin/main. That number is stale by construction — it decays hourly
// — so nothing here measures it, prints it, or branches on it. A control that
// carries a literal fact rots and then breaks the next legitimate change. The
// rule holds whether the window is three days or three minutes; the only thing
// that matters is that the two clocks are different at all.
//
// # This package obeys its own rule
//
// The mechanism is a Go package exercised by `go test`, which compiles the
// current tree. It shells out to `git` and reads files; it never invokes pogo
// or pogod, so it cannot be broken by the deploy state it exists to police.
// TestMechanismObeysItsOwnRule asserts that, rather than leaving it to a
// comment that a later edit can quietly falsify.
package hookselfactivate

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind names the two ways a tracked gate can end up depending on deploy state.
type Kind string

const (
	// KindNoSourceRoute: the file invokes pogo/pogod and has no `go run
	// ./cmd/...` route at all, so it does nothing until a redeploy.
	KindNoSourceRoute Kind = "no-source-route"

	// KindPresenceNotCapability: a guard selects an installed binary on
	// presence alone. This is the 4866a26 failure — a source route exists
	// but is unreachable whenever a stale binary is installed, which is
	// exactly the window the source route was written for.
	KindPresenceNotCapability Kind = "presence-not-capability"
)

// Finding is one violation of the rule.
type Finding struct {
	File string
	Line int // 1-based; 0 when the finding is about the file as a whole
	Kind Kind
	// Evidence is the offending source text, comments stripped.
	Evidence string
}

func (f Finding) String() string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return fmt.Sprintf("%s: %s: %s", loc, f.Kind, f.Evidence)
}

// Explain returns the remedy for a finding, phrased for someone who has just
// had a commit blocked and does not yet know why this rule exists.
func (f Finding) Explain() string {
	switch f.Kind {
	case KindNoSourceRoute:
		return "This tracked file calls pogo/pogod but has no `go run ./cmd/...` route.\n" +
			"Tracked files go live at MERGE; the compiled binary goes live only at\n" +
			"self-deploy. Between the two, this gate is calling a binary that does not\n" +
			"yet do what the merge assumes — it will fail, or pass, for reasons that have\n" +
			"nothing to do with what it checks. Add a source route it can fall through to."
	case KindPresenceNotCapability:
		return "This guard selects an installed binary on PRESENCE, not CAPABILITY.\n" +
			"A stale binary satisfies `command -v` / `[ -x ... ]` and wins the route,\n" +
			"so any source fallback below it is unreachable in precisely the window it\n" +
			"was written for. Ask the candidate whether it HAS the behaviour — e.g.\n" +
			"`\"$candidate\" <subcommand> --help >/dev/null 2>&1` alongside the presence\n" +
			"test — so a stale binary falls through instead. See hooks/commit-msg."
	}
	return ""
}

// Comment stripping. A `#` starts a comment when it opens a word; a `#` inside
// a word (`repo#89`, `${x#pfx}`) does not. Comments must go before analysis:
// hooks/commit-msg discusses `command -v pogo` in its own prose, and prose is
// not a code path.
var commentRe = regexp.MustCompile(`(^|\s)#.*$`)

func stripComment(line string) string {
	return commentRe.ReplaceAllString(line, "")
}

// A source route: `go run ./cmd/pogo`, `go run ./cmd/...`, with or without
// flags between. This is the arm that decouples the gate from deploy state.
var sourceRouteRe = regexp.MustCompile(`\bgo\s+run\s+[^|;&]*\./cmd/`)

// A reference to the installed binary, as a command word or a path ending in
// pogo/pogod. Quotes are tolerated because that is how hooks write it.
var candidateRe = regexp.MustCompile(`"?\$?[\w./{}$-]*\b(pogod|pogo)"?\b`)

// Presence-only probes. Each answers "does something named pogo exist" and
// nothing about what it can do.
var presenceRe = regexp.MustCompile(`(command\s+-v|which|type|hash)\s+"?(pogod|pogo)"?|\[\[?\s*-[xfe]\s+"?[^]]*\b(pogod|pogo)"?\s*\]\]?`)

// A capability probe: the candidate is INVOKED, with a word after it. That
// word is the subcommand or flag whose existence is in question. This is the
// distinction the whole package turns on — `command -v pogo` versus
// `"$repo_root/bin/pogo" check-commit-body --help`.
var capabilityProbeRe = regexp.MustCompile(`"?\$?[\w./{}$-]*\b(pogod|pogo)"?\s+[\w-]`)

// Analyze reports every way the given shell source coupled itself to the
// installed binary. name is used only for Finding.File.
//
// Analyze takes source text rather than a path so the positive controls can
// feed it historical revisions straight out of `git show`, with no checkout
// and nothing written to disk.
func Analyze(name, src string) []Finding {
	lines := strings.Split(src, "\n")
	code := make([]string, len(lines))
	for i, l := range lines {
		code[i] = stripComment(l)
	}
	joined := strings.Join(code, "\n")

	// A file that never reaches for pogo/pogod has nothing to couple. This is
	// what keeps hooks/pre-commit — gofmt and go build, no pogo at all — out
	// of the report instead of needing an exemption.
	if !referencesInstalledBinary(code) {
		return nil
	}

	var findings []Finding

	if !sourceRouteRe.MatchString(joined) {
		findings = append(findings, Finding{
			File:     name,
			Kind:     KindNoSourceRoute,
			Evidence: "no `go run ./cmd/...` route anywhere in the file",
		})
	}

	for _, g := range guards(code) {
		for _, clause := range splitClauses(g.text) {
			if !presenceRe.MatchString(clause) {
				continue
			}
			// The guard names a candidate by presence. It is only safe if the
			// SAME guard also runs it. Checked across the whole guard, not the
			// clause: the fixed hook spreads the presence test and the probe
			// across two `&&`-joined clauses on two lines, which is correct
			// and must not read as a violation.
			if capabilityProbeRe.MatchString(g.text) {
				continue
			}
			findings = append(findings, Finding{
				File:     name,
				Line:     g.line,
				Kind:     KindPresenceNotCapability,
				Evidence: strings.TrimSpace(g.text),
			})
			break // one finding per guard is enough to send someone to it
		}
	}

	return findings
}

// referencesInstalledBinary reports whether any line names pogo/pogod outside
// a `go run` route. `go run ./cmd/pogo` mentions pogo but is the remedy, not
// the hazard.
func referencesInstalledBinary(code []string) bool {
	for _, l := range code {
		if !candidateRe.MatchString(l) {
			continue
		}
		if sourceRouteRe.MatchString(l) && !presenceRe.MatchString(l) {
			// Line is purely a source route.
			withoutSource := sourceRouteRe.ReplaceAllString(l, "")
			if !candidateRe.MatchString(withoutSource) {
				continue
			}
		}
		return true
	}
	return false
}

type guard struct {
	line int
	text string
}

// guards extracts the condition text of every `if`/`elif`, from the keyword up
// to `then`. Conditions in this repo's hooks wrap across lines on `&&`, so a
// line-at-a-time reading would split a presence test away from the capability
// probe that redeems it and report a false violation.
func guards(code []string) []guard {
	var out []guard
	for i := 0; i < len(code); i++ {
		f := strings.Fields(code[i])
		if len(f) == 0 || (f[0] != "if" && f[0] != "elif") {
			continue
		}
		start := i
		var b strings.Builder
		for ; i < len(code); i++ {
			b.WriteString(" ")
			b.WriteString(code[i])
			if hasThen(code[i]) {
				break
			}
		}
		out = append(out, guard{line: start + 1, text: b.String()})
	}
	return out
}

func hasThen(line string) bool {
	for _, f := range strings.Fields(strings.ReplaceAll(line, ";", " ; ")) {
		if f == "then" {
			return true
		}
	}
	return false
}

// splitClauses breaks a condition on its shell operators so each test is
// classified on its own.
var clauseSplitRe = regexp.MustCompile(`&&|\|\||;`)

func splitClauses(s string) []string {
	return clauseSplitRe.Split(s, -1)
}
