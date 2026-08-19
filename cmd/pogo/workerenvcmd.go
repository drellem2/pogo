package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// newAgentEnvCmd builds `pogo agent env`.
//
// It exists because "what environment does a polecat get?" had no answer you
// could ASK for. The answer was narrated — in a help paragraph, in a doc
// comment, in six prompt templates — and mg-fbaf recorded what that costs: on
// one night POGO_WORKER_CORES was believed not to exist, and then, by a second
// reader who had just established that it does, believed to have no consumer.
// The second reader was quoting the line naming the consumer while asserting
// its opposite, and a third reader who knew the area did not catch it either
// until they grepped.
//
// So the deliverable is the grep, promoted to a command. Two properties are
// load-bearing and both are cheap:
//
//   - The variable list is the SAME list the spawn path injects from
//     (internal/agent/workerenv.go), not a copy of it. A variable cannot be
//     injected without appearing here.
//   - "read by" is COMPUTED from the prompt corpus embedded in this binary. A
//     stated consumer list would go stale on the first template edit and would
//     then be this ticket's defect wearing this ticket's fix as a label.
//
// The command's own bounds are printed with its output rather than left for a
// reader to infer, because an enumeration silent about its edges is how "I
// searched one place" became "the mechanism is absent" — which happened inside
// mg-fbaf's own audit of mg-fbaf.
type agentEnvJSON struct {
	Vars         []agentEnvVarJSON  `json:"vars"`
	WorkerBudget agent.WorkerBudget `json:"worker_budget"`
	BudgetSource string             `json:"budget_source"`
	NotCovered   []string           `json:"not_covered"`
}

type agentEnvVarJSON struct {
	agent.InjectedEnv
	// ReadBy is the shipped prompt files that reference $NAME, computed from
	// this binary's embedded corpus. An empty list means no SHIPPED PROMPT
	// mentions it — it does not mean nothing consults it.
	ReadBy []string `json:"read_by"`
}

func newAgentEnvCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "List the environment variables pogod injects into a worker, and which prompts read each",
		Long: `List every environment variable pogod injects into a polecat it spawns, in
injection order, with what each one is for and which shipped prompts read it.

The variable list is the same list the spawn path injects from, so a variable
cannot be injected without being reported here. The 'read by' column is
computed by scanning the prompt corpus embedded in this binary — it is a search
result, not a claim, and it goes stale only if the binary does.

What is NOT covered: a worker also inherits pogod's entire environment, and a
dispatcher may add anything it likes with 'spawn-polecat --env K=V'. Those are
open sets and this list does not close them. 'read by' covers shipped prompts
only — a consumer in Go code, in a user-edited prompt under ~/.pogo/agents, or
in another repository is outside the search, and an empty result there is not
evidence of absence.

The two core-budget VALUES come from the running daemon (the same figure
'pogo host load' reports), so this command and the spawn path cannot disagree
about them. With no daemon reachable the names, purposes and consumers still
answer and the values are reported as unknown.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			budget, source := workerBudgetFromDaemon()
			vars := agent.PolecatEnv(budget)

			if *jsonOutput {
				out := agentEnvJSON{
					WorkerBudget: budget,
					BudgetSource: source,
					NotCovered:   agentEnvNotCovered,
				}
				for _, v := range vars {
					out.Vars = append(out.Vars, agentEnvVarJSON{
						InjectedEnv: v,
						ReadBy:      agent.PromptConsumers(v.Name),
					})
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			printAgentEnv(os.Stdout, vars, budget, source)
			return nil
		},
	}
}

// agentEnvNotCovered is what this list cannot see. Carried as data so --json
// readers get the bounds too: a machine consumer that took the array as
// complete would make exactly the mistake this command exists to stop.
var agentEnvNotCovered = []string{
	"the worker inherits pogod's entire environment (os.Environ()) on top of these",
	"a dispatcher may add anything with `pogo agent spawn-polecat --env KEY=VALUE`",
	"`read by` searches the SHIPPED prompt corpus only — not Go code, not user-edited prompts under ~/.pogo/agents, not other repositories",
}

// workerBudgetFromDaemon asks the running pogod what it would hand the next
// worker, and reports which source answered. The daemon is asked rather than
// the division recomputed here for the reason WouldRefuseDispatch is: a second
// copy of the policy can disagree with the one doing the injecting, and a
// reader has no way to tell which they are looking at.
func workerBudgetFromDaemon() (agent.WorkerBudget, string) {
	resp, err := client.GetHostLoad("")
	if err != nil {
		return agent.WorkerBudget{}, "unreachable: " + err.Error()
	}
	return resp.WorkerBudget, "the running daemon (GET /agents/hostload)"
}

func printAgentEnv(w io.Writer, vars []agent.InjectedEnv, budget agent.WorkerBudget, source string) {
	fmt.Fprintln(w, "Environment pogod injects into a polecat")
	fmt.Fprintln(w, "========================================")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "In injection order. A later assignment of the same name wins, which is why a")
	fmt.Fprintln(w, "dispatcher's --env beats the core budget and nothing beats POGO_ROLE.")
	fmt.Fprintln(w)

	for _, v := range vars {
		value := v.Value
		if !v.Set {
			value = v.Placeholder
		}
		fmt.Fprintf(w, "  %s=%s\n", v.Name, value)
		fmt.Fprintf(w, "      is:       %s\n", v.Why)
		if v.When != "" {
			fmt.Fprintf(w, "      set when: %s\n", v.When)
		}
		// The caveat prints on its OWN line, after the purpose, never joined to
		// it by an "and". mg-fbaf's sharpest instance was one sentence carrying
		// both, with the caveat doing the emphatic work and the existence
		// riding along as a clause that two readers dropped.
		if v.Note != "" {
			fmt.Fprintf(w, "      but:      %s\n", v.Note)
		}
		fmt.Fprintf(w, "      read by:  %s\n", describeConsumers(agent.PromptConsumers(v.Name)))
		fmt.Fprintln(w)
	}

	if budget.Known() {
		fmt.Fprintf(w, "Core budget values from %s.\n", source)
	} else {
		fmt.Fprintf(w, "Core budget values UNKNOWN — %s.\n", source)
		fmt.Fprintln(w, "The names, purposes and consumers above are unaffected; only the two numbers are.")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "What this list does NOT cover:")
	for _, b := range agentEnvNotCovered {
		fmt.Fprintf(w, "  - %s\n", b)
	}
}

// describeConsumers renders a computed consumer list, and says plainly what an
// empty one means. "nothing" is the word that cost mg-fbaf its fourth instance,
// so it is not the word used here.
func describeConsumers(files []string) string {
	if len(files) == 0 {
		return "no shipped prompt mentions it (this searched shipped prompts only)"
	}
	const show = 2
	if len(files) <= show {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s, +%d more shipped prompts", strings.Join(files[:show], ", "), len(files)-show)
}
