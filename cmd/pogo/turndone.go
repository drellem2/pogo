package main

// `pogo turn-done`: the writer half of the fleet's turn-completion artifact
// (mg-a270).
//
// THIS COMMAND IS RUN BY THE AGENT, INSIDE ITS OWN TURN. That is not a usage
// note, it is the entire value of the artifact. Every signal that read green
// through the 22-hour outage of 2026-08-10/11 was written by pogod about
// pogod — process present, schedule registered, 140 nudges delivered, revision
// current — and every one of them was truthful. A line that pogod appended on
// an agent's behalf would join that set exactly, and the check reading it would
// inherit the defect it was built to remove. cmd/pogod is held to this
// mechanically by TestPogodNeverWritesTheTurnLog, not by this comment.
//
// It touches no daemon. The moment this artifact matters most is the moment
// pogod is unreachable, wedged, or reporting a fleet that is not working — so
// the writer is a file append and nothing else, and it keeps working when
// everything that would have reported on it has stopped.

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/turnlog"
)

func newTurnDoneCmd(jsonOutput *bool) *cobra.Command {
	var (
		agentName string
		note      string
	)
	cmd := &cobra.Command{
		Use:   "turn-done",
		Short: "Record that you finished a turn (one append-only line; run it yourself, at the end of a turn)",
		Long: `Append one timestamped line recording that YOU completed a turn.

  ` + turnlog.Dir() + `/<agent>.log
  <RFC3339 UTC timestamp> <agent-name> <note>

WHY IT EXISTS. On 2026-08-10/11 this fleet was inert for twenty-two hours while
every signal at the daemon end read green: the processes existed, the schedules were
registered, 140 nudges were delivered, the running revision was current. All of
it was TRUE, and all of it measured what the daemon did. The outage was
diagnosed from the only two files on the machine that a stopped agent cannot
produce — two heartbeat logs that existed by accident, on the two agents that
needed them least. Three of the five running crew agents wrote nothing of the
kind, and one of those three was the coordinator every other detector routes
through.

This line is that file, for every agent, on purpose. It is the only artifact
here that nothing except a completed turn can create, which is what makes its
absence mean something.

RUN IT YOURSELF, AT THE END OF A TURN. Not in advance, not on a timer, not from
a background script, and never for another agent. The instant something other
than a finished turn can produce the line, it is worth what ` + "`nudge_delivered`" + `
was worth during those twenty-two hours. pogod does not call this command and
a test in internal/turnlog enforces that over the whole tree.

The agent name defaults to $POGO_AGENT_NAME, which pogod sets in every agent's
environment. Outside an agent session there is no name to default to and this
refuses rather than guessing: a line under the wrong name is not a missing
signal, it is a false one.

Read the other side with ` + "`pogo check-turns`" + `.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("turn-done takes no positional arguments (got %q); put the description in --note", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentName == "" {
				agentName = os.Getenv("POGO_AGENT_NAME")
			}
			if agentName == "" {
				return fmt.Errorf("no agent name: $POGO_AGENT_NAME is unset and --agent was not given.\n" +
					"Every agent pogod spawns has POGO_AGENT_NAME in its environment, so an empty one\n" +
					"means this is not running inside an agent turn — and a turn-completion line for a\n" +
					"turn that did not happen is worse than none")
			}
			now := time.Now().UTC()
			if err := turnlog.Append(agentName, note, now); err != nil {
				return err
			}
			if *jsonOutput {
				cli.PrintJSON(map[string]any{
					"agent": agentName,
					"at":    turnlog.Stamp(now),
					"note":  note,
					"path":  turnlog.Path(agentName),
				})
				return nil
			}
			fmt.Printf("turn recorded: %s\n", turnlog.Path(agentName))
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "Agent name (default: $POGO_AGENT_NAME)")
	cmd.Flags().StringVar(&note, "note", "", "A few words on what this turn finished")
	return cmd
}
