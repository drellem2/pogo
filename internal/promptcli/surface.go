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
package promptcli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Node is one command in a CLI's tree: the subcommands and the long flags its
// own `--help` names. Flags include inherited/global ones, because that is what
// the command actually accepts — `pogo refinery show --json` is legal even
// though `--json` is declared on the root.
type Node struct {
	Path  []string // e.g. ["pogo", "agent", "spawn-polecat"]
	Subs  map[string]bool
	Flags map[string]bool // long names, without the leading "--"
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
// It deliberately carries no knowledge of what any flag VALUE may be: checking
// that `--status=closed` is a legal status would mean running the command, and
// that is the one thing this package will not do. See the package comment.
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
	n := &Node{Path: append([]string(nil), path...), Subs: map[string]bool{}, Flags: map[string]bool{}}
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
		s.Add(append([]string{root}, path...), subs, flags).TakesArgs = takesArgs
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
