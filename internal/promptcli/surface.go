// Package promptcli checks the `mg …` and `pogo …` invocations that shipped
// prompts instruct against the real accept-surface of those CLIs.
//
// # Why this package exists (mg-21b1)
//
// Three shipped prompts in one night each told their reader to run something
// the CLI rejects, and all three were found by someone RUNNING the command —
// never by reading the prompt:
//
//   - mg-159a: mayor.md's type table said to omit `--template` for a bare
//     `task` item. The type→template map is closed Go and refuses with a 409;
//     `task` is the DEFAULT type, so the prompt was wrong about the ordinary
//     dispatch, not an edge case.
//   - mg-4bb9: three prompts said `mg edit` has "no append/comment subcommand"
//     and to mail the note instead. `--append-body` and `--append-body-file`
//     exist, and `mg edit --help` OPENS by recommending the second one.
//   - mg-d8ea: pm-template.md said `mg spend --by item --tag=<tag>`. There is
//     no `--tag` on `mg spend`; the working form is `--by tag:<tag>`.
//
// Nothing checked prompt prose against the tool it describes, so a wrong
// invocation shipped and every reader inherited it with no reason to doubt it.
// mg-4bb9 is the sharpest case and shows why reading cannot catch these: a
// confident false claim FORECLOSES the `--help` that would correct it. Silence
// makes a reader look; a wrong answer stops them.
//
// # The single most important constraint: NEVER RUN THE EXTRACTED COMMAND
//
// Prompt bodies contain `mg archive --days=0`, `mg reopen <id>`,
// `pogo agent stop <name>`, `gh issue close`. A control that executes its
// fixtures is a control that destroys the store it runs in. This package
// verifies that a subcommand or flag EXISTS; it never verifies that it WORKS.
//
// The only process this package ever starts is `<binary> <path…> --help`, and
// DiscoverBinary is the only function that starts one. Cobra returns
// flag.ErrHelp from Command.execute() as soon as it sees the help flag —
// before ValidateArgs and before any PersistentPreRun — so a `--help`
// invocation cannot reach a command's RunE no matter what path precedes it.
// The extracted argv is never assembled into a command line.
//
// # Flag VALUES, and why the constraint above decides their shape (mg-9324)
//
// A fourth defect shipped that the accept-surface cannot see:
// `mg list --status=closed`. `--status` is a real flag and `closed` is a
// plausible string, so the surface check passes it; `done` is the accepted
// spelling. Establishing that by invocation is exactly what the constraint
// above forbids, and the constraint is not negotiable — the corpus this reads
// contains `mg archive --days=0`, which is gate-blind and has eaten live
// carriers twice.
//
// So the value class is answered structurally or not at all, and mostly it is
// not at all. ParseFlagSpecs carries the measurement: 4 of 153 value-bearing
// flags declare a parseable enumeration, cobra's completion machinery is
// entirely unregistered in both tools, and one of the four enumerations is
// already wrong in the direction that flags correct prompts. A value is
// therefore judged only where a ValueRule names the code that does the
// refusing, every run reports the values it could NOT judge, and
// "the values are not declared anywhere machine-readable" is the honest
// headline rather than a caveat.
package promptcli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// FlagSpec is one flag as its OWN `--help` page declares it: the usage text,
// whether it takes a value, and any legal-value enumeration the usage text
// happens to spell out.
//
// Values is populated only when the usage text declares an enumeration in a
// machine-readable shape (see ParseFlagSpecs). READ THE WARNING THERE BEFORE
// TRUSTING IT: a usage string is prose the author typed, not the set the
// validator checks, and `pogo agent spawn-polecat --provider` is a live example
// of one that is stale in the direction that manufactures false positives.
// Nothing in this package treats Values as authoritative on its own; a
// ValueRule has to name it as the source.
type FlagSpec struct {
	Name  string
	Usage string
	// TakesValue is set when the declaration shows a type token
	// (`--status string`), i.e. the flag is not a bare boolean. It is what makes
	// the coverage census countable: only a value-BEARING flag can have an
	// unchecked value.
	TakesValue bool
	Values     []string
}

