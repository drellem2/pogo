package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/memcheck"
	"github.com/drellem2/pogo/internal/providers"
)

// exitMemdirsUsage is `pogo check-memdirs`'s exit code for a malformed
// invocation, matching the rest of the check-* family (0 clean, 1 finding, 2
// usage, 3 measured nothing).
const exitMemdirsUsage = 2

// memdirsReport is the JSON shape and the render source. It carries Measured
// explicitly rather than letting an empty Stranded list stand in for health,
// because those two states are the whole point of this check existing.
type memdirsReport struct {
	Measured   bool             `json:"measured"`
	Candidates int              `json:"candidates"`
	Stores     int              `json:"stores"`
	Stranded   []memdirsFinding `json:"stranded"`
}

type memdirsFinding struct {
	Agent     string `json:"agent"`
	WorkDir   string `json:"work_dir"`
	StoreDir  string `json:"store_dir"`
	NoteCount int    `json:"note_count"`
	// Newest is UTC with an explicit Z: two surfaces rendering from
	// differently-deserialized values would otherwise disagree by the host's offset.
	Newest string   `json:"newest_note,omitempty"`
	Sample []string `json:"sample,omitempty"`
}

// newCheckMemdirsCmd builds `pogo check-memdirs` (mg-a9b3): the sweep for agent
// memory stores that hold notes nothing loads.
//
// It joins the check-* family — check-acks, check-commit-body, check-intake,
// check-mailloops, check-orphans, check-prompts, check-staleness, check-stranded,
// check-strandedmail, check-teardown, check-verdicts — whose membership
// criterion is A READ-ONLY DETECTOR THAT REPORTS A CONDITION AND TAKES NO
// ACTION. This one reports; the remedy is a triage, and a triage is not
// something a sweep can do.
func newCheckMemdirsCmd(jsonOutput *bool) *cobra.Command {
	var agentRoot string
	cmd := &cobra.Command{
		Use:   "check-memdirs",
		Short: "Report per-agent memory stores holding notes nothing loads (never moves, never deletes)",
		Long: `Report every harness auto-memory store keyed on a pogo-owned agent directory
that still holds notes.

WHAT THIS FINDS THAT THE OTHER MEMORY CHECKS CANNOT. ` + "`pogo doctor`" + ` already judges
every memory index on the machine three ways: over the load cap, holding notes
the index does not name, and carrying a hook whose item has moved on. All three
are properties of a store some session is still using. This one is the property
of a store no session uses any more — and that is the one no session can report,
because the agents that used to write there have healthy recall against a
different store and indexes at exact parity. Nothing is broken anywhere an
instrument is pointed.

HOW A STORE STOPS HAVING A READER. The store is keyed on the session's PROJECT,
and for a directory inside a git repo the project is the repo, not the directory.
So making a parent directory a git repo re-keys every agent underneath it onto
one shared store. That is usually an improvement — it is how this fleet's crew
came to share one corpus — and the notes already written in the old per-agent
stores do not follow. On this box ` + "`~/.pogo`" + ` became a repo on 2026-07-07 and 153
notes across five stores went unreachable. It took five weeks and a duplicated
investigation to notice: one stranded note had recorded a finding that two agents
later re-derived from scratch and filed as new work.

Nothing was misconfigured and no write ever failed. Every file was on disk and
readable the whole time. They simply stopped participating in recall.

WHY THIS IS NOT AN AGE CHECK. "A store nothing has written to in N days" fires on
every legitimately dormant per-repo store on the machine, and still cannot see a
store stranded five minutes ago. The signal is not staleness — it is that the
store is keyed to a directory POGO ITSELF OWNS. pogo creates the agent working
directory and runs the agent in it, so a store hanging off that directory has
exactly one possible reader by construction, while a shared store has many. Once
the fleet's memories live in one shared store, a populated per-agent store is a
store with no reader whatever its mtime says.

AN EMPTY STORE IS NOT A FINDING. A retired store is deliberately left holding its
index and no notes, so the tombstone explaining the retirement survives for the
next reader. Deleting the directory would only mean the next session on that
project root re-creates it silently, which is the failure this replaces.

THE PATH IS CONSTRUCTED, NEVER DISCOVERED, and that decides the exit codes. Going
from an agent's working directory to its store needs the harness's own path
encoding, which pogo does not own — so it lives in the provider, and a wrong
model produces a path that does not exist rather than a wrong finding. That is
the safe failure direction, but it means a silently-changed encoding would report
zero findings forever. So a run that probed nothing exits ` + fmt.Sprint(exitInstrumentFailure) + ` and says so, rather
than printing an all-clear it has not earned.

Exit status: 0 nothing stranded, 1 at least one store holds unreadable notes,
` + fmt.Sprint(exitMemdirsUsage) + ` usage error, ` + fmt.Sprint(exitInstrumentFailure) + ` this run measured nothing.`,
		// A malformed invocation exits 2, never 1 and never 0. Cobra's default
		// would fold both a bad flag and a stray argument into the generic error
		// exit, which a schedule cannot tell from a real finding — and 0 would be
		// worse still, reporting a detector that never ran as green.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				fmt.Fprintf(os.Stderr, "check-memdirs takes no positional arguments (got %q)\n", args[0])
				os.Exit(exitMemdirsUsage)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			root := agentRoot
			if root == "" {
				root = agent.PromptDir()
			}
			home, err := os.UserHomeDir()
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("cannot resolve home directory: %v", err), exitInstrumentFailure)
			}

			sv, err := memcheck.SurveyPerAgentStores(home, root, providers.AgentMemoryStoreIndexes)
			if err != nil {
				cli.ExitWithError(*jsonOutput, fmt.Sprintf("cannot read agent root %s: %v", root, err), exitInstrumentFailure)
			}

			rep := memdirsReport{
				Measured:   sv.Measured(),
				Candidates: sv.Candidates,
				Stores:     len(sv.Stores),
			}
			for _, st := range sv.Stranded() {
				f := memdirsFinding{
					Agent:     st.Agent,
					WorkDir:   st.WorkDir,
					StoreDir:  st.Dir,
					NoteCount: len(st.Notes),
					Sample:    sampleNotes(st.Notes),
				}
				if !st.Newest.IsZero() {
					f.Newest = st.Newest.UTC().Format("2006-01-02T15:04Z")
				}
				rep.Stranded = append(rep.Stranded, f)
			}
			sort.SliceStable(rep.Stranded, func(i, j int) bool {
				return rep.Stranded[i].NoteCount > rep.Stranded[j].NoteCount
			})

			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(renderMemdirs(rep, root))
			}

			if !rep.Measured {
				os.Exit(exitInstrumentFailure)
			}
			if len(rep.Stranded) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&agentRoot, "agent-root", "",
		"agent working-directory root to sweep (default $POGO_HOME/agents)")

	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintf(os.Stderr, "check-memdirs: %v\n\n%s", err, c.UsageString())
		os.Exit(exitMemdirsUsage)
		return nil
	})
	return cmd
}

