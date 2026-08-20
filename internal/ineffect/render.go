package ineffect

// The human rendering. Two design constraints, both from the ticket:
//
//  1. THE VERDICT WORD AND THE CARRIER NAME TRAVEL TOGETHER. A reader who skims
//     one line has to come away with "which artifact am I acting through", not
//     just "green/red" — the failure being fixed is precisely a correct global
//     answer applied to the wrong artifact.
//  2. EVERY ROW NAMES WHERE IT WAS MEASURED. The ticket's two live instances
//     were both caught by someone testing rather than inferring; a report whose
//     rows cannot be re-run by hand asks to be inferred from.

import (
	"fmt"
	"sort"
	"strings"
)

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder

	if r.Err != "" {
		fmt.Fprintf(&b, "in-effect: %s\n", r.Summary)
		return b.String()
	}

	fmt.Fprintf(&b, "commit %s", short(r.Commit))
	if r.When != "" {
		fmt.Fprintf(&b, "  (%s)", r.When)
	}
	b.WriteString("\n")
	if r.Subject != "" {
		fmt.Fprintf(&b, "  %s\n", r.Subject)
	}
	b.WriteString("\n")

	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s — %d file(s): %s\n", f.Class, len(f.Paths), pathsPreview(f.Paths, 4))
		for _, c := range f.Carriers {
			line := fmt.Sprintf("  %-7s %s", strings.ToUpper(string(c.Verdict)), c.Carrier)
			if c.At != "" {
				line += fmt.Sprintf(" (%s)", c.At)
			}
			fmt.Fprintf(&b, "%s\n", line)
			fmt.Fprintf(&b, "          %s\n", c.Why)
			if c.Remedy != "" {
				fmt.Fprintf(&b, "          remedy: %s\n", c.Remedy)
			}
		}
		if f.Note != "" {
			fmt.Fprintf(&b, "  note: %s\n", f.Note)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "%s\n", r.Summary)
	if hint := r.remedyHint(); hint != "" {
		fmt.Fprintf(&b, "%s\n", hint)
	}
	// The report says which build produced it, unconditionally. This command
	// is itself compiled into the pogo CLI and is therefore subject to the
	// condition it reports; a reader holding an old `pogo` is reading that
	// build's classification rules, and the only way to know is to be told.
	fmt.Fprintf(&b, "reported by %s — these are that build's rules, and it is itself an artifact of the `compiled` class.\n", reporterOr(r.Reporter))
	// The provenance caveat renders only when it APPLIES. A warning printed on
	// every run — including the runs where the reporter's revision came from
	// the build script and is trustworthy — is a warning readers learn to skip,
	// and this one has to survive to the run where it matters.
	if !strings.Contains(r.Reporter, "source="+trustedSource) {
		b.WriteString("  (that identity is NOT a trustworthy statement about this repo: only `source=ldflags` is stamped by a build that knew which repo it was building — a binary built in a linked worktree carries the ENCLOSING repository's HEAD, mg-8d0f)\n")
	}
	return b.String()
}

// trustedSource is the one internal/version provenance that names this repo by
// construction. Kept as a constant beside its use so the condition above reads
// as the claim it is.
const trustedSource = "ldflags"

// reporterOr never returns an empty string: a blank where an identity belongs
// reads as "no drift here" and means "no stamp was found".
func reporterOr(desc string) string {
	if desc == "" {
		return "an <unstamped> pogo"
	}
	return desc
}

// remedyHint collects the distinct remedies of every non-live carrier into one
// closing line. Repeating a remedy under each of five rows buries the fact that
// there are only two actions owed.
func (r Report) remedyHint() string {
	seen := map[string]bool{}
	var rs []string
	for _, f := range r.Findings {
		for _, c := range f.Carriers {
			if c.Verdict == Live || c.Verdict == NotApplicable || c.Remedy == "" || seen[c.Remedy] {
				continue
			}
			seen[c.Remedy] = true
			rs = append(rs, c.Remedy)
		}
	}
	if len(rs) == 0 {
		return ""
	}
	sort.Strings(rs)
	return "owed: " + strings.Join(rs, "; ")
}

// ExitCode maps the overall verdict onto the CLI's shared code vocabulary. It
// is a distinct value per verdict on purpose: a caller that gates on this
// command must be able to tell "measured, and not in effect" from "could not
// measure", because a check that goes green on an absent measurement is the
// defect this command exists to remove.
func (r Report) ExitCode() int {
	switch r.Overall {
	case OverallLive, OverallNoCarriers:
		return 0
	case OverallInert, OverallHalfLive:
		return 1
	default:
		return 3
	}
}