// Node is one command in a CLI's tree: the subcommands and the long flags its
// own `--help` names. Flags include inherited/global ones, because that is what
// the command actually accepts — `pogo refinery show --json` is legal even
// though `--json` is declared on the root.
type Node struct {
	Path  []string // e.g. ["pogo", "agent", "spawn-polecat"]
	Subs  map[string]bool
	Flags map[string]bool // long names, without the leading "--"
	// FlagSpecs holds the per-flag detail for the flags this page DECLARES,
	// keyed by long name. It is a subset of Flags: Flags deliberately also
	// collects every `--x` mentioned anywhere in the flag block, including
	// cross-references like "alias for --wide", and a mention is not a
	// declaration.
	FlagSpecs map[string]FlagSpec
	// TakesArgs is set when the Usage section shows a positional form, i.e.
	// something other than the bare `[command]` / `[flags]` skeleton.
	//
	// It exists to keep a RUNNABLE group from being read as a typo factory.
	// `pogo schedule` both dispatches to ack/list/rm and runs itself as
	// `pogo schedule <agent> --cron …`, so mayor.md's correct
	// `pogo schedule mayor --cron …` looks exactly like a misspelled
	// subcommand unless the usage line is read. Trade-off, stated: a genuine
	// typo in a subcommand of a runnable group goes unreported, because a
	// false positive on a correct instruction is the more expensive error —
	// it is the thing this package exists to stop people doing to prompts.
	TakesArgs bool
}

// Surface is one CLI's accept-surface, keyed by space-joined command path.
//
// It is a description of what the tool ACCEPTS, derived from the tool itself.
//
// It carries what each flag's own help page SAYS about its legal values, and
// nothing more (see FlagSpec.Values and ParseFlagSpecs). A value is judged only
// where a ValueRule names a source for the legal set; the surface alone never
// decides one. Establishing that a value works would mean running the command,
// and that is the one thing this package will not do — see the package comment.
type Surface struct {
	Root  string
	nodes map[string]*Node
}

// NewSurface builds an empty surface for a root binary name.
func NewSurface(root string) *Surface {
	s := &Surface{Root: root, nodes: map[string]*Node{}}
	return s
}

// Add registers a node. Path includes the root as its first element.
func (s *Surface) Add(path []string, subs, flags []string) *Node {
	n := &Node{
		Path: append([]string(nil), path...),
		Subs: map[string]bool{}, Flags: map[string]bool{},
		FlagSpecs: map[string]FlagSpec{},
	}
	for _, x := range subs {
		n.Subs[x] = true
	}
	for _, x := range flags {
		n.Flags[x] = true
	}
	s.nodes[strings.Join(path, " ")] = n
	return n
}

// Lookup returns the node at a command path, if the CLI has one.
func (s *Surface) Lookup(path []string) (*Node, bool) {
	n, ok := s.nodes[strings.Join(path, " ")]
	return n, ok
}

