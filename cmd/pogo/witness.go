package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
)

// `pogo agent witness` — read the on-disk polecat witness WITHOUT pogod.
//
// WHY THIS COMMAND EXISTS (mg-65b2). The redeploy drain must answer "are there
// live polecats?" at the one moment it cannot ask pogod: when pogod has stopped
// answering. mg-13a3 already built the answer — the witness store outlives the
// process that wrote it, and that is the entire reason it can answer a question
// the in-memory registry cannot — but the only routes to it were inside pogod
// (registryLiveness, orphan.go), i.e. inside the process that is down. This is
// the same evidence, reachable from outside.
//
// WHY THE DRAIN SHELLS OUT HERE INSTEAD OF PARSING THE FILE ITSELF. The driver
// (scripts/pogo-self-deploy) is curl-and-sed by design and could read the JSON
// unaided. It must not. The liveness test is not "is the pid in the file" — it
// is `kill(pid,0)` AND a `ps -o lstart=` identity match, because a bare pid is a
// false witness (pids are recycled). witnessVerdict's own doc names this caller
// and the hazard: "if they ever disagreed about what 'our process is alive'
// means, the drain and the reaper would be reasoning about different fleets.
// The verdict has exactly one definition." A shell re-implementation would be a
// second definition of the word, drifting silently, which is the mistake the
// ruling on mg-65b2 called out one layer up (drain_wait growing its own private
// notion of "down" beside classify_drain_precondition's). One definition, one
// probe, reached over a process boundary.
//
// EXIT CODES ARE THE INTERFACE, AND THEY KEEP THREE STATES APART:
//
//	0 (ExitSuccess)  — the witness is present and readable. alive_count is a
//	                   MEASUREMENT and the caller may act on it, including 0.
//	2 (ExitNotFound) — no witness file. An ABSENCE, not a zero.
//	1 (ExitError)    — a witness exists and could not be read or parsed.
//
// A caller must not collapse 1 and 2 into "no polecats". That is the defect
// this command was built to let the drain avoid: "I could not look" rendered as
// "there are none". They are distinct here so the operator's message can be
// accurate about which of them happened — the two need different fixes (install
// a newer pogod vs. repair a corrupt file).
//
// The JSON is COMPACT (json.Marshal, not cli.PrintJSON's MarshalIndent) on
// purpose: the driver parses it with the single-line seds documented in its
// header, which do not tolerate the space MarshalIndent puts after each colon.
type witnessCLIEntry struct {
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	WorkItemID string `json:"work_item_id,omitempty"`
}

type witnessCLIReport struct {
	WitnessPath    string            `json:"witness_path"`
	WitnessPresent bool              `json:"witness_present"`
	AliveCount     int               `json:"alive_count"`
	Alive          []witnessCLIEntry `json:"alive"`
}

// printCompactJSON writes v as single-line JSON. See the note above on why this
// does not go through cli.PrintJSON.
func printCompactJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error":"failed to marshal JSON: %s"}`+"\n", err)
		os.Exit(cli.ExitError)
	}
	fmt.Println(string(data))
}

// witnessErrorExit reports an unreadable/absent witness and exits with code.
//
// One channel per mode, mirroring cli.ExitWithError: the reason is a structured
// `error` field in JSON mode and a stderr line otherwise. Emitting both would
// double every message in the driver's log, which captures the command's output
// and re-prints it — the reason has to be legible exactly once.
func witnessErrorExit(jsonOut bool, msg string, code int) {
	if jsonOut {
		printCompactJSON(map[string]interface{}{"error": msg})
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(code)
}

// runAgentWitness implements `pogo agent witness`.
func runAgentWitness(jsonOut bool) {
	present, err := agent.WitnessStoreExists()
	if err != nil {
		witnessErrorExit(jsonOut, err.Error(), cli.ExitError)
	}
	if !present {
		witnessErrorExit(jsonOut,
			fmt.Sprintf("no polecat witness at %s — this is an ABSENCE, not a report that no polecats are running: "+
				"an idle fleet leaves a present-and-empty file, so a missing one means nothing has ever written here "+
				"(a pogod predating mg-13a3, or a different POGO_HOME)", agent.WitnessPath()),
			cli.ExitNotFound)
	}

	alive, err := agent.WitnessedAlivePolecats()
	if err != nil {
		witnessErrorExit(jsonOut, err.Error(), cli.ExitError)
	}

	report := witnessCLIReport{
		WitnessPath:    agent.WitnessPath(),
		WitnessPresent: true,
		AliveCount:     len(alive),
		Alive:          []witnessCLIEntry{},
	}
	for _, r := range alive {
		report.Alive = append(report.Alive, witnessCLIEntry{Name: r.Name, PID: r.PID, WorkItemID: r.WorkItemID})
	}

	if jsonOut {
		printCompactJSON(report)
		return
	}
	if report.AliveCount == 0 {
		fmt.Printf("No witnessed polecat is alive (%s)\n", report.WitnessPath)
		return
	}
	for _, e := range report.Alive {
		if e.WorkItemID != "" {
			fmt.Printf("%s (pid=%d, work_item=%s)\n", e.Name, e.PID, e.WorkItemID)
		} else {
			fmt.Printf("%s (pid=%d)\n", e.Name, e.PID)
		}
	}
}