// sampleNotes returns at most three note names, so the report names something
// concrete without printing a corpus. A count alone reads as a metric; a name is
// what makes a reader open the directory.
func sampleNotes(notes []string) []string {
	const max = 3
	if len(notes) <= max {
		return notes
	}
	return notes[:max]
}

func renderMemdirs(rep memdirsReport, root string) string {
	var b strings.Builder

	if !rep.Measured {
		fmt.Fprintf(&b, "check-memdirs: MEASURED NOTHING — no store path was probed under %s.\n\n", root)
		b.WriteString("This is not a clean result and must not be read as one. Either that root holds\n")
		b.WriteString("no agent directories, or no configured harness names a per-agent memory store\n")
		b.WriteString("for them. If agents ARE running here, the harness's path encoding has probably\n")
		b.WriteString("changed and this check has gone blind — see agent.Provider.AgentMemoryStoreIndex.\n")
		return b.String()
	}

	if len(rep.Stranded) == 0 {
		fmt.Fprintf(&b, "check-memdirs: clean — %d store path(s) probed, %d store(s) found, none holding notes.\n",
			rep.Candidates, rep.Stores)
		b.WriteString("A store holding only its index is the retired shape and is reported clean by design.\n")
		return b.String()
	}

	total := 0
	for _, f := range rep.Stranded {
		total += f.NoteCount
	}
	fmt.Fprintf(&b, "check-memdirs: %d note(s) in %d per-agent store(s) that nothing loads.\n\n",
		total, len(rep.Stranded))

	for _, f := range rep.Stranded {
		fmt.Fprintf(&b, "  %-16s %3d note(s)", f.Agent, f.NoteCount)
		if f.Newest != "" {
			fmt.Fprintf(&b, "   newest %s", f.Newest)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %-16s %s\n", "", f.StoreDir)
		if len(f.Sample) > 0 {
			fmt.Fprintf(&b, "  %-16s e.g. %s\n", "", strings.Join(f.Sample, ", "))
		}
		b.WriteString("\n")
	}

	b.WriteString("These notes are readable and are participating in nobody's recall. The remedy is a\n")
	b.WriteString("TRIAGE, not a copy: some of what is here has been superseded, and moving it into a\n")
	b.WriteString("loaded store wholesale would deliver refuted guidance reading as current. Decide per\n")
	b.WriteString("note whether it still holds, then leave the store holding only a tombstone index so\n")
	b.WriteString("the next reader finds the decision instead of re-deriving it.\n")
	return b.String()
}