// Paths returns every command path in the surface, sorted. Used by reports and
// by the "is this thing denied by the prompt real?" check.
func (s *Surface) Paths() []string {
	out := make([]string, 0, len(s.nodes))
	for k := range s.nodes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NamesContaining returns the flags and subcommands of a node whose name
// contains stem, for reporting a false absence claim back with the thing the
// prompt said did not exist.
//
// Substring, not equality, and that is the point: mg-4bb9's prompt denied "an
// append/comment subcommand" — an English noun, not a flag literal. Only the
// stem "append" is recoverable from that sentence, and `--append-body` /
// `--append-body-file` are what it has to match. Callers must NOT use this for
// a flag the prompt named literally (`--body`), because `mg show --body-hash`
// would answer a denial of `--body` that is in fact true; see Checker.
func (n *Node) NamesContaining(stem string) []string {
	stem = strings.ToLower(stem)
	var out []string
	for f := range n.Flags {
		if strings.Contains(f, stem) {
			out = append(out, "--"+f)
		}
	}
	for c := range n.Subs {
		if strings.Contains(c, stem) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// helpSkip are the cobra-generated subcommands a walk does not descend into.
// They are real commands, so they stay in the surface; they just have nothing
// underneath worth a process.
var helpSkip = map[string]bool{"help": true, "completion": true}

var (
	reCommandsHeader = regexp.MustCompile(`^[A-Za-z ]*Commands:$`)
	reFlagsHeader    = regexp.MustCompile(`^(Flags|Global Flags):$`)
	reUsageHeader    = regexp.MustCompile(`^Usage:$`)
	reSubName        = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9_-]*)`)
	reLongFlag       = regexp.MustCompile(`--([a-zA-Z0-9][a-zA-Z0-9-]*)`)
	reBareCmdWord    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// ParseHelp reads one cobra `--help` page into a subcommand list, a long flag
// list, and whether the command takes positional arguments. Both "Flags:" and
// "Global Flags:" feed the flag list, since an inherited flag is one the
// command accepts.
func ParseHelp(out string) (subs, flags []string, takesArgs bool) {
	section := ""
	seenSub := map[string]bool{}
	seenFlag := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case reCommandsHeader.MatchString(t):
			section = "cmd"
			continue
		case reFlagsHeader.MatchString(t):
			section = "flag"
			continue
		case reUsageHeader.MatchString(t):
			section = "usage"
			continue
		case t == "":
			section = ""
			continue
		}
		switch section {
		case "usage":
			if usageHasPositional(t) {
				takesArgs = true
			}
		case "cmd":
			// Entries are indented; a flush-left line is prose, not a command.
			if !strings.HasPrefix(line, "  ") {
				continue
			}
			if m := reSubName.FindStringSubmatch(t); m != nil && !seenSub[m[1]] {
				seenSub[m[1]] = true
				subs = append(subs, m[1])
			}
		case "flag":
			for _, m := range reLongFlag.FindAllStringSubmatch(line, -1) {
				if !seenFlag[m[1]] {
					seenFlag[m[1]] = true
					flags = append(flags, m[1])
				}
			}
		}
	}
	sort.Strings(subs)
	sort.Strings(flags)
	return subs, flags, takesArgs
}

// ---------------------------------------------------------------------------
// Flag detail, and the enumeration question (mg-9324)
// ---------------------------------------------------------------------------

var (
	// A cobra flag-block declaration, split into shorthand / long name / type
	// token / usage text:
	//
	//	  -a, --archived          include archived items
	//	      --status string     filter by status (available, claimed, …)
	//
	// The type token is optional (booleans have none) and the usage text is
	// separated by the padding cobra inserts to align the column, which is
	// always 2+ spaces. `-h, --help   help for list` resolves correctly because
	// `help` cannot be the type token — `for` follows it after ONE space, so the
	// 2+ space requirement forces the empty-type reading.
	reFlagDecl = regexp.MustCompile(`^\s+(?:-[a-zA-Z], )?--([a-zA-Z][a-zA-Z0-9-]*)(\s+[a-zA-Z][a-zA-Z0-9]*)?\s{2,}(\S.*)$`)

	// A trailing parenthetical, stripped before the colon form is read so that
	// `priority level: low, medium, high (default: medium)` does not drag
	// `(default: medium)` into the value set.
	reTrailingParen = regexp.MustCompile(`\s*\([^()]*\)\s*$`)

	reParenGroup = regexp.MustCompile(`\(([^()]*)\)`)
	// The colon form takes the FIRST colon, not the last: `partition axis:
	// status, repo, tag, tag:<value>, …` must be read as one list containing
	// `tag:<value>` — which then fails the bare-token test and is rejected
	// whole — rather than as the tail after `tag:`.
	reColonList = regexp.MustCompile(`^[^:()]*:\s*(\S.*)$`)
	reEnumSplit = regexp.MustCompile(`\s*[,|]\s*`)
	reEnumItem  = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)
)

// enumStopWords are tokens that prove a comma list is prose rather than a value
// set. `(e.g. 12h, 2d)` and `(optional, default: medium)` both reach the split
// intact and both must be rejected: an ILLUSTRATIVE list read as an exhaustive
// one flags every legal value it forgot to mention.
var enumStopWords = map[string]bool{
	"e.g": true, "e.g.": true, "i.e": true, "i.e.": true,
	"etc": true, "etc.": true, "default": true, "optional": true, "required": true,
}

// ParseFlagSpecs reads a cobra `--help` page's flag block into per-flag detail.
//
// # The enumeration it extracts is a CLAIM, not a legal-value set
//
// mg-9324 asked whether accepted flag VALUES can be read structurally, the way
// mg-21b1 read the accept-surface. Measured over the whole of both CLIs — 341
// distinct (command, flag) pairs, 153 of them value-bearing — the answer is
// almost entirely no, and the little that IS readable cannot be trusted
// unattended:
//
//   - cobra `ValidArgs` describes POSITIONALS, not flag values, and there is
//     exactly one in either tool (`pogo completion bash|zsh|fish`).
//   - `RegisterFlagCompletionFunc` is called ZERO times in either tool, so
//     `__complete` answers every flag with ShellCompDirectiveDefault — i.e.
//     "ask the filesystem". There is nothing there to read.
//   - the enum type behind a flag is unreadable BY CONSTRUCTION for `mg`, which
//     this package reaches only as a binary. That is not an oversight: the
//     binary is what pins the check to the revision under test.
//   - which leaves the usage string, and exactly FOUR flags in either tool
//     spell out a parseable enumeration.
//
// Of those four, ONE IS ALREADY WRONG in the direction that matters.
// `pogo agent spawn-polecat --provider` says `(claude, codex, pi)`;
// providers.Resolve accepts a fourth, `cursor`, and returns ok for it. A checker
// that believed the usage string would report a correct `--provider=cursor` as a
// defect — the exact failure this package exists to prevent, at a 1-in-4 rate on
// its own covered set. And a `--help` page gives no way to tell that stale
// hand-written list from `mg list --status`, whose usage string macguffin
// GENERATES from the same `listStatusValues` slice its validator rejects
// against, and which therefore cannot drift.
//
// So Values is offered as evidence and never as a verdict. A ValueRule has to
// name it as the source, and say why the naming is sound. See ValueRule.
func ParseFlagSpecs(out string) map[string]FlagSpec {
	specs := map[string]FlagSpec{}
	var order []string
	section, last := "", ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case reFlagsHeader.MatchString(t):
			section, last = "flag", ""
			continue
		case reCommandsHeader.MatchString(t), reUsageHeader.MatchString(t):
			section, last = "", ""
			continue
		case t == "":
			section, last = "", ""
			continue
		}
		if section != "flag" {
			continue
		}
		m := reFlagDecl.FindStringSubmatch(line)
		if m == nil {
			// A usage string long enough for cobra to wrap continues onto the
			// next line. Fold it in, or the enumeration at its tail is lost.
			if last != "" {
				sp := specs[last]
				sp.Usage = strings.TrimSpace(sp.Usage + " " + t)
				specs[last] = sp
			}
			continue
		}
		name := m[1]
		if _, dup := specs[name]; !dup {
			order = append(order, name)
		}
		specs[name] = FlagSpec{
			Name:       name,
			Usage:      strings.TrimSpace(m[3]),
			TakesValue: strings.TrimSpace(m[2]) != "",
		}
		last = name
	}
	for _, name := range order {
		sp := specs[name]
		sp.Values = declaredValues(sp.Usage)
		specs[name] = sp
	}
	return specs
}

// declaredValues pulls a legal-value enumeration out of one flag's usage text,
// in the two shapes the corpus's tools actually use:
//
//	filter by status (available, claimed, pending, done, shelved, archived)
//	priority level: low, medium, high
//
// It is deliberately strict, and errs toward finding nothing. Every item must be
// a single bare lowercase token and there must be at least two, which is what
// rejects `(comma-separated or repeated)`, `(substring match)`,
// `(overrides $MG_ROOT; default ~/.macguffin)`, `("-" for stdin)` and the rest
// of the 150 parentheticals in these two help trees that are prose. Returning
// nothing costs a value that goes unchecked and is reported as such; returning a
// wrong set costs a false finding against a correct prompt, and those are not
// the same price.
func declaredValues(usage string) []string {
	for _, m := range reParenGroup.FindAllStringSubmatch(usage, -1) {
		if v := enumItems(m[1]); v != nil {
			return v
		}
	}
	stripped := usage
	for {
		next := reTrailingParen.ReplaceAllString(stripped, "")
		if next == stripped {
			break
		}
		stripped = next
	}
	if m := reColonList.FindStringSubmatch(stripped); m != nil {
		if v := enumItems(m[1]); v != nil {
			return v
		}
	}
	return nil
}

// enumItems accepts a comma- or pipe-separated list of at least two bare tokens,
// and rejects everything else.
func enumItems(s string) []string {
	parts := reEnumSplit.Split(strings.TrimSpace(s), -1)
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !reEnumItem.MatchString(p) || enumStopWords[p] {
			return nil
		}
		out = append(out, p)
	}
	return out
}

// usageHasPositional reads one cobra usage line — `pogo schedule <agent>
// [flags]` — and reports whether it shows a positional slot. The command path
// is bare lowercase words; anything else that is not cobra's `[flags]` or
// `[command]` skeleton is an argument the command accepts.
func usageHasPositional(line string) bool {
	for _, tok := range strings.Fields(line) {
		switch tok {
		case "[flags]", "[command]":
			continue
		}
		if reBareCmdWord.MatchString(tok) {
			continue
		}
		return true
	}
	return false
}

// DiscoverBinary walks a cobra CLI's command tree by asking each node for its
// own `--help`, and returns the surface it describes.
//
// bin is the executable to run (a path or a PATH name); root is the name the
// prompts spell it with, which is not always the same string — the pogo binary
// under test is a freshly compiled temp file, but prompts say `pogo`.
//
// USE A BINARY BUILT FROM THE REVISION UNDER TEST, not the one on PATH. That is
// not a stylistic preference: while writing this package the installed `pogo`
// had no `check-intake`, the source three commits back did, and a check run
// against PATH would have reported mayor.md's correct `pogo check-intake`
// instruction as a defect. Source and runtime disagree routinely here — it is
// the same trap as reasoning about a merged control the running daemon does not
// carry.
//
// The ONLY argv this ever builds is `bin <path…> --help`. See the package
// comment for why that is inert, and why nothing else may be added.
func DiscoverBinary(ctx context.Context, bin, root string) (*Surface, error) {
	s := NewSurface(root)
	var walk func(path []string) error
	walk = func(path []string) error {
		args := append(append([]string{}, path...), "--help")
		cmd := exec.CommandContext(ctx, bin, args...)
		out, err := cmd.CombinedOutput()
		// A cobra help page exits 0, but be lenient: some roots print help and
		// exit non-zero. Only a page with no recognizable sections is fatal.
		subs, flags, takesArgs := ParseHelp(string(out))
		if len(subs) == 0 && len(flags) == 0 && err != nil {
			return fmt.Errorf("%s %s --help: %w", bin, strings.Join(path, " "), err)
		}
		node := s.Add(append([]string{root}, path...), subs, flags)
		node.TakesArgs = takesArgs
		node.FlagSpecs = ParseFlagSpecs(string(out))
		for _, sub := range subs {
			if helpSkip[sub] {
				s.Add(append(append([]string{root}, path...), sub), nil, nil)
				continue
			}
			if err := walk(append(append([]string{}, path...), sub)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nil); err != nil {
		return nil, err
	}
	return s, nil
}
