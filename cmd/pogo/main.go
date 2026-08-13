////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/ackwatch"
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/auditwatch"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/closingref"
	"github.com/drellem2/pogo/internal/completion"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
	"github.com/drellem2/pogo/internal/ghtoken"
	"github.com/drellem2/pogo/internal/gitceiling"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/homevcs"
	"github.com/drellem2/pogo/internal/memcheck"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/reconcile"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/revcheck"
	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/selfdrift"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/sourcewatch"
	"github.com/drellem2/pogo/internal/synthfail"
	"github.com/drellem2/pogo/internal/version"
	"github.com/drellem2/pogo/internal/wedgewatch"
	"github.com/drellem2/pogo/internal/xref"
)

func showPromptFile(path string, jsonOut bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		cli.ExitWithError(jsonOut, err.Error(), cli.ExitError)
	}
	if jsonOut {
		cli.PrintJSON(map[string]string{
			"path":    path,
			"content": string(data),
		})
	} else {
		fmt.Print(string(data))
	}
}

// showRawPromptFile resolves a prompt name (coordinator → crew → template)
// and emits the source file verbatim. Used by `pogo agent prompt show --raw`
// to preserve the pre-synthesis behavior for users who want to inspect the
// shipped/customized file as-is.
func showRawPromptFile(name string, jsonOut bool) {
	if name == agent.CoordinatorName() {
		path, err := agent.ResolveMayorPrompt()
		if err != nil {
			cli.ExitWithError(jsonOut, err.Error(), cli.ExitError)
		}
		showPromptFile(path, jsonOut)
		return
	}
	if path, err := agent.ResolveCrewPrompt(name); err == nil {
		showPromptFile(path, jsonOut)
		return
	}
	if path, err := agent.ResolveTemplate(name); err == nil {
		showPromptFile(path, jsonOut)
		return
	}
	cli.ExitWithError(jsonOut, fmt.Sprintf("prompt %q not found (checked %s, crew/, templates/)", name, agent.CoordinatorName()), cli.ExitError)
}

// optedOutNote renders the parity opt-out count as a clause, or "" when there is
// nothing to say.
//
// It is always shown next to a parity result, pass or warn, because the opt-out
// is the reason this check is shippable at all: deliberate non-indexing is a
// CORRECT action that produces a parity "defect", and a reader who cannot see
// how many notes claimed the opt-out cannot tell a clean store from a store that
// silenced itself (mg-cb71).
func optedOutNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d deliberately unindexed, not counted)", n)
}

// exitInstrumentFailure is `pogo check-teardown`'s exit code for a run that
// reached no verdict for any carrier it scanned (mg-dd22).
//
// It is distinct from cli.ExitError on purpose. "The detector found something"
// and "the detector could not see" demand opposite responses — chase the
// finding, versus fix the network and re-run — and a schedule that gets the
// same integer for both will treat a blind run as a result, which is the exact
// failure this code exists to make impossible.
const exitInstrumentFailure = 3

func main() {

	// Bound every git repository lookup at POGO_HOME before any subcommand runs.
	// The CLI is in the same class as the daemon: `pogo gc` prunes polecat
	// worktrees and the refinery drives merges, both by shelling out to git
	// against repos nested inside ~/.pogo. A lookup aimed at one that has lost
	// its .git walks up and silently succeeds on the fleet's config repo, so gc
	// would prune against the wrong toplevel (mg-ca7d).
	//
	// Inert outside ~/.pogo: a ceiling that is not an ancestor of the working
	// directory does not affect the walk, so this does not touch an operator
	// running `pogo` against their own repos.
	if err := gitceiling.Ensure(); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot bound git repository lookups at %s: %v\n", config.PogoHome(), err)
		os.Exit(1)
	}

	// Resolve the coordinator agent's name ([agents] coordinator) and the worker
	// role's display name ([agents] worker) before any prompt resolution or
	// synthesis happens client-side (prompt show/list run in this process, not
	// in pogod). The worker name is display-only — it feeds prompt prose, never
	// an identifier.
	//
	// On an existing install whose config.toml predates the role keys, these
	// names come from the live Default* consts until the migration guard pins
	// the frozen legacy ones. `pogo install` runs that guard and re-resolves
	// before it synthesizes prompts; see pinAndResolveRoles (mg-bc47).
	resolveRoles()

	var jsonOutput bool

	var cmdVisit = &cobra.Command{
		Use:   "visit [file]",
		Short: "Visit file or directory",
		Long: `Checks if the file is contained in a repository, and if
so indexes the repository.`,
		Args: cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				cli.ExitWithError(jsonOutput, "visit requires a file argument", cli.ExitError)
			}
			resp, err := client.Visit(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if resp == nil {
				cli.ExitWithError(jsonOutput, "not found", cli.ExitNotFound)
			}
			if jsonOutput {
				cli.PrintJSON(resp)
			} else {
				fmt.Println(resp.ParentProject.Path)
			}
		},
	}

	var cmdServer = &cobra.Command{
		Use:   "server",
		Short: "Control the pogo server",
		Long: `server provides commands to control the pogo daemon.
Child commands include start, stop, and status.`,
	}
	var cmdServerStart = &cobra.Command{
		Use:   "start",
		Short: "Start the pogo server and the crew it declares",
		Long: `Start the pogo server, and leave the declared crew running.

Whatever state the daemon is found in, this command ends by running the crew
auto-start sweep — every agent whose prompt frontmatter declares auto_start =
true, the mayor included — and reporting what it found, by name.

That is the point of the command rather than a side effect. "Running" and
"crewed" are different facts: a daemon whose mayor crashed still binds its port,
still answers /health, and still fires schedules, while nothing is dispatched
and work piles up silently. Before this, a start against such a daemon printed
"the server is already running" and did nothing, so the obvious recovery action
was a no-op.

The three states it handles:

  not running    spawn pogod, then confirm the crew came up with it
  index-only     restart orchestration (refinery, crash-respawn) and the crew
  full mode      leave the mode alone and sweep the crew

The sweep is idempotent: an agent that is already up is reported as such, not
restarted. Polecats are ephemeral and are never restored; they are re-dispatched
per work item.

No crew is started on a daemon with no config file, or one running with
[agents] autostart = false; the output says which, rather than reporting an
empty success. Exits non-zero if any crew agent errored while starting.`,
		Args: cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			// "status" is what the daemon was doing before this command, and it
			// is kept distinct from what the command then did about the crew:
			// a caller parsing --json needs to tell a cold start from a start
			// against a healthy daemon, and both now sweep.
			status := "running"
			if err := client.HealthCheck(); err != nil {
				if !jsonOutput {
					fmt.Println("Starting pogo server...")
				}
				if err := client.StartServer(); err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				status = "started"
				if !jsonOutput {
					fmt.Println("pogo server started")
				}
			}

			// The mode read is advisory — it only decides the wording. The
			// start-orchestration call below is correct in either mode: it
			// transitions from index-only, and sweeps the crew without touching
			// the mode when already full.
			mode, modeErr := client.GetServerMode()
			if !jsonOutput {
				switch {
				case modeErr == nil && mode == "index-only":
					fmt.Println("Restarting orchestration...")
				case status == "started":
					fmt.Println("Confirming the crew came up...")
				default:
					fmt.Println("The server is already running; checking the crew...")
				}
			}

			report, err := client.StartOrchestration()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"status":  status,
					"message": orchestrationRestartSummary(report),
					"report":  report,
				})
			} else {
				for _, line := range orchestrationRestartLines(report) {
					fmt.Println("  " + line)
				}
			}
			// A crew agent that ERRORED on spawn is a failure of the start, not
			// a detail of it, so the exit code says so — after the report has
			// been printed, so the operator gets the names either way. Starting
			// zero agents because none are eligible (or because the daemon is
			// unconfigured / [agents] autostart = false) is not an error and
			// stays exit 0; that case is reported in words.
			if len(report.AgentsFailed) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}

	var stopAll bool
	var stopHold time.Duration
	var cmdServerStop = &cobra.Command{
		Use:   "stop",
		Short: "Stop orchestration (agents + refinery); use --all for full teardown",
		Long: `By default, stops orchestration (agents and refinery) while keeping
the pogo server running for indexing and search. Use --all to fully
shut down the server process.

A stopped fleet comes back BY ITSELF. pogod arms a resume deadline at the
moment of the stop and restores full mode if nothing else has, then mails the
coordinator to say it had to (mg-5af1). This exists because the fleet being up
used to be contingent on whoever stopped it surviving long enough to restart
it: on 2026-08-08 a deploy stopped the crew, hung for 31h39m, and the fleet
stayed dark for 33 hours with every supervisor behaving correctly.

Use --hold to declare a longer window when a long dark period is INTENDED:

    pogo server stop --hold 4h

There is no way to stop the fleet indefinitely without saying so. That is the
point: an undeclared indefinite dark fleet is indistinguishable from an outage,
and it was one.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if stopAll {
				if !jsonOutput {
					fmt.Println("Stopping pogo server...")
				}
				err := client.StopServer()
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"status":  "stopped",
						"message": "pogo server stopped",
					})
				} else {
					fmt.Println("Server stopped.")
				}
			} else {
				if !jsonOutput {
					fmt.Println("Stopping orchestration (agents + refinery)...")
				}
				report, err := client.StopOrchestrationHold(stopHold)
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"status":  "index-only",
						"message": "orchestration stopped, server still running",
						"report":  report,
					})
				} else {
					fmt.Println("Orchestration stopped. Server still running (indexing + search).")
					// The deadline is printed on the stop, not left to be
					// discovered later. A caller that just took the fleet down
					// is the one reader guaranteed to be present, and telling
					// them when it comes back is what stops the restore from
					// arriving as a surprise.
					switch {
					case report.ResumeDue != "":
						fmt.Printf("The fleet comes back at %s unless you start it first.\n", report.ResumeDue)
						fmt.Println("  pogod restores full mode at that deadline and mails the coordinator (mg-5af1).")
						fmt.Println("  Declare a longer window with --hold if a long dark period is intended.")
					default:
						fmt.Println("⚠ NO resume deadline is armed on this daemon.")
						fmt.Println("  The fleet stays down until something starts it. If this process dies")
						fmt.Println("  before it does, nothing else is responsible for bringing the crew back.")
					}
					fmt.Println("Use --all to fully shut down the server.")
				}
			}
		},
	}
	cmdServerStop.Flags().BoolVar(&stopAll, "all", false, "fully shut down the server process")
	cmdServerStop.Flags().DurationVar(&stopHold, "hold", 0,
		"how long the fleet may legitimately stay stopped (e.g. 4h); default is pogod's resume grace")

	var cmdServerStatus = &cobra.Command{
		Use:     "status",
		Aliases: []string{"health"},
		Short:   "Show pogo server health (uptime, mode, agents, refinery)",
		Long: `Query GET /health/full on pogod and print a short summary.

Use --json for the raw structured response.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			report, err := client.GetFullHealth()
			if err != nil {
				cli.ExitWithError(jsonOutput, "pogo server is not reachable: "+err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(report)
				return
			}
			fmt.Printf("pogod:    %s  (mode=%s, uptime=%s)\n",
				report.Pogod.Status, report.Pogod.Mode, report.Pogod.Uptime)
			fmt.Printf("agents:   %d total, %d running, %d exited\n",
				report.Agents.Total, report.Agents.Running, report.Agents.Exited)
			refState := "stopped"
			if report.Refinery.Running {
				refState = "running"
			}
			if !report.Refinery.Enabled {
				refState = "disabled"
			}
			fmt.Print(formatHealthRefinery(refState, report.Refinery, time.Now()))
		},
	}

	var statusLive bool
	var statusInterval time.Duration
	var statusTag string
	var statusAssignee string

	// renderStatus fetches the current dashboard state and returns it as a
	// fully-formatted text frame. In JSON mode it prints directly and returns
	// "". The whole frame is built into a buffer before returning so live mode
	// can write it to the terminal in a single flicker-free update.
	renderStatus := func() string {
		// statusFilter describes an active --assignee filter to a machine
		// consumer, so it can tell "no work items" from "none matched" without
		// having been told a filter was applied. AppliesTo names the sections
		// the filter actually reached — agents and refinery entries have no
		// assignee, so they are listed in full and are not in it.
		type statusFilter struct {
			Assignee  string   `json:"assignee"`
			AppliesTo []string `json:"applies_to"`
			Matched   int      `json:"matched"`
		}
		// WorkItems is a pointer so that an active filter can emit the key
		// with an empty value ("filtered to nothing") while an absent filter
		// keeps today's behaviour of omitting it entirely.
		type statusReport struct {
			Filter    *statusFilter           `json:"filter,omitempty"`
			Agents    []agent.AgentInfo       `json:"agents"`
			WorkItems *string                 `json:"work_items,omitempty"`
			Refinery  *refinery.Status        `json:"refinery,omitempty"`
			Queue     []refinery.MergeRequest `json:"refinery_queue,omitempty"`
		}

		var report statusReport

		// Agents
		agents, agentErr := client.ListAgents()
		if agentErr == nil {
			report.Agents = agents
		}

		// Work items via mg list. What is fetched does NOT depend on
		// --assignee: the filter is applied to the rendered result below, so
		// the dashboard reads exactly the same thing with or without it.
		mgArgs := []string{"list"}
		if statusTag != "" {
			mgArgs = append(mgArgs, "--tag", statusTag)
		}
		mgOut, mgErr := exec.Command("mg", mgArgs...).CombinedOutput()
		workItems := ""
		if mgErr == nil {
			workItems = strings.TrimSpace(string(mgOut))
		}

		filtering := statusAssignee != ""
		wantAssignee := canonicalAssignee(statusAssignee)
		matched := 0
		if filtering {
			workItems, matched = filterWorkItemsByAssignee(workItems, statusAssignee)
			report.Filter = &statusFilter{
				Assignee:  wantAssignee,
				AppliesTo: []string{"work_items"},
				Matched:   matched,
			}
			// Always present under a filter, even when it selected nothing.
			report.WorkItems = &workItems
		} else if workItems != "" {
			report.WorkItems = &workItems
		}

		// Refinery
		refStatus, refErr := client.GetRefineryStatus()
		if refErr == nil {
			report.Refinery = refStatus
		}
		queue, queueErr := client.GetRefineryQueue()
		if queueErr == nil {
			report.Queue = queue
		}

		if jsonOutput {
			cli.PrintJSON(report)
			return ""
		}

		// --- Text output ---
		// Build the entire frame into a buffer so callers can emit it in one
		// write. Never print incrementally here: in live mode a partially
		// written frame is exactly what causes visible flicker.
		var b strings.Builder

		if statusLive {
			fmt.Fprintf(&b, "pogo status --live  (every %s, Ctrl-C to quit)\n\n", statusInterval)
		}

		// A filter that reaches one section of three has to say so, or an
		// empty work-item list beside a full agent list reads as a fleet that
		// is idle rather than a view that is narrowed.
		if filtering {
			fmt.Fprintf(&b, "Filter: assignee=%s  (work items only; agents and refinery have no assignee and are shown in full)\n\n", wantAssignee)
		}

		// Agents section
		if filtering {
			fmt.Fprintln(&b, "=== Agents (unfiltered) ===")
		} else {
			fmt.Fprintln(&b, "=== Agents ===")
		}
		if agentErr != nil {
			fmt.Fprintf(&b, "  (unavailable: %s)\n", agentErr)
		} else if len(agents) == 0 {
			fmt.Fprintln(&b, "  No agents running.")
		} else {
			crew := 0
			polecats := 0
			running := 0
			for _, a := range agents {
				if a.Type == "crew" {
					crew++
				} else {
					polecats++
				}
				if a.Status == "running" {
					running++
				}
			}
			fmt.Fprintf(&b, "  %d total (%d crew, %d polecat), %d running\n",
				len(agents), crew, polecats, running)
			for _, a := range agents {
				marker := ""
				if a.RateLimited {
					marker = "  ⚠ rate-limited"
				}
				fmt.Fprintf(&b, "  %-20s  %-8s  %-10s  pid=%-6d  uptime=%s%s\n",
					a.Name, a.Type, a.Status, a.PID, a.Uptime, marker)
			}
		}
		fmt.Fprintln(&b)

		// Work items section
		if filtering {
			fmt.Fprintf(&b, "=== Work Items (assignee=%s) ===\n", wantAssignee)
		} else {
			fmt.Fprintln(&b, "=== Work Items ===")
		}
		if mgErr != nil {
			fmt.Fprintln(&b, "  (unavailable: mg not found)")
		} else if filtering && matched == 0 {
			// Stated as a count, not as "no work items": a filter that
			// matched nothing is a different fact from an empty backlog.
			fmt.Fprintf(&b, "  0 matching assignee=%s.\n", wantAssignee)
		} else if workItems == "" {
			fmt.Fprintln(&b, "  No work items.")
		} else {
			for _, line := range strings.Split(workItems, "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		fmt.Fprintln(&b)

		// Refinery section
		if filtering {
			fmt.Fprintln(&b, "=== Refinery (unfiltered) ===")
		} else {
			fmt.Fprintln(&b, "=== Refinery ===")
		}
		if refErr != nil {
			fmt.Fprintf(&b, "  (unavailable: %s)\n", refErr)
		} else {
			state := "stopped"
			if refStatus.Running {
				state = "running"
			}
			if !refStatus.Enabled {
				state = "disabled"
			}
			// "In flight" is stated separately from the pending count. A
			// pending count alone cannot tell a refinery grinding through a
			// merge from one that has stopped, and the dashboard used to show
			// only the count and the pending rows (mg-0c51).
			inFlight := "none"
			if len(refStatus.InFlight) > 0 {
				parts := make([]string, 0, len(refStatus.InFlight))
				for _, ln := range refStatus.InFlight {
					p := fmt.Sprintf("%s[%s]", ln.ID, ln.Repo)
					if !ln.Since.IsZero() {
						p += fmt.Sprintf(" (%s)", time.Since(ln.Since).Round(time.Second))
					}
					parts = append(parts, p)
				}
				inFlight = strings.Join(parts, ", ")
			} else if refStatus.Processing != "" {
				// A pogod predating per-repo lanes reports only the single
				// slot. Fall back to it rather than printing "none" at a
				// refinery that is busy.
				inFlight = refStatus.Processing
				if !refStatus.ProcessingSince.IsZero() {
					inFlight += fmt.Sprintf(" (%s)", time.Since(refStatus.ProcessingSince).Round(time.Second))
				}
			}
			fmt.Fprintf(&b, "  Status: %s  |  In flight: %s  |  Pending: %d  |  History: %d  |  Poll: %s\n",
				state, inFlight, refStatus.QueueLen, refStatus.HistoryLen, refStatus.PollInterval)
		}
		if queueErr == nil && len(queue) > 0 {
			fmt.Fprintln(&b)
			for _, mr := range queue {
				age := time.Since(mr.SubmitTime).Truncate(time.Second)
				author := mr.Author
				if author == "" {
					author = "-"
				}
				fmt.Fprintf(&b, "  %-10s  %-20s  branch=%-30s  author=%-15s  age=%s\n",
					mr.Status, mr.ID, mr.Branch, author, age)
				if mr.Status == refinery.StatusProcessing {
					for _, l := range progressLines(&mr, time.Now()) {
						fmt.Fprintf(&b, "              %s\n", l)
					}
				}
			}
		}

		return b.String()
	}

	var cmdStatus = &cobra.Command{
		Use:   "status",
		Short: "Show unified dashboard of agents, work items, and refinery queue",
		Long: `Show a unified read-only dashboard aggregating:
  - Running agents (from pogod)
  - Work items (from macguffin)
  - Refinery merge queue (from pogod)

Use --live for a continuously updating view (like watch), refreshed every
--interval (default 2s; must be positive).

--assignee narrows the WORK ITEM section and nothing else:

    pogo status --assignee=human    # what is waiting on you
    pogo status --assignee=none     # items nobody has been given
    pogo status --assignee=parked

Agents and refinery merge requests carry no assignee, so they are shown in
full; the output says so on its face, and names the filter in the work-item
header, so a short list next to a busy fleet cannot be misread as an idle one.
Matching is exact and case-insensitive — not substring. 'human' also matches
items assigned to your OS username, which is how mg renders them; 'none'
selects items with no assignee at all (an item assigned to the literal word
"none" is therefore not selectable).

The filter applies per refresh under --live, and is carried in --json as a
"filter" object; under a filter "work_items" is always present, empty when
nothing matched, so a consumer never has to know a filter was applied.

With --json a single snapshot is printed as one indented JSON object.
Combining --live with --json emits a stream of such objects on stdout — one
full snapshot per interval, no terminal control codes — suitable for piping
into a machine consumer (e.g. jq with its streaming slurp). Ctrl-C ends the
stream.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if !statusLive {
				fmt.Print(renderStatus())
				return
			}

			// time.NewTicker panics on a non-positive interval; reject it
			// with a clean error instead.
			if statusInterval <= 0 {
				cli.ExitWithError(jsonOutput, fmt.Sprintf("--interval must be positive, got %s", statusInterval), cli.ExitError)
			}

			// Live mode: refresh in place on interval.
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

			ticker := time.NewTicker(statusInterval)
			defer ticker.Stop()

			// draw fetches the next frame and repaints it flicker-free.
			//
			// The frame is fetched in full BEFORE any terminal control codes
			// are emitted (fetching involves an mg exec + two pogod HTTP
			// calls). We then repaint in a single write: cursor home, each
			// line cleared to end-of-line as it is overwritten, and finally
			// erase-to-end-of-screen to remove any trailing lines left by a
			// previously taller frame. This never blanks the whole screen, so
			// there is no visible flash between frames — unlike a \033[2J
			// full-screen erase, which leaves the terminal blank for the whole
			// fetch latency every tick.
			draw := func() {
				frame := renderStatus()
				if jsonOutput {
					// JSON mode already printed; nothing to repaint.
					return
				}
				var out strings.Builder
				out.WriteString("\033[H") // cursor to top-left
				out.WriteString(strings.ReplaceAll(frame, "\n", "\033[K\n"))
				out.WriteString("\033[J") // erase from cursor to end of screen
				fmt.Print(out.String())
			}

			// One-time full clear so stale scrollback doesn't bleed into the
			// first frame; subsequent repaints reuse the same region.
			if !jsonOutput {
				fmt.Print("\033[2J\033[H")
			}
			draw()

			for {
				select {
				case <-sig:
					fmt.Println()
					return
				case <-ticker.C:
					draw()
				}
			}
		},
	}

	var cmdService = &cobra.Command{
		Use:   "service",
		Short: "Manage the pogo system service",
		Long:  `Install, uninstall, or check the status of the pogo daemon as a system service (launchd on macOS, systemd on Linux).`,
	}

	var serviceInstallDetach bool
	var cmdServiceInstall = &cobra.Command{
		Use:   "install",
		Short: "Install pogo as a system service",
		Long: `Generate and install a launchd plist (macOS) or systemd unit (Linux) so the pogo daemon starts on login and restarts on crash.

The install is idempotent: rerunning it diffs the in-repo plist against the
on-disk plist and only reloads launchd when something changed. If the service
is already loaded and pogod is healthy, the rerun is a no-op. If the plist is
loaded-but-stopped or loaded-with-stale-config, the install unloads it and
performs a fresh load.

On macOS the install runs an orchestrated lifecycle to prevent the
crew/launchd race observed on mg-9cdc (architect's analysis 2026-04-28):
quiesce crew (stop orchestration so crew agents can't auto-respawn pogod),
unload any prior plist, stop the running pogod, wait for :10000 to drain,
load the plist, then health-check launchd-pogod. If a stranger holds :10000
past the drain timeout the install fails fast rather than producing a
silent launchd-pogod exit.

On macOS the install also mails the mayor when it finishes:

  [install] com.pogo.daemon installed and running   — on success
  [install] FAILED com.pogo.daemon                  — on any error

This lets a polecat fire-and-forget the install and have a follow-up agent
verify the result via mail (the call kills the polecat's parent pogod, so the
polecat itself can't observe completion).

Running detached (required when the caller is a child of pogod):

  pogo service install --detach

The --detach flag re-execs pogo in a new session via syscall.Setsid with
stdio redirected to /tmp/pogo-service-install.log. The parent prints the
dispatched PID and exits 0 within ~100ms; the child runs the full install
and self-reports to mayor on completion.

WHY: pogo service install stops the currently-running pogod before launchctl
loads the new one. Any process that's a child of that pogod (a polecat, a
crew agent, a refinery worker) gets SIGHUP'd when its parent dies and exits
mid-install. --detach moves the install into a new session so it survives
the pogod restart. The caller can then exit immediately and rely on the
mailed report for verification. (This replaces the prior nohup+setsid
recipe, which doesn't work on macOS where setsid is not available.)`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if serviceInstallDetach {
				pid, logPath, err := service.Detach("")
				if err != nil {
					cli.ExitWithError(jsonOutput, "failed to detach: "+err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"dispatched": true,
						"pid":        pid,
						"log":        logPath,
					})
				} else {
					fmt.Printf("install dispatched in background; PID=%d; log=%s\n", pid, logPath)
				}
				return
			}
			if err := service.Install(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			// Tier-3 recovery agent (mg-f5fc / mg-6749) is intentionally
			// kept separate from this install path: a wedged pogod must
			// still be recoverable, so install-recovery cannot depend on
			// install. Print a one-line nudge instead of auto-installing.
			if installed, _ := service.RecoveryStatus(); !installed {
				fmt.Println("Recovery agent not installed. Run `pogo service install-recovery` to enable controlled pogod restarts.")
			}
		},
	}
	cmdServiceInstall.Flags().BoolVar(&serviceInstallDetach, "detach", false, "Run the install in a new session and exit immediately; install proceeds in background and self-reports via mail")

	var cmdServiceInstallRecovery = &cobra.Command{
		Use:   "install-recovery",
		Short: "Install the tier-3 recovery LaunchAgent (com.pogo.recovery)",
		Long: `Install com.pogo.recovery — the external launchd agent that bounces pogod via launchctl kickstart -k when signaled.

The recovery agent runs in its own launchd job, independent of pogod's
process tree. Polecats and operators signal a restart by writing a .req
file to ~/.pogo/recovery/queue/ (see ` + "`pogo recovery request`" + `); launchd's
WatchPaths trigger fires the recovery script, which rate-limits and runs
launchctl kickstart -k gui/$UID/com.pogo.daemon.

This subcommand is deliberately separate from ` + "`pogo service install`" + `: if
pogod is wedged, an operator must still be able to install or repair the
recovery agent. Folding it into the regular install would create a
chicken-and-egg.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.InstallRecovery(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceUninstallRecovery = &cobra.Command{
		Use:   "uninstall-recovery",
		Short: "Remove the tier-3 recovery LaunchAgent (com.pogo.recovery)",
		Long:  `Stop and remove com.pogo.recovery. State under ~/.pogo/recovery/ (queue, processed/, failed/, last_restart) is left in place.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.UninstallRecovery(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceInstallDeploy = &cobra.Command{
		Use:   "install-deploy",
		Short: "Install the nightly redeploy LaunchAgent (com.pogo.deploy)",
		Long: `Install com.pogo.deploy — the launchd job that fires ~/.pogo/bin/pogo-deploy.sh daily at 03:00 local to rebuild and redeploy pogod from main.

The job exists because scripts/pogo-self-deploy cannot call itself: its first
line refuses any caller inside pogod's process tree (the kickstart -k it ends
with would kill that caller mid-deploy), and every crew agent and polecat is
such a descendant. A LaunchAgent is parented by launchd, so it clears the guard.

The runner is a trigger, not a deployer. It gates on a 02:00-05:00 window (so a
fire deferred by sleep is dropped, not honoured mid-workday), syncs a DEDICATED
checkout at ~/.pogo/deploy-src (never the developer's working tree), skips
entirely when there is no drift, and then hands off to pogo-self-deploy with
--yes and never --force. A do_prove RED aborts before the restart and alerts.

Deliberately separate from ` + "`pogo service install-recovery`" + `: recovery is the
tier-3 safety net that bounces a wedged pogod, and folding a rebuild-from-main
into it would make every emergency restart a deploy.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.InstallDeploy(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceUninstallDeploy = &cobra.Command{
		Use:   "uninstall-deploy",
		Short: "Remove the nightly redeploy LaunchAgent (com.pogo.deploy)",
		Long:  `Stop and remove com.pogo.deploy. The build checkout under ~/.pogo/deploy-src is left in place.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.UninstallDeploy(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceInstallReclaim = &cobra.Command{
		Use:   "install-reclaim",
		Short: "Install the size-triggered Go module cache reclaim LaunchAgent (com.pogo.reclaim)",
		Long: `Install com.pogo.reclaim — the launchd job that fires ~/.pogo/bin/pogo-reclaim.sh every 30 minutes and runs ` + "`go clean -modcache`" + ` when free space AND cache size have both crossed their floors.

WHY IT EXISTS. On 2026-08-12 this box sat at 100% (571 MiB free of 460G) with a
7.3G module cache, and ./build.sh — which is the refinery's merge gate — failed
at the link step with "no space left on device" across ~40 packages. The cost
was not the outage. It was that the outage presented as a compile error naming
specific packages, so a full disk read as a broken branch.

WHY THE TRIGGER IS TWO NUMBERS ANDed. Free space is the arm that maps to the
observed damage; cache size is the arm that maps to what the reclaim can
actually return. Free-space alone fires on a full disk whose cache is small,
deletes almost nothing, and writes a log line that reads like the disk was
handled — the same misattribution, produced by the fix. Cache-size alone throws
away a cache that costs a network round to rebuild on a box with 300G free.

The schedule is a SAMPLER, not the trigger: launchd has no size trigger, so the
job wakes on an interval and the size decides. A fire costs one ` + "`df`" + `; the
` + "`du`" + ` of the cache is only paid once the disk is already known to be low.

WHAT IT DOES NOT DO. It reclaims the Go module cache and nothing else. On the
box that prompted it that was 7.3G of a 422G fill — headroom, not a fix. When
free space is low and the cache is not why, the job refuses to fire, exits 4,
and says so in the log and in a rate-limited mail.

~/.pogo/bin/pogo-reclaim.sh is a STATIC COPY: a merge to main does not refresh
it. Re-run this command after any change to the runner.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.InstallReclaim(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceUninstallReclaim = &cobra.Command{
		Use:   "uninstall-reclaim",
		Short: "Remove the Go module cache reclaim LaunchAgent (com.pogo.reclaim)",
		Long:  `Stop and remove com.pogo.reclaim. State under ~/.pogo/reclaim (the alert-cooldown stamp) is left in place.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.UninstallReclaim(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var cmdServiceUninstall = &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the pogo system service",
		Long:  `Stop and remove the pogo daemon system service.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.Uninstall(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
		},
	}

	var reconcileDryRun bool
	var cmdServiceReconcile = &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile host-side artifacts (poller scripts) from their repo sources",
		Long: `Reconcile every mirror declared in [reconcile] mirrors onto the host.

For each mirror pogo copies the repo/generator source over the host target using
an ATOMIC replace (write a temp file in the target's directory, then rename(2))
— never an in-place rewrite, because bash reads a script by byte offset and
rewriting it under a live interpreter can resume at a shifted offset and execute
garbage. Then, if the mirror names a launchd job, pogo KICKSTARTS it so the
running process actually picks up the new bytes: writing the file changes
nothing for a long-lived bash while-loop (it parses the loop once and never
re-reads the file), and on this host launchd dispatches no nondemand spawns
(mg-50e0), so an explicit ` + "`launchctl kickstart`" + ` is the only thing that
makes the change real. A re-run also heals a box whose file is already correct
but whose process started before the file was written.

Host artifacts are COPIES, never symlinks into a checkout: a symlink would make
an uncommitted local edit instantly live in production, inverting the repo/host
boundary this step defends (mg-be0c).

Declare mirrors in config.toml:

  [reconcile]
  mirrors = [
    "watchdog|~/dev/pogo-reminders/bin/watchdog.sh|~/.pogo/pogo-reminders/bin/watchdog.sh|com.pogo.watchdog",
  ]`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Load()
			mirrors := cfg.Reconcile.Mirrors
			if len(mirrors) == 0 {
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{"mirrors": []interface{}{}, "message": "no [reconcile] mirrors declared"})
				} else {
					fmt.Println("No [reconcile] mirrors declared. Add them to config.toml under [reconcile] mirrors.")
				}
				return
			}
			deps := reconcile.HostDeps()
			type outRes struct {
				Name        string `json:"name"`
				Changed     bool   `json:"changed"`
				Kickstarted bool   `json:"kickstarted"`
				NewPID      int    `json:"new_pid,omitempty"`
				Reason      string `json:"reason,omitempty"`
				Error       string `json:"error,omitempty"`
			}
			var results []outRes
			anyErr := false
			for _, m := range mirrors {
				mir := reconcile.Mirror{Name: m.Name, Source: m.Source, Target: m.Target, Label: m.Label}
				if reconcileDryRun {
					d := reconcile.CheckDrift(mir, deps)
					r := outRes{Name: m.Name}
					if !d.Clean() {
						r.Changed = true
						r.Reason = "would reconcile: " + strings.TrimSpace(d.Report())
					} else {
						r.Reason = "clean"
					}
					results = append(results, r)
					if !jsonOutput {
						if d.Clean() {
							fmt.Printf("  clean   %s\n", m.Name)
						} else {
							fmt.Printf("%s", d.Report())
						}
					}
					continue
				}
				res := reconcile.Reconcile(mir, service.KickstartJob, deps)
				r := outRes{Name: res.Name, Changed: res.Changed, Kickstarted: res.Kickstarted, NewPID: res.NewPID, Reason: res.Reason}
				if res.Err != nil {
					r.Error = res.Err.Error()
					anyErr = true
				}
				results = append(results, r)
				if !jsonOutput {
					switch {
					case res.Err != nil:
						fmt.Printf("  ERROR   %s: %v\n", res.Name, res.Err)
					case res.Kickstarted:
						fmt.Printf("  updated %s: %s, kickstarted (new pid %d)\n", res.Name, res.Reason, res.NewPID)
					case res.Changed:
						fmt.Printf("  updated %s: %s\n", res.Name, res.Reason)
					default:
						fmt.Printf("  ok      %s: already current\n", res.Name)
					}
				}
			}
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{"dry_run": reconcileDryRun, "results": results})
			}
			if anyErr {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmdServiceReconcile.Flags().BoolVar(&reconcileDryRun, "dry-run", false, "Report what would be reconciled without writing or restarting anything")

	var cmdServiceCheckDrift = &cobra.Command{
		Use:   "check-drift",
		Short: "Report host artifacts that have drifted from their repo sources (never fixes)",
		Long: `Compare every [reconcile] mirror against its source and the RUNNING reality,
and report divergence. This command REPORTS ONLY — it never reconciles. Auto-
fixing drift silently is a reconcile loop fighting a genuinely-broken artifact,
the same failure shape as an unbounded reaper; report loudly, let a human or an
explicit ` + "`pogo service reconcile`" + ` act.

It checks three dimensions per mirror:

  file     the on-disk copy no longer matches its source (a hand-edit or a
           merge that never reached the host);
  loaded   the LOADED launchd job execs a different program than the target —
           a plist whose bytes match the generator but whose loaded job still
           points at the old path (exactly how the recovery plist hid for six
           weeks, mg-6e82);
  process  the process launchd is running started BEFORE the target was last
           written, so it parsed old bytes even at the correct path (pa's
           pollers ran 41 minutes of pre-patch code, mg-be0c).

The last two are the "running reality" checks: the file is not the process.

Exit status is 0 when every mirror is clean, 1 when any drift is found (so it
can gate a schedule or CI step).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Load()
			mirrors := cfg.Reconcile.Mirrors
			deps := reconcile.HostDeps()
			type outDrift struct {
				Name      string `json:"name"`
				Target    string `json:"target"`
				Label     string `json:"label,omitempty"`
				Clean     bool   `json:"clean"`
				FileDrift string `json:"file_drift,omitempty"`
				PathDrift string `json:"path_drift,omitempty"`
				StaleProc string `json:"stale_proc,omitempty"`
			}
			var drifts []outDrift
			driftCount := 0
			for _, m := range mirrors {
				d := reconcile.CheckDrift(reconcile.Mirror{Name: m.Name, Source: m.Source, Target: m.Target, Label: m.Label}, deps)
				drifts = append(drifts, outDrift{
					Name: d.Name, Target: d.Target, Label: d.Label, Clean: d.Clean(),
					FileDrift: d.FileDrift, PathDrift: d.PathDrift, StaleProc: d.StaleProc,
				})
				if !d.Clean() {
					driftCount++
					if !jsonOutput {
						fmt.Printf("%s", d.Report())
					}
				} else if !jsonOutput {
					fmt.Printf("  clean   %s (%s)\n", d.Name, d.Target)
				}
			}
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{"drift_count": driftCount, "mirrors": drifts})
			} else if driftCount == 0 {
				fmt.Printf("deploy OK: %d mirror(s) match source and running reality.\n", len(mirrors))
			} else {
				fmt.Printf("\nDEPLOY DRIFT: %d of %d mirror(s) drifted — what runs is not what the repo says.\n", driftCount, len(mirrors))
				fmt.Println("Fix with: pogo service reconcile")
			}
			if driftCount > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}

	// check-teardown: the gh-issue teardown detector (mg-6e57). Top-level rather
	// than under `service` because it audits WORKFLOW state (mg carriers vs
	// GitHub), not host deploy artifacts.
	var teardownArchived bool
	var cmdCheckTeardown = &cobra.Command{
		Use:   "check-teardown",
		Short: "Report gh-issue carriers that reached done while their issue stayed open (never closes anything)",
		Long: `Audit the LAST step of the gh-issue workflow: for every carrier work item at
` + "`status=done`" + `, ask GitHub whether the referenced ` + "`gh:`" + ` issue is actually closed.

This exists because that step can silently not run. mg-07ba reached
` + "`status=done, stage: merge`" + ` on 2026-07-17 with every promise in the thread
fulfilled — but nobody closed drellem2/pogo#89, and it sat open for four days.
Nothing noticed, because from the outside a carrier that completed its teardown
and one that skipped it are the same three characters: ` + "`done`" + `. The miss is an
ABSENCE, and an absence emits nothing.

This command REPORTS ONLY. It never closes an issue and never comments —
closing an external issue is outward-facing and stays human-gated. Its job is to
make the miss impossible to sit on, not to post on anyone's behalf.

Findings come in three kinds:

  teardown miss   a done carrier whose issue is still OPEN, with no declaration
                  that it is open on purpose. The finding this exists to produce.
  indeterminate   the lookup WORKED and its answer is not a usable state — the
                  ref names an issue or repo that no longer resolves, or GitHub
                  reports a state this detector does not model. These are NOT
                  clean: a failed lookup and a closed issue are indistinguishable
                  to a careless check, so an unreadable answer is reported, never
                  assumed shut. Re-running reproduces them.
  not checked     the lookup FAILED and the carrier was never audited — no
                  network, no credential, a rate limit. Reported apart from
                  indeterminate because a failure to measure is not a
                  measurement (mg-dd22). Network-class failures are retried with
                  backoff first; auth, rate-limit and unclassified ones are not,
                  since re-running a repeatable failure only reproduces it.
  declared open   the carrier says why its issue is open deliberately, via a
                  ` + "`gh-open: <reason>`" + ` line in its body. Listed, but not a miss
                  and not an alert — a detector that cries wolf gets muted long
                  before the run that matters.

A run in which NO carrier reached a verdict is reported as a SUSPECTED
INSTRUMENT FAILURE rather than as a result, and exits ` + fmt.Sprint(exitInstrumentFailure) + `. Twelve carriers
all failing at once is not what twelve broken carriers look like; it is what a
broken detector looks like. On 2026-08-04 one network blip did exactly that to a
batch of 12 — six of which were real teardown misses — and the output was
indistinguishable from a completed scan.

Scans ` + "`status=done`" + ` by default. Archived carriers are NOT scanned unless
--archived is passed: this store holds ~80 archived carriers against 2 done
ones, and each costs a network round-trip. That is a real coverage gap and it is
stated rather than hidden — a carrier archived while its issue is still open is
the most thoroughly forgotten case of all.

Exit status is 0 when nothing is actionable, 1 when anything is found, and ` + fmt.Sprint(exitInstrumentFailure) + `
when the run produced no verdict at all (so a schedule can tell "the check found
something" from "the check could not run" without parsing the report).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Every verdict this command renders comes from a `gh` call, so an
			// unauthenticated gh turns the whole report into indeterminates.
			// In an authed shell this is a no-op; run from launchd, cron, or any
			// other minimal environment it is the difference between an answer
			// and a blind one (mg-03ea). Warned, not fatal — gh can also be
			// authenticated by `gh auth login`, which this cannot see.
			if res := ghtoken.Ensure(); !res.OK() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", res)
			}

			src := ghteardown.MGSource{IncludeArchived: teardownArchived}
			carriers, err := src.Carriers()
			if err != nil {
				// A store we could not read is not "no findings". Fail loudly:
				// silence here would be this detector reproducing, inside itself,
				// exactly the failure it was built to catch.
				fmt.Fprintf(os.Stderr, "cannot read work-item store: %v\n", err)
				os.Exit(cli.ExitError)
			}

			// RetryingLookup, not the bare GHLookup: this box's network is
			// ~50% intermittent (mg-0ffc), and an un-retried lookup turns one
			// blip into a full batch of non-answers (mg-dd22). The CLI and
			// pogod's watcher ride out a blip identically, so a hand re-run
			// cannot disagree with the unattended one for want of a retry.
			rep := ghteardown.Detect(carriers, ghteardown.RetryingLookup(ghteardown.GHLookup))

			if jsonOutput {
				type outFinding struct {
					Carrier string `json:"carrier"`
					Issue   string `json:"issue"`
					Title   string `json:"title,omitempty"`
					Stage   string `json:"stage,omitempty"`
					State   string `json:"state"`
					Detail  string `json:"detail,omitempty"`
					// Class names WHY there is no state, so a consumer can
					// separate a network blip from an auth gap from a deleted
					// issue without re-parsing gh's prose (mg-dd22).
					Class string `json:"class,omitempty"`
				}
				conv := func(fs []ghteardown.Finding) []outFinding {
					out := make([]outFinding, 0, len(fs))
					for _, f := range fs {
						out = append(out, outFinding{
							Carrier: f.Carrier.ID, Issue: f.Carrier.String(),
							Title: f.Carrier.Title, Stage: f.Carrier.Stage,
							State: string(f.State), Detail: f.Detail,
							Class: string(f.Class),
						})
					}
					return out
				}
				cli.PrintJSON(map[string]interface{}{
					"scanned":       rep.Scanned,
					"statuses":      src.Statuses(),
					"miss_count":    len(rep.Misses),
					"indeterminate": conv(rep.Indeterminate),
					"not_checked":   conv(rep.Blocked),
					"misses":        conv(rep.Misses),
					"declared_open": conv(rep.DeclaredOpen),
					"actionable":    rep.Actionable(),
					// A machine consumer must be able to tell a result from a
					// run that measured nothing WITHOUT counting findings —
					// counting is exactly what made "12 indeterminate" read as
					// a completed scan.
					"instrument_failure": rep.InstrumentFailure(),
					"failure_classes":    rep.FailureClasses(),
				})
			} else {
				fmt.Print(rep.Render())
			}

			// A blind run gets its own exit code. Collapsing it into the
			// ordinary "found something" exit would put the two facts a schedule
			// most needs to separate — "the detector has findings" and "the
			// detector cannot see" — behind the same integer.
			if rep.InstrumentFailure() {
				os.Exit(exitInstrumentFailure)
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmdCheckTeardown.Flags().BoolVar(&teardownArchived, "archived", false,
		"Also scan archived carriers (slower: one network lookup per carrier)")

	// check-intake: the gh-issue INTAKE detector (mg-039b). Sibling of
	// check-teardown at the other end of the same workflow — that one catches a
	// carrier that finished while its issue stayed open, this one catches an issue
	// that never got a carrier at all. The on-demand half of the standing runner
	// in pogod; same detector, same inputs.
	var (
		intakeRepos []string
		intakeGrace time.Duration
	)
	var cmdCheckIntake = &cobra.Command{
		Use:   "check-intake",
		Short: "Report open GitHub issues that no work item carries a `gh:` ref for (never files anything)",
		Long: `Reconcile the OPEN issues on the watched repos against the ` + "`gh:`" + ` carrier markers
in the work-item store, and report the issues nothing is tracking.

This exists because a delivered ` + "`[gh]`" + ` mail can be dropped with nothing noticing.
drellem2/pogo#99 was filed 2026-07-29 at 18:53:58Z. The poller mailed the
coordinator 46 seconds later, and again 20 minutes after that when Daniel
commented. Both mails were delivered. Neither produced a work item, and the issue
went ~10 hours with no carrier — invisible to ` + "`mg list`" + `, to
` + "`mg list --tag=gh-issue`" + `, and to every other board the fleet reads. Its paired
issue #100, filed 19 minutes later, WAS carried, so a pair filed to be considered
together got split and the untracked half went dark. It surfaced only because a PM
ran an open-issue sweep by hand, early, on a hunch.

Neither the poller nor mail delivery failed. What failed was follow-through, and
the coordinator prompt already prescribes the discipline that would have prevented
it. Prescribing it was not sufficient: there was no detector, only an instruction.
The set difference is trivially computable and nothing computed it.

The predicate is the ` + "`gh:`" + ` BODY MARKER, at any work-item status including
archived and shelved. Deliberately not the ` + "`gh-issue`" + ` tag (a carrier filed
without the tag is still a carrier, and treating it as absent would produce a
finding nobody could clear) and deliberately not a title match (titles drift; the
marker is a declaration). A ref counts only on a structural line — one that starts
with ` + "`gh:`" + `, outside blockquotes and outside fenced code blocks — so prose
citing an issue does not make an item its carrier.

Findings come in four kinds:

  uncarried    an open issue past the grace window with no carrier. The finding
               this exists to produce.
  unreadable   a watched repo whose open-issue list could NOT be fetched. NOT
               clean: a failed list and a repo with no open issues look identical
               to a careless check, so an unreadable repo is reported rather than
               counted as covered.
  blind scan   the carrier scan examined ZERO work items. Reported as blindness
               rather than as "every open issue is uncarried", which is what
               joining against an empty carrier set would produce — a wall of
               findings that is entirely an artefact of the scan.
  fresh        an uncarried issue still inside --grace. Listed, never alarmed: an
               issue filed 90 seconds ago is a mail in flight, not a dropped one.

REPORTS ONLY. It never files a work item and never comments on an issue — what an
issue IS (triage, duplicate, out of scope, a question) is a judgement, and that
stays with the coordinator.

The watch list comes from --repo if given, else from the issue poller's own state
directory (` + "`$POGO_HOME/gh-issues/seen-<owner>-<repo>.json`" + `) so the two halves of
this reconciliation cannot drift, else from a built-in default. The report says
which.

Exit status is 0 when nothing is actionable, 1 when any uncarried issue,
unreadable repo, or blind scan is found (so it can gate a schedule or CI step).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Every issue list this command renders comes from a `gh` call, so an
			// unauthenticated gh turns the whole report into unreadable repos. In an
			// authed shell this is a no-op; run from launchd, cron, or any other
			// minimal environment it is the difference between an answer and a blind
			// one (mg-03ea). Warned, not fatal — gh can also be authenticated by
			// `gh auth login`, which this cannot see.
			if res := ghtoken.Ensure(); !res.OK() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", res)
			}

			stateDir := filepath.Join(config.PogoHome(), ghintake.PollerStateDirName)
			repos, repoSrc := ghintake.ResolveRepos(intakeRepos, stateDir)

			src := ghintake.MGSource{}
			inv, err := ghintake.Collect(repos, ghintake.GHOpenIssues, src.Carriers, src.Statuses())
			if err != nil {
				// A store we could not read is not "no carriers" — that would turn
				// every open issue into a finding. Fail loudly instead.
				fmt.Fprintf(os.Stderr, "cannot read work-item store: %v\n", err)
				os.Exit(cli.ExitError)
			}

			// grace <= 0 is the documented "report immediately" setting, and Detect
			// reads it that way, so the flag value passes through unmassaged.
			grace := intakeGrace
			rep := ghintake.Detect(inv, time.Now(), grace)

			if jsonOutput {
				type outFinding struct {
					Issue  string `json:"issue"`
					Title  string `json:"title,omitempty"`
					Author string `json:"author,omitempty"`
					URL    string `json:"url,omitempty"`
					AgeSec int    `json:"age_seconds"`
				}
				conv := func(fs []ghintake.Finding) []outFinding {
					out := make([]outFinding, 0, len(fs))
					for _, f := range fs {
						out = append(out, outFinding{
							Issue: f.Issue.Ref(), Title: f.Issue.Title,
							Author: f.Issue.Author, URL: f.Issue.URL,
							AgeSec: int(f.Age.Seconds()),
						})
					}
					return out
				}
				repoErrs := make([]map[string]string, 0, len(rep.RepoErrors))
				for _, e := range rep.RepoErrors {
					repoErrs = append(repoErrs, map[string]string{"repo": e.Repo, "detail": e.Detail})
				}
				itemErrs := make([]map[string]string, 0, len(rep.ItemErrors))
				for _, e := range rep.ItemErrors {
					itemErrs = append(itemErrs, map[string]string{"item": e.ID, "detail": e.Detail})
				}
				cli.PrintJSON(map[string]interface{}{
					"repos":            rep.Repos,
					"repo_source":      repoSrc,
					"statuses":         rep.Statuses,
					"items_scanned":    rep.ItemsScanned,
					"carrier_refs":     rep.CarrierRefs,
					"scanned":          rep.Scanned,
					"carried":          rep.Carried,
					"uncarried_count":  len(rep.Uncarried),
					"uncarried":        conv(rep.Uncarried),
					"fresh":            conv(rep.Fresh),
					"unreadable_repos": repoErrs,
					"unreadable_items": itemErrs,
					"blind_store":      rep.BlindStore,
					"grace_seconds":    int(grace.Seconds()),
					"actionable":       rep.Actionable(),
				})
			} else {
				fmt.Print(rep.Render())
				if len(repos) == 0 {
					// An empty watch list and a clean one both render zero
					// findings. Say which this was, or a check that examined
					// nothing reads as a check that found nothing (mg-f04b).
					fmt.Printf("watch list is EMPTY (%s) — nothing was examined. "+
						"Name repos with --repo, or in [gh_intake] repos.\n", repoSrc)
				} else {
					fmt.Printf("watch list from %s.\n", repoSrc)
				}
			}

			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmdCheckIntake.Flags().StringSliceVar(&intakeRepos, "repo", nil,
		"Watched repo as owner/name (repeatable); default is discovered from the poller's state directory")
	cmdCheckIntake.Flags().DurationVar(&intakeGrace, "grace", config.DefaultGHIntakeGrace,
		"How long an open issue may go uncarried before it counts as a finding (0 or less: report immediately)")

	// check-acks: the completion-deficit detector (mg-1935). The on-demand half
	// of the standing runner in pogod; same detector, same fixture-free inputs.
	var (
		checkAcksPopulations bool
		checkAcksSince       string
		checkAcksUntil       string
	)
	var cmdCheckAcks = &cobra.Command{
		Use:   "check-acks",
		Short: "Report schedules completing far fewer of their fires than their peers (never acts)",
		Long: `Read the scheduler's completion counters and report any schedule completing far
fewer of its fires than its directly comparable peers.

The counters have existed since mg-a754 — ` + "`pogo schedule list`" + ` even renders a
` + "`⚠ N unacked`" + ` marker — and until now nothing read them. On 2026-07-29 at 01:52
the table read:

    mail-check-architect     751/757
    mail-check-pa            753/757
    mail-check-pm-onethird   751/757
    mail-check-pm-pogo       270/757   <-- 36%

pm-pogo had been completing about a third of its mail-check fires FOR ITS ENTIRE
RUN, and the only path to noticing was a human reading that table and comparing
rows. Every liveness instrument said healthy: process alive, health=healthy,
last-activity 0s ago. Claude Code's working spinner is itself PTY output, so no
output-based check can fire on a spinning agent. The completion ratio is the one
number that saw through it, because it measures completed WORK rather than
liveness.

The comparison is CROSS-AGENT, not against a schedule's own history: pm-pogo was
always broken, so there was no regression for a self-comparison to find. A
schedule is judged only against peers of the same kind, on the same cadence,
with a comparable number of fires since registration — and it must be both far
below that peer median AND below an absolute floor.

A WHOLE COHORT failing is reported separately, and it is judged on the LAST FEW
HOURS rather than on the counters. Two outages on 2026-08-10 that had already
ENDED put ~80 dead fires into every 10-minute schedule; the cohort median fell
40% -> 36% -> 26% over a day the fleet spent recovering, and the finding stayed
escalated for 61 hours because a lifetime ratio cannot be pulled back over a
floor by later health. It now reads the absolute completion rate over the
trailing blackout window, behind the same liveness gate, so it CLEARS on its own
once the cohort completes fires again — do not "fix" one of these by
re-registering the schedules, which zeroes the counters and hides the signal
rather than correcting it (mg-c232). The since-registration median is still
printed, labelled as context.

The per-schedule rule keeps its lifetime ratio, because averaging over a long
run is what cancels turn-length noise. The recent window may only RETIRE one of
its findings — the schedule is below its peers over its history and is completing
its fires now — and can never raise one.

Four things are deliberately NOT reported:

  fresh counters     registering a schedule with an existing --id replaces the
                     entry and zeroes its counters, and every crew agent
                     re-registers on startup. With a nightly redeploy that would
                     otherwise flag the whole crew every morning.
  too few fires      a handful of fires is not a sample.
  no peers           a schedule with nothing comparable to compare against is
                     UNJUDGED, and says so, rather than being reported as clean.
  unmeasured cohort  a cohort with too little traffic inside the window to judge
                     — a daily cadence inside a 3-hour window — is named as
                     unmeasured rather than counted healthy.

A recent ` + "`system_wake`" + ` suppresses the whole report: post-sleep replay makes
stale acks expected.

REPORTS ONLY — it never nudges, restarts, or unregisters anything. Note that a
default ` + "`pogo nudge`" + ` cannot reach the agent this typically finds: it waits for
2s of PTY silence that a spinner never delivers. Use ` + "`--immediate`" + `.

Exit status is 0 when nothing is actionable, 1 when any deficit is found.

--populations answers a different question: not WHICH schedule is deficient but
WHAT MECHANISM produced the deficit. Three can, and they have opposite remedies:

  batched      several fires delivered inside one agent turn. Each new fire's
               token supersedes the last, so only one is redeemable however
               diligent the agent is.
  token-less   a fire delivered carrying no token. Nothing to ack, so the
               deficit is unclosable BY THE AGENT — a token-lifetime change
               would not touch it.
  boundary     a fire outstanding at the moment you looked. Bounded at one per
               schedule, so it is negligible against a long history and the
               entire reading against a short one. Not a property of the agent.

It reads events.log rather than the counters, because a re-registration zeroes
the counters and the nightly redeploy guarantees one — so a deficit accumulated
during a storm is erased by the restart that follows it, and the live table
always reads calm. Validate against storm data, not calm data: a fix verified on
a quiet fleet is verified in the conditions where the metric was never wrong.

    pogo check-acks --populations --since 2026-07-29T15:00:00Z --until 2026-07-29T23:00:00Z

--populations never exits non-zero: it is a measurement, not a verdict.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if checkAcksPopulations {
				runAckPopulations(checkAcksSince, checkAcksUntil, jsonOutput)
				return
			}
			entries, err := client.ListSchedules("")
			if err != nil {
				// Scheduler state we could not read is not "no findings".
				// Silence here would be this detector reproducing, inside
				// itself, the failure it was built to catch.
				cli.ExitWithError(jsonOutput, fmt.Sprintf("cannot read scheduler state: %v", err), cli.ExitError)
			}

			now := time.Now()
			snap := ackwatch.Snapshot{Now: now, Samples: ackwatch.SampleEntries(entries, now)}
			if p, perr := scheduler.DefaultPath(); perr == nil {
				logPath := scheduler.EventLogPath(p)
				snap.LastDisruption, snap.DisruptionReason = ackwatch.LastDisruption(logPath, now)
				// The absolute (blackout) arm needs the windowed fire traffic, or
				// it reports itself blind. Without this, `pogo check-acks` would
				// answer the fleet-outage question with the peer-relative arm
				// alone — which is the arm that reads 0 findings during an outage
				// (mg-e2a4).
				recent := ackwatch.RecentFires(logPath, now, ackwatch.DefaultBlackoutWindow)
				snap.Recent = &recent
			}
			// The blackout arm's liveness gate. An empty fleet and a dead one
			// produce identical completion counters, so the arm judges RUNNING
			// agents only — and reports itself blind, rather than guessing, when
			// the daemon cannot say who is running.
			if infos, aerr := client.ListAgents(); aerr == nil {
				runningSince := make(map[string]time.Time, len(infos))
				for _, a := range infos {
					if a.Status == agent.StatusRunning {
						runningSince[a.Name] = a.StartTime
					}
				}
				snap.RunningSince = runningSince
			}
			rep := ackwatch.Detect(snap, ackwatch.DefaultParams())

			if jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Render())
			}

			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmdCheckAcks.Flags().BoolVar(&checkAcksPopulations, "populations", false,
		"split the deficit by mechanism (batched / token-less / boundary) from events.log")
	cmdCheckAcks.Flags().StringVar(&checkAcksSince, "since", "",
		"RFC3339 start of the measured window (default: 7 days ago); --populations only")
	cmdCheckAcks.Flags().StringVar(&checkAcksUntil, "until", "",
		"RFC3339 end of the measured window (default: end of log); --populations only")

	// check-mailloops: the missing-mail-loop reader (mg-032b). The on-demand
	// half of the standing announcer in pogod; same judgement, asked of the
	// whole fleet at once instead of one name at a time.
	var cmdCheckMailLoops = &cobra.Command{
		Use:   "check-mailloops",
		Short: "Report agents with no mail-check schedule — they can be mailed but never woken (never acts)",
		Long: `Report every agent that has NO mail-check schedule. Such an agent can be
mailed, and nothing will ever wake it to read the mail — it is unreachable by
every coordination path the fleet has, while looking perfectly healthy.

This is the same judgement ` + "`pogo agent diagnose <name>`" + ` has reported as
health=no_mail_loop since mg-de08, asked of every agent at once. That difference
is the point. Until mg-032b the ONLY consumer was that per-agent subcommand,
which takes the agent's NAME as an argument — and not knowing which name to type
is exactly what a silently-unreachable agent looks like from the outside. The
fault was detectable, never announced.

pogod now also announces it on its heartbeat (see [deaf_watch] in
docs/CONFIGURATION.md); this command is for when you want the answer now.

WHO IS NOT JUDGED, deliberately:

  polecats            they register their own loop at spawn (mg-e633) with
                      their own escalation path (mg-6fe0); coverage is the
                      witness, not this.
  stopped agents      a configured agent that is not running is owed nothing.
  not configured      no prompt for it on this machine's prompt tree, which was
                      read: it is not one of ours.
  unreadable prompts  the prompt tree could not be READ, so the agent could not
                      be classified, and a false RED costs more than silence.
                      This one is not a clean exclusion — it is a fault in the
                      instrument, and the report says so in those words.

Those exclusions mean a small "judged" count is normal. Every report NAMES the
agents it did not judge, whatever the verdict — a clean bill of health over 2 of
6 agents is a statement about 2 agents, and it says so. A report that judged
NOTHING says so in as many words rather than printing an all-clear, and a pogod
with no basis to judge at all is an ERROR here, not an empty list.

The machine-readable reason in --json is one of "polecat", "not_running",
"not_configured", "unreadable_prompts" — one per category above. Until mg-7b3f
the last two were a single value, because agent.IsConfiguredAgent returned false
for both and the reason set was deliberately kept no more precise than the code
could back; this text named a distinction the code could not compute, which was
this command's own reported defect one level in.

Against a pogod older than this client the unjudged set is absent from the wire
entirely. That is reported as UNKNOWN — never as zero — because "the daemon did
not say" and "nobody was excluded" are opposite statements. Such a pogod also
still sends "not_configured" for an unreadable prompt tree, and nothing on the
wire distinguishes that from a real one: the report carries no version, so this
client cannot detect the skew and does not pretend to. Read a "not_configured"
from an old daemon as the old collapsed value; "pogo version" says which you
have.

REPORTS ONLY — it never registers a schedule, nudges, or restarts. Re-registering
the loop on the agent's behalf would hide WHY it vanished, and that is the part
worth knowing.

Exit status is 0 when every judged agent has a loop, 1 when any agent is
unreachable.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			rep, err := client.MailLoopReport()
			if err != nil {
				// A fleet we could not judge is not a reachable fleet.
				// Silence here would be this detector reproducing, inside
				// itself, the failure it was built to catch.
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Render())
			}
			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}

	// check-commit-body: the closing-keyword adjacency detector (mg-2627).
	// Sibling of check-teardown — that one catches an issue left OPEN, this one
	// catches an issue closed by accident. Same workflow surface, opposite
	// direction.
	var cmdCheckCommitBody = &cobra.Command{
		Use:   "check-commit-body [file]",
		Short: "Reject commit messages whose closing keywords would close a GitHub issue",
		Long: `Read a commit message (from FILE, or stdin when FILE is omitted or "-") and
report every place GitHub would parse a closing keyword followed by an issue
reference — INCLUDING across a line wrap.

Exits non-zero on findings, so it can back a commit-msg hook or a CI step. The
refinery runs the same check on every branch it merges; see
internal/refinery/closingref_gate.go for why both placements exist.

The wrap is the point. On 2026-07-21 a commit body read:

    ...and every promise in the thread was fulfilled — but nobody closed
    drellem2/pogo#89, and it sat OPEN from Jul 17 to Jul 21.

Nobody wrote a directive. ` + "`closed`" + ` is a past-tense verb in a narrative
sentence about someone else's omission, and the reference is a citation. GitHub
joined the lines, read "closing keyword + reference", and shut an external
contributor's issue with no explanation on a thread that had been quiet for four
days. A same-line check would not have seen it.

What passes: ` + "`Refs drellem2/pogo#89`" + `, and ordinary prose citing an issue with
no closing keyword immediately before it. Our commit bodies cite issues
constantly and legitimately; a check that flagged all of them would be off
within a week.

To close an issue on purpose, say so per reference in the body:

    Closing-ref-ack: drellem2/pogo#89 — intentional; <why>

That is a commit-message edit, not a flag — it stays in the permanent record and
suppresses only the reference it names.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			var (
				data   []byte
				err    error
				source string
			)
			if len(args) == 0 || args[0] == "-" {
				source = "commit message (stdin)"
				data, err = io.ReadAll(os.Stdin)
			} else {
				source = args[0]
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "check-commit-body: %v\n", err)
				os.Exit(cli.ExitError)
			}

			// Comment lines are what git's editor template adds and strips
			// again; they never reach the stored message, so judging them
			// would reject commits over text GitHub never sees.
			findings := closingref.Check(stripGitComments(string(data)))
			if len(findings) == 0 {
				return
			}
			fmt.Fprint(os.Stderr, closingref.Report(closingref.CommitMessage, source, findings))
			os.Exit(cli.ExitError)
		},
	}

	var statusRepo string
	var statusRef string
	var statusNoDrift bool
	var cmdServiceStatus = &cobra.Command{
		Use:   "status",
		Short: "Whether the service is installed, and whether the daemon is running the code you think it is",
		Long: `Report two things about this installation:

  1. whether the pogo system service (launchd/systemd) is installed, and
  2. REVISION DRIFT — the three-way running / installed / main comparison.

The second half is the one that answers "am I running what I think I am
running?", and until mg-75ec it had no shipped surface at all. pogod does NOT
self-install: nothing rebuilds the binary when a change merges and nothing
restarts the daemon when the binary is replaced, so an installation drifts from
its own source silently. The fleet's own answer to this lives in
scripts/pogo-self-deploy, which is a repo file — anyone who installed pogo with
` + "`go install`" + ` has no copy of it and so could not see drift at all.

The three axes:

  running pogod     what the LIVE daemon self-reports via GET /version. Read
                    from the process, never from the file: ` + "`go install`" + ` rewrites
                    the on-disk binary underneath a running daemon, and that
                    divergence is exactly the drift being looked for.
  installed pogod   the vcs stamp baked into the pogod binary on disk.
  installed pogo    the same for the CLI. It is not optional cargo: a ` + "`pogo`" + `
                    older than the ` + "`pogod`" + ` it talks to is a protocol mismatch
                    waiting to happen, and a check that reads only the daemon
                    reports health it has not measured.

  main HEAD         what the source repo says should be running.

WITHOUT A CHECKOUT the third axis is simply unavailable, and the report says so
rather than refusing to answer: running-vs-installed is still fully measurable,
and a daemon still running code that has already been replaced on disk is real
drift that needs no repo, no git and no network to establish. Pass --repo (or
set POGO_REPO) to a pogo checkout to get all three.

A REVISION IS EVIDENCE, NOT TRUTH. A binary with no vcs stamp, or one stamped
with a commit that does not exist in the checkout, is reported as UNKNOWN — not
as clean and not as behind. Both would be claims about ancestry the check never
measured. An unstamped binary in particular does not get a "rebuild" verdict:
the rebuild would be unstamped too and the drift would never clear.

REPORT-ONLY. This command never builds, installs, restarts, or reconciles
anything. Its exit status is 0 whether or not drift is found, so existing
callers keep working; read the "status" field of --json to gate on it.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			installed, path := service.Status()
			var report *selfdrift.Report
			if !statusNoDrift {
				r := selfdrift.Check(selfdrift.HostDeps(statusRepo), statusRef)
				report = &r
			}
			if jsonOutput {
				out := map[string]interface{}{
					"installed": installed,
					"path":      path,
				}
				if report != nil {
					out["drift"] = report
				}
				cli.PrintJSON(out)
				return
			}
			if installed {
				fmt.Printf("Service installed: %s\n", path)
			} else {
				fmt.Println("Service not installed.")
			}
			if report != nil {
				fmt.Println()
				fmt.Print(report.Text())
			}
		},
	}
	cmdServiceStatus.Flags().StringVar(&statusRepo, "repo", "", "pogo source checkout to compare against (default $POGO_REPO, else the checkout you are standing in)")
	cmdServiceStatus.Flags().StringVar(&statusRef, "ref", selfdrift.DefaultRef, "git ref in the source checkout whose HEAD the install should match")
	cmdServiceStatus.Flags().BoolVar(&statusNoDrift, "no-drift", false, "skip the revision-drift check and report only whether the service is installed")

	var verifyRevisionExpect string
	var verifyRevisionTimeout time.Duration
	var cmdServiceVerifyRevision = &cobra.Command{
		Use:   "verify-revision",
		Short: "Is the running pogod the revision it is supposed to be? — the check as a GATE, with an exit code",
		Long: `Poll GET /version until the running pogod reports the revision it is supposed to
be running, and exit with a code that says which of three things happened.

WHAT THIS IS FOR. Four code paths restart or verify pogod, and until mg-ed4a
only scripts/pogo-self-deploy asked which revision came back. ` + "`launchctl list`" + `
says a job is registered; ` + "`/health`" + ` says something is listening; ` + "`launchctl\nkickstart`" + ` exiting 0 says launchd accepted the request. None of them says the
RIGHT thing is listening, and a kickstart re-execs whatever is on disk — so
silently reinstating a stale binary is what a restart does when the disk is
stale. This box spent eight days in exactly that state: alive, healthy, 92
commits behind, on a 2026-07-30 build.

The restart paths themselves now PRINT this verdict but do not fail on it —
` + "`pogo service install`" + ` still succeeds against a stale daemon, deliberately and
under mg-ed4a's explicit instruction, because something may depend on that. This
command is the same check for a caller that wants the gate: scripts/launchd/pogo-recovery.sh
runs it after its kickstart, and anything else that must not proceed against the
wrong daemon can too.

EXIT CODES — three, not two, because "could not tell" is a real answer:

  0  AGREES    both revisions were read and they match.
  1  DIFFERS   both were read and the daemon is running something else.
  3  UNKNOWN   at least one side could not be read. NOT a pass. A check that
               goes green because it measured nothing is the failure this
               command exists to remove.

By default the expectation is the vcs revision stamped into the pogod binary
launchd is configured to exec — read from the installed plist, because that is
what launchd actually runs, and it needs no repo, no network and no config.
Pass --expect <rev> for a different expectation (a deploy expects main's HEAD).

AGREES DOES NOT MEAN CURRENT. Against the default expectation it means the
RESTART took: the process is running the binary launchd execs. If that binary is
itself eight days old, this command says AGREES and is right to. Measured on
this box 2026-08-07:

  pogo service verify-revision                       AGREES  (d31297f493cd)
  pogo service verify-revision --expect $(git rev-parse main)
                                                     DIFFERS (main 22e0541f7fd2)

"Is the DISK current?" is a different question with a different instrument —
` + "`pogo service status`" + `, and pogod's standing revision-staleness alarm. Ask this
one that question deliberately with --expect, or do not read its green as an
answer to it.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			res := service.VerifyRevision(verifyRevisionExpect, verifyRevisionTimeout)

			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"verdict":  string(res.Verdict),
					"running":  res.Running,
					"expected": res.Expected,
					"reason":   res.Reason,
					"binary":   service.LaunchdProgramPath(),
					"waited":   res.Waited.Round(time.Second).String(),
				})
			} else {
				fmt.Println(res)
				if !res.OK() {
					fmt.Printf("  binary launchd is configured to exec: %s\n", service.LaunchdProgramPath())
				}
			}

			switch res.Verdict {
			case revcheck.Agrees:
				os.Exit(cli.ExitSuccess)
			case revcheck.Differs:
				os.Exit(cli.ExitError)
			default:
				// A distinct code, so a caller can tell "wrong daemon" from
				// "no reading" — they owe different actions and collapsing
				// them is the defect this whole line of work is about.
				os.Exit(cli.ExitUnknown)
			}
		},
	}
	cmdServiceVerifyRevision.Flags().StringVar(&verifyRevisionExpect, "expect", "",
		"revision the daemon should be running (default: the vcs stamp of the pogod binary launchd execs)")
	cmdServiceVerifyRevision.Flags().DurationVar(&verifyRevisionTimeout, "timeout", revcheck.DefaultTimeout,
		"how long to poll before settling on a verdict; a restarting daemon is unreachable for part of it")

	// Agent commands
	var cmdAgent = &cobra.Command{
		Use:   "agent",
		Short: "Manage agent processes",
		Long:  `Commands for spawning, listing, stopping, and attaching to agent processes managed by pogod.`,
	}

	var cmdAgentStart = &cobra.Command{
		Use:   "start <name>",
		Short: "Start a crew agent by name",
		Long: `Start a crew agent using the prompt file at ~/.pogo/agents/crew/<name>.md.
The agent runs as a persistent crew process that pogod monitors and restarts on crash.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			info, err := client.StartAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(info)
			} else {
				fmt.Printf("Started crew agent %s (pid=%d, prompt=%s)\n", info.Name, info.PID, info.PromptFile)
			}
		},
	}

	var cmdAgentList = &cobra.Command{
		Use:   "list",
		Short: "List agents pogod knows about (presence here is not liveness)",
		Long: `List the agents in pogod's registry, with pid, type, status and uptime.

This is a registry view, not a liveness probe. Do not read it as one:

  - Absence is not evidence of exit. An agent pogod never knew about, or
    one dropped by a restart, is absent while its process runs.
  - Presence is not evidence of life. A listed pid can already be gone;
    status=exited is reported here, but the pid stays stale through the
    ~2s window in which a restart_on_crash agent is being respawned.

To decide whether a process is actually gone, ask for the probe:
'pogo agent diagnose <name> --json' reports process_alive.

A crew agent that is CONFIGURED on this machine and is not running has no
registry entry, so it is not one of the rows above — an absent member cannot
appear in a set it has left. Those agents are named in a footer under the
listing, and 'pogo agent roster' is the full view. --json is unchanged: it
emits the registry array exactly as before, because eight callers consume it
and assume every element has a process behind it (mg-7d20).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			agents, err := client.ListAgents()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(agents)
			} else {
				if len(agents) == 0 {
					fmt.Println("No running agents.")
					printAbsentFooter()
					return
				}
				for _, a := range agents {
					if a.Status == agent.StatusParked {
						fmt.Printf("%-20s  pid=-       type=%-8s  status=%-10s  parked-at=%s\n",
							a.Name, a.Type, a.Status, a.ParkedAt)
						continue
					}
					activity := ""
					if a.LastActivity != "" {
						activity = "  last-activity=" + a.LastActivity
					}
					workItem := ""
					if a.WorkItemID != "" {
						workItem = "  work-item=" + a.WorkItemID
					}
					fmt.Printf("%-20s  pid=%-6d  type=%-8s  status=%-10s  uptime=%s%s%s\n",
						a.Name, a.PID, a.Type, a.Status, a.Uptime, activity, workItem)
				}
				printAbsentFooter()
			}
		},
	}

	var cmdAgentRoster = &cobra.Command{
		Use:   "roster",
		Short: "Show the CONFIGURED crew set against the registry, absences included",
		Long: `Roster lists every crew/mayor agent this machine is configured to have and
says where each one stands: running, parked, or absent.

It is the only pogo reading in which an agent that is NOT running is a row
rather than a silence. 'pogo agent list', the stall-watch, ackwatch and
deaf-watch all iterate pogod's registry, which holds the agents pogod is
running — so a stopped agent is not a row with a bad value in it, it is no row
at all, and nothing distinguishes "this agent is down" from "this agent was
never configured here". crew-doctor was stopped on 2026-08-10 and stayed down
2 days 21 hours with every one of those instruments reading green (mg-7d20).

An absence is not automatically a fault, so each one is reported with what its
own frontmatter asked for:

  auto_start = true   — pogod should have started it at boot and did not.
  auto_start = false  — on-demand; nothing will bring it back until asked.
  prompt unreadable   — configured, and we cannot say what was wanted.

PARKED is not an absence. Park is the supported way to be down: it is
declared, it survives restarts, and it already shows in 'pogo agent list'.

Roster also reports one configuration invariant it is uniquely placed to see,
because it parses every configured prompt's frontmatter: auto_start = true with
restart_on_crash = false. That pairing is the only shape that can leave a
mail-check firing at an agent pogod will never bring back (mg-8677), and it is
reported for a RUNNING agent too — that is when it is still cheap to fix.

pogod announces the same findings on a clock without anyone running this
command — see internal/absentwatch. This is the pull surface for the same
report; neither is inside 'pogo doctor --check', because doctor is the only
routine reader of that checklist and an instrument that cannot go red for the
failure it names is worse than none.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			rep, err := client.AgentRoster()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(rep)
				return
			}
			for _, m := range rep.Members {
				extra := ""
				if m.State == agent.RosterPresent && m.Status != "" {
					extra = "  status=" + string(m.Status)
				}
				fmt.Printf("%-20s  %-8s  class=%-15s%s\n", m.Name, m.State, m.Class, extra)
			}
			if len(rep.Members) > 0 {
				fmt.Println()
			}
			fmt.Print(rep.Render())
		},
	}

	var spawnType string
	var spawnEnv []string
	var cmdAgentSpawn = &cobra.Command{
		Use:   "spawn <name> <command> [args...]",
		Short: "Spawn a new agent process with a PTY",
		Long:  `Spawn a new agent process. pogod allocates a PTY and holds the master fd.`,
		Args:  cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			agentType := agent.AgentType(spawnType)
			if agentType != agent.TypeCrew && agentType != agent.TypePolecat {
				cli.ExitWithError(jsonOutput, "type must be 'crew' or 'polecat'", cli.ExitError)
			}
			info, err := client.SpawnAgent(agent.SpawnAPIRequest{
				Name:    args[0],
				Type:    agentType,
				Command: args[1:],
				Env:     spawnEnv,
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(info)
			} else {
				fmt.Printf("Spawned agent %s (pid=%d, type=%s)\n", info.Name, info.PID, info.Type)
			}
		},
	}
	cmdAgentSpawn.Flags().StringVarP(&spawnType, "type", "t", "polecat", "Agent type: crew or polecat")
	cmdAgentSpawn.Flags().StringSliceVarP(&spawnEnv, "env", "e", nil, "Additional environment variables (KEY=VALUE)")

	var cmdAgentStop = &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop an agent once (a restart_on_crash agent is respawned — see 'park')",
		Long: `Stop terminates the agent's process. It is a one-shot action, not a
dormancy switch.

IF THE AGENT HAS restart_on_crash = true, STOP IS A RESTART.
That flag is an always-on contract: pogod respawns the agent after ANY
exit — a clean return, a crash, or this command. Stop therefore cycles
such an agent rather than keeping it down, and the replacement is a
fresh process with a new pid.

  - To keep a crew agent down — for a maintenance window, or to cycle a
    long-running agent's context — use 'pogo agent park <name>' and
    'pogo agent wake <name>'. Park is the supported stopped-by-intent
    lever: it persists a flag that suppresses the respawn, survives
    pogod restarts, and gates boot-time auto-start.
  - Do not script stop→start against a restart_on_crash agent. You are
    racing pogod's respawn, and when it wins, start fails with "already
    running" — an error that is really reporting the fresh instance.
    Park→wake has no such race: the flag is written before the stop.

Stopping an agent without restart_on_crash keeps it stopped, and stop
is idempotent against an agent whose process has already died.

To confirm a teardown, ask 'pogo agent diagnose <name> --json' for
process_alive. Do not infer it from absence in 'pogo agent list'.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			err := client.StopAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"status": "stopped", "name": args[0]})
			} else {
				fmt.Printf("Agent %s stopped.\n", args[0])
			}
		},
	}

	var cmdAgentPark = &cobra.Command{
		Use:   "park <name>",
		Short: "Park a crew agent: stop it and keep it stopped across restarts",
		Long: `Park puts a crew agent into supported dormancy in one command:

  1. persists a park flag at ~/.pogo/agents/<name>/.parked — it survives
     pogod restarts, suppresses the restart_on_crash respawn, and makes
     boot-time auto-start skip the agent regardless of auto_start;
  2. removes the agent's pogod schedules, recording them in the park file
     so wake can restore them;
  3. stops the agent process.

This is the supported way to keep a restart_on_crash=true agent down —
a plain 'pogo agent stop' is respawned by the supervisor within seconds.
Parked agents show as status=parked in 'pogo agent list'. Reverse with
'pogo agent wake <name>'.

Park is also the supported way to CYCLE an always-on agent: park it,
then wake it. Wake starts a fresh process, so the agent comes back with
a new context, and the recorded schedules are restored with it. Prefer
this to a scripted stop→start, which races the respawn.

Park applies to crew agents only; it rejects polecats, which are
ephemeral by design and are not respawned.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			resp, err := client.ParkAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(resp)
			} else {
				fmt.Printf("Parked agent %s (%d schedule(s) paused). Wake with 'pogo agent wake %s'.\n",
					resp.Agent, resp.SchedulesPaused, resp.Agent)
			}
		},
	}

	var cmdAgentWake = &cobra.Command{
		Use:   "wake <name>",
		Short: "Wake a parked crew agent",
		Long: `Wake reverses a park: starts the agent, restores the schedules that were
recorded when it was parked, and clears the park flag. The agent also
re-registers its own schedules per the crew startup contract; schedule
adds are keyed on (agent, id), so nothing stacks duplicates.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			resp, err := client.WakeAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(resp)
			} else {
				fmt.Printf("Woke agent %s (pid=%d, %d schedule(s) restored).\n",
					resp.Agent, resp.PID, resp.SchedulesRestored)
			}
		},
	}

	var cmdAgentAttach = &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach terminal to a running agent",
		Long: `Connect your terminal to a running agent's PTY via its unix domain socket.
The agent's output streams to your terminal and your input goes to the agent.
Detach with Ctrl-\ to leave the agent running and restore your terminal.

Detaching restores both your terminal's input modes and any display modes the
agent's TUI turned on (alternate screen, mouse and focus reporting, cursor
visibility), so the shell prompt you return to is clean. If the agent exits or
is restarted while you are attached, the attach ends on its own rather than
leaving you on a frozen screen.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			info, err := client.GetAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			fmt.Printf("Attaching to agent %s (pid=%d). Detach with Ctrl-\\.\n", info.Name, info.PID)
			if err := client.AttachAgent(info.SocketPath); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			// Say so explicitly. A detach can be the user's Ctrl-\ or the agent
			// going away underneath them (respawn, stop); silence made the
			// second case read as "my terminal broke" (mg-9b5b).
			fmt.Printf("Detached from agent %s.\n", info.Name)
		},
	}

	var outputPlain bool
	var outputBytes, outputLines int
	var cmdAgentOutput = &cobra.Command{
		Use:   "output <name>",
		Short: "Show recent output from an agent",
		Long: fmt.Sprintf(`Show recent output from an agent's PTY buffer.

Use --plain to strip ANSI/VT escape sequences for human-readable or machine-parseable output.

The default window is the last %d bytes. --bytes selects a larger one, up to the
ring's full %d bytes; --lines selects the last N newline-separated lines out of
the whole ring. The two are mutually exclusive.

Sizing note: pogod's wedge detector judges an agent on the last %d bytes
(wedgewatch.OutputScanBytes). Reproducing what it saw takes --bytes %d; the
default is a quarter of that.`,
			agent.DefaultOutputBytes, agent.OutputRingBytes,
			wedgewatch.OutputScanBytes, wedgewatch.OutputScanBytes),
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// A non-positive value would fall through the >0 test in the
			// client and be sent as "no window given" — i.e. accepted and
			// ignored, the exact shape mg-8a56 was filed about. Refuse it.
			for _, f := range []string{"bytes", "lines"} {
				if v, _ := cmd.Flags().GetInt(f); cmd.Flags().Changed(f) && v <= 0 {
					cli.ExitWithError(jsonOutput,
						fmt.Sprintf("--%s must be positive, got %d", f, v), cli.ExitError)
				}
			}
			output, err := client.GetAgentOutput(args[0], client.AgentOutputOptions{
				Plain: outputPlain,
				Bytes: outputBytes,
				Lines: outputLines,
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"output": output})
			} else {
				fmt.Print(output)
			}
		},
	}
	cmdAgentOutput.Flags().BoolVar(&outputPlain, "plain", false, "Strip ANSI escape sequences from output")
	cmdAgentOutput.Flags().IntVar(&outputBytes, "bytes", 0,
		fmt.Sprintf("Return the last N bytes (default %d, max %d)", agent.DefaultOutputBytes, agent.OutputRingBytes))
	cmdAgentOutput.Flags().IntVar(&outputLines, "lines", 0, "Return the last N lines from the whole retained ring")
	cmdAgentOutput.MarkFlagsMutuallyExclusive("bytes", "lines")

	var cmdAgentStatus = &cobra.Command{
		Use:   "status [name]",
		Short: "Show agent status and details",
		Long:  `Show detailed status for a specific agent, or a summary of all agents.`,
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 1 {
				info, err := client.GetAgent(args[0])
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(info)
				} else {
					fmt.Printf("Name:         %s\n", info.Name)
					fmt.Printf("Process:      %s\n", info.ProcessName)
					fmt.Printf("PID:          %d\n", info.PID)
					fmt.Printf("Type:         %s\n", info.Type)
					fmt.Printf("Status:       %s\n", info.Status)
					fmt.Printf("Uptime:       %s\n", info.Uptime)
					if info.LastActivity != "" {
						fmt.Printf("Last active:  %s\n", info.LastActivity)
					}
					if info.PromptFile != "" {
						fmt.Printf("Prompt:       %s\n", info.PromptFile)
					}
					if info.RestartCount > 0 {
						fmt.Printf("Restarts:     %d\n", info.RestartCount)
					}
					if info.Status == "exited" {
						fmt.Printf("Exit code:    %d\n", info.ExitCode)
					}
					fmt.Printf("Command:      %s\n", strings.Join(info.Command, " "))
					fmt.Printf("Socket:       %s\n", info.SocketPath)
				}
			} else {
				agents, err := client.ListAgents()
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(agents)
				} else {
					if len(agents) == 0 {
						fmt.Println("No agents.")
						return
					}
					crew := 0
					polecats := 0
					running := 0
					for _, a := range agents {
						if a.Type == "crew" {
							crew++
						} else {
							polecats++
						}
						if a.Status == "running" {
							running++
						}
					}
					fmt.Printf("Agents: %d total (%d crew, %d polecat), %d running\n\n",
						len(agents), crew, polecats, running)
					for _, a := range agents {
						extra := ""
						if a.RestartCount > 0 {
							extra += fmt.Sprintf("  restarts=%d", a.RestartCount)
						}
						if a.RateLimited {
							extra += "  rate-limited"
						}
						if a.LastActivity != "" {
							extra += fmt.Sprintf("  last-activity=%s", a.LastActivity)
						}
						fmt.Printf("  %-20s  %-12s  %-8s  pid=%-6d  uptime=%s%s\n",
							a.Name, a.ProcessName, a.Status, a.PID, a.Uptime, extra)
					}
				}
			}
		},
	}

	var cmdAgentDiagnose = &cobra.Command{
		Use:   "diagnose <name>",
		Short: "Diagnose agent health (stall detection, process checks)",
		Long: `Run diagnostics on a specific agent. Checks last-activity timestamps,
process health, idle duration, and stall detection thresholds.

Health states:
  healthy      — produced output within the last 30s (actively working)
  idle         — quiet for over 30s but within the stall threshold (alive, between cycles)
  stalled      — quiet for longer than the stall threshold
  rate_limited — alive but wedged on the provider's usage-limit modal (gh #45)
  no_mail_loop — has no mail-check schedule: it can be mailed, but nothing will
                 wake it to read the mail. Reported for an agent pogod expects
                 to be running (mg-de08) and for any configured agent that IS
                 running, including an auto_start=false one someone turned on —
                 a running agent nothing can wake is a fault whatever its
                 frontmatter wants (mg-738f)
  exited       — process has exited
  dead         — registered as running but OS process is gone

A cron-driven agent (e.g. a */30 mail-check) is idle by design between firings.
While it is within one cron interval of its last scheduled firing it reports
"idle", not "stalled", even past the threshold — see cron_covered in --json.

CONFIRMING A TEARDOWN. process_alive in --json is the signal to use — it is a
kill(pid, 0) probe of the agent's pid, so it answers whether that process is
still there, rather than whether pogod still lists it. Absence from
'pogo agent list' is not evidence of exit and must not be read as one. Note
that a restart_on_crash agent legitimately comes back ~2s after any exit: a
false process_alive means that process is gone, not that the agent will stay
down. To keep it down, park it.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			diag, err := client.DiagnoseAgent(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(diag)
			} else {
				fmt.Printf("Name:           %s\n", diag.Name)
				fmt.Printf("Type:           %s\n", diag.Type)
				fmt.Printf("Status:         %s\n", diag.Status)
				fmt.Printf("PID:            %d\n", diag.PID)
				fmt.Printf("Process alive:  %v\n", diag.ProcessAlive)
				fmt.Printf("Uptime:         %s\n", diag.Uptime)
				if !diag.LastActivity.IsZero() {
					fmt.Printf("Last activity:  %s ago\n", diag.IdleDuration)
				} else {
					fmt.Printf("Last activity:  (no output yet)\n")
				}
				fmt.Printf("Stall threshold: %s\n", diag.StallThreshold)
				fmt.Printf("Health:         %s\n", diag.Health)
				if diag.TranscriptCheck != nil && diag.TranscriptCheck.State == synthfail.StateFailing {
					tc := diag.TranscriptCheck
					fmt.Printf("\n⚠ FAILING EVERY TURN: %s is answering turns locally and failing them —\n", diag.Name)
					fmt.Printf("  %d zero-token failure turns since %s.\n", tc.Count, tc.First.UTC().Format(time.RFC3339))
					fmt.Printf("  Reason: %s — %s\n", tc.Reason, tc.Reason.Human())
					if tc.Detail != "" {
						fmt.Printf("  Harness said: %q\n", tc.Detail)
					}
					fmt.Printf("  This is NOT a wedge: the agent is alive and consuming every nudge on\n")
					fmt.Printf("  time, it just accomplishes nothing with them — so delivery counters read\n")
					fmt.Printf("  green throughout (mg-18d0).\n")
					fmt.Printf("  DO NOT RESTART. A new session inherits the same credential/limit and the\n")
					fmt.Printf("  restart discards this session's context. pogod has suppressed\n")
					fmt.Printf("  restart-based remediation for this agent and paged `human`.\n")
				} else if diag.TranscriptCheck != nil && diag.TranscriptCheck.State == synthfail.StateUnavailable {
					// Say so explicitly. An unread transcript is not a clean bill of
					// health, and rendering it as silence is the absence-as-evidence
					// error this detector exists to prevent.
					fmt.Printf("\nℹ Transcript check unavailable: %s\n", diag.TranscriptCheck.Unavailable)
					fmt.Printf("  Turn-failure detection is OFF for this agent; the states above are\n")
					fmt.Printf("  pogo's pre-detector behaviour. This is not evidence of health.\n")
				}
				if diag.RateLimited {
					fmt.Printf("\n⚠ Agent appears rate-limited (provider usage limit).")
					if !diag.RateLimitedSince.IsZero() {
						fmt.Printf(" Since %s.", diag.RateLimitedSince.UTC().Format(time.RFC3339))
					}
					fmt.Printf("\n  It is alive but wedged on the rate-limit modal; work resumes when the limit\n")
					fmt.Printf("  resets. Do not restart it to \"fix\" the wedge. See docs/operations.md →\n")
					fmt.Printf("  \"Recovering from a usage-limit episode\".\n")
				}
				if diag.CronCovered {
					fmt.Printf("\nℹ Idle past the stall threshold, but within one cron interval of\n")
					fmt.Printf("  the last scheduled firing — this is normal between-cron idle, not a stall.\n")
				}
				if diag.MailCheckMissing {
					fmt.Printf("\n⚠ NO MAIL LOOP: %s has no mail-check schedule. Mail sent to it\n", diag.Name)
					fmt.Printf("  will sit unread until something nudges it by hand — the agent looks fine\n")
					fmt.Printf("  and is unreachable (mg-de08). An agent that is running but not\n")
					fmt.Printf("  auto_start is reported too: turning one on does not give it a mail\n")
					fmt.Printf("  loop, and nothing else will flag that it cannot hear (mg-738f).\n")
					fmt.Printf("  Restore it:\n")
					fmt.Printf("    pogo schedule %s --cron \"*/10 * * * *\" --id mail-check-%s --replay once \\\n", diag.Name, diag.Name)
					fmt.Printf("        --message \"Check your mail with mg mail list %s and handle any unread messages.\"\n", diag.Name)
				}
				if diag.Stalled {
					fmt.Printf("\n⚠ Agent appears stalled. Consider nudging or restarting:\n")
					fmt.Printf("  pogo nudge %s \"status check\"\n", diag.Name)
					fmt.Printf("  pogo agent stop %s\n", diag.Name)
				}
				if diag.Health == "dead" {
					fmt.Printf("\n⚠ Process is gone but agent is still registered. Stop and re-dispatch:\n")
					fmt.Printf("  pogo agent stop %s\n", diag.Name)
				}
				if diag.RecentOutputTail != "" {
					fmt.Printf("\n--- Recent output (last ~500 bytes) ---\n%s\n", diag.RecentOutputTail)
					// Say where the rest is. This tail is a fraction of the
					// window the wedge detector judged on, and a reader with no
					// way to ask for more takes it for everything there is —
					// which is how mg-8a56 produced a wrong bound.
					fmt.Printf("\n(that is a tail; pogod judged this agent on the last %d bytes —\n",
						wedgewatch.OutputScanBytes)
					fmt.Printf(" see them with: pogo agent output %s --bytes %d --plain)\n",
						diag.Name, wedgewatch.OutputScanBytes)
				}
			}
		},
	}

	var cmdAgentWitness = &cobra.Command{
		Use:   "witness",
		Short: "Report witnessed-alive polecats from the on-disk witness (no pogod required)",
		Long: `Read the persisted polecat witness and report which polecats are provably alive.

This asks the PROCESSES, not pogod: each record's (pid, start_time) pair is
re-probed against the kernel, so it answers when pogod is down — which is the
only reason it exists. The redeploy drain uses it to tell a wedged-but-idle
fleet (safe to bounce) from a wedged-and-live one (bouncing mints permanent
survivors) — see scripts/pogo-self-deploy's drain_wait (mg-65b2).

Exit codes distinguish three states that must never be collapsed:
  0  witness present and readable — alive_count is a measurement, 0 included
  2  no witness file — an ABSENCE, not a report of zero
  1  a witness exists but could not be read

An idle fleet leaves a present-and-EMPTY witness, so a missing file is not
evidence that nothing is running.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runAgentWitness(jsonOutput)
		},
	}

	// Prompt subcommands
	var cmdAgentPrompt = &cobra.Command{
		Use:   "prompt",
		Short: "Manage agent prompt files",
		Long:  `Commands for listing and inspecting prompt files in ~/.pogo/agents/.`,
	}

	var cmdAgentPromptList = &cobra.Command{
		Use:   "list",
		Short: "List available prompt files",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			prompts, err := client.ListPrompts()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(prompts)
			} else {
				if len(prompts) == 0 {
					fmt.Println("No prompt files found.")
					fmt.Printf("Create them in %s\n", agent.PromptDir())
					return
				}
				for _, p := range prompts {
					fmt.Printf("%-12s  %-20s  %s\n", p.Category, p.Name, p.Path)
				}
			}
		},
	}

	var cmdAgentPromptInit = &cobra.Command{
		Use:   "init",
		Short: "Create the ~/.pogo/agents/ directory structure",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := agent.InitPromptDirs(); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{
					"status": "created",
					"path":   agent.PromptDir(),
				})
			} else {
				fmt.Printf("Created directory structure at %s\n", agent.PromptDir())
				fmt.Println("  crew/       — Long-running agent prompts")
				fmt.Println("  templates/  — Polecat prompt templates (with {{.Variable}} expansion)")
			}
		},
	}

	var installForce bool
	var installNoBackup bool
	var cmdAgentPromptInstall = &cobra.Command{
		Use:   "install",
		Short: "Install default prompt files to ~/.pogo/agents/",
		Long: `Copy the default mayor prompt and polecat template to ~/.pogo/agents/.
Stale files are auto-updated when the embedded version changes. Use --force to overwrite all files.

When --force overwrites a user-edited canonical file, the pre-overwrite
content is copied to <name>.bak.<timestamp> first so customizations are
recoverable. Pass --no-backup with --force to skip that copy and overwrite
without a safety net.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			result, err := agent.InstallPrompts(agent.InstallOpts{Force: installForce, NoBackup: installNoBackup})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(result)
			} else {
				for _, f := range result.Installed {
					fmt.Printf("  installed: %s\n", f)
				}
				for _, f := range result.Updated {
					fmt.Printf("  updated: %s\n", f)
				}
				for _, f := range result.Skipped {
					fmt.Printf("  skipped (up-to-date): %s\n", f)
				}
				for _, b := range result.Backups {
					fmt.Printf("  backed up: %s -> %s (user-edited; --force overwrote)\n", b.Path, b.BackupPath)
				}
				for _, c := range result.Conflicts {
					fmt.Fprintf(os.Stderr, "  conflict: %s preserved (user-edited); new embed written to %s — see docs/prompt-customization.md to reconcile\n", c.Path, c.DistPath)
				}
				if len(result.Installed) == 0 && len(result.Updated) == 0 && len(result.Skipped) > 0 && len(result.Conflicts) == 0 {
					fmt.Println("All prompts up-to-date.")
				}
			}
		},
	}
	cmdAgentPromptInstall.Flags().BoolVar(&installForce, "force", false, "Overwrite existing prompt files")
	cmdAgentPromptInstall.Flags().BoolVar(&installNoBackup, "no-backup", false, "With --force, skip the pre-overwrite backup of user-edited files")

	var showRaw bool
	var cmdAgentPromptShow = &cobra.Command{
		Use:   "show <name>",
		Short: "Show the synthesised prompt for a named agent or template",
		Long: `Print the prompt content an agent would receive for <name> after applying
extends-directive synthesis, drop-in fragments from ~/.pogo/agents/dropins/<name>/,
and (for polecat templates) {{.Var}} substitution with stub preview values.

Resolves <name> in this order: mayor, crew/<name>.md, templates/<name>.md.
Use --raw to skip synthesis and emit the source file verbatim.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			if showRaw {
				showRawPromptFile(name, jsonOutput)
				return
			}
			out, err := agent.SynthesizePrompt(name, agent.PreviewTemplateVars())
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{
					"name":    name,
					"content": out,
				})
			} else {
				fmt.Print(out)
			}
		},
	}
	cmdAgentPromptShow.Flags().BoolVar(&showRaw, "raw", false, "Show the source file verbatim (skip synthesis and drop-ins)")

	// Create crew prompt
	var createPromptForce bool
	var cmdAgentPromptCreate = &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new crew agent prompt file",
		Long: `Scaffold a new crew agent prompt file at ~/.pogo/agents/crew/<name>.md.

Creates the file with a default template that you can customize. Use --force to
overwrite an existing prompt file.

Example:
  pogo agent prompt create reviewer`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			path, err := agent.CreateCrewPrompt(name, createPromptForce)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{
					"status": "created",
					"name":   name,
					"path":   path,
				})
			} else {
				fmt.Printf("Created crew prompt: %s\n", path)
				fmt.Println("Edit the file to customize your agent's behavior, then start it with:")
				fmt.Printf("  pogo agent start %s\n", name)
			}
		},
	}
	cmdAgentPromptCreate.Flags().BoolVar(&createPromptForce, "force", false, "Overwrite existing prompt file")

	// Spawn polecat from template
	var spawnPolecatTemplate string
	var spawnPolecatTask string
	var spawnPolecatBody string
	var spawnPolecatBodyFile string
	var spawnPolecatId string
	var spawnPolecatRepo string
	var spawnPolecatBranch string
	var spawnPolecatEnv []string
	var spawnPolecatProvider string
	var spawnPolecatNoWorktree bool
	var spawnPolecatPairingOverride string
	var spawnPolecatStrandedOverride string
	var cmdAgentSpawnPolecat = &cobra.Command{
		Use:   "spawn-polecat <name>",
		Short: "Spawn a polecat from a prompt template",
		Long: `Spawn an ephemeral polecat (a disposable worker agent) using a prompt template from ~/.pogo/agents/templates/.
The template is expanded with the provided variables and used as the agent's prompt file.

TEMPLATE SELECTION IS CLOSED. Omit --template and the work item named by --id is
routed on its ` + "`type`" + ` field through a fixed map (design -> polecat-architect,
qa -> polecat-qa). A type that is not in that map — scoping, audit, bug, or a
bare task — selects NO template and the spawn is REFUSED with a 409 naming the
type. It does not fall back to the build worker: a design item silently built
and merged is worse than a dispatch that stops and says why.

Pass --template explicitly to override the map and dispatch anything at any
template, including --template=polecat for the general-purpose build worker.
That override is for a person (or a coordinator acting by hand); an automated
caller that supplies no --template gets the refusal.

The body comes from --body-file (read verbatim from a file, "-" for stdin) or
--body (inline); the two are mutually exclusive. --body-file is the default
idiom — reach for it first:

  pogo agent spawn-polecat cat-1234 --id mg-1234 --body-file - <<'EOF'
  body text with ` + "`backticks`" + ` and $VARS, all literal
  EOF

  pogo agent spawn-polecat cat-1234 --id mg-1234 --body-file ./task.md

THE QUOTING IS THE WHOLE PROPERTY. <<'EOF' is literal; a bare <<EOF expands
exactly like --body="..." and silently reintroduces the bug.

Why: the shell expands ` + "`backticks`" + `, $VAR and $(cmd) inside --body="..." before
pogo runs, so the polecat's prompt silently loses them and pogo cannot tell
that apart from a body someone typed that way. An unset $VAR is the worst
case — it deletes the object of a constraint and leaves prose that still reads
as intentional. --body-file puts no shell in the path at all.

--body remains supported and is not deprecated: it is the inline shortcut, fine
for any body that carries no metacharacters.

A --body-file that cannot be read is an error, never an empty body.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			body, err := bodyFromFlags(cmd, spawnPolecatBody, spawnPolecatBodyFile)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			info, err := client.SpawnPolecat(agent.SpawnPolecatAPIRequest{
				Name:       args[0],
				Template:   spawnPolecatTemplate,
				Task:       spawnPolecatTask,
				Body:       body,
				Id:         spawnPolecatId,
				Repo:       spawnPolecatRepo,
				Branch:     spawnPolecatBranch,
				Env:        spawnPolecatEnv,
				Provider:   spawnPolecatProvider,
				NoWorktree: spawnPolecatNoWorktree,

				PairingOverride:  spawnPolecatPairingOverride,
				StrandedOverride: spawnPolecatStrandedOverride,
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(info)
			} else {
				fmt.Printf("Spawned polecat %s (pid=%d, prompt=%s)\n", info.Name, info.PID, info.PromptFile)
			}
		},
	}
	// No default. A `"polecat"` default here would be a SECOND implementation of
	// the routing decision — and the wrong one, because it reaches pogod as an
	// explicit template and is therefore indistinguishable from a human typing
	// --template=polecat. Sending "" is what lets the closed type→template map
	// in internal/agent/templateroute.go see that no override was given (mg-9a04).
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatTemplate, "template", "", "Worker template name (from ~/.pogo/agents/templates/); omit to route on the work item's `type` — an unrouted type is REFUSED, not defaulted to the build worker")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatTask, "task", "", "Work item title ({{.Task}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatBodyFile, "body-file", "", "PREFERRED: read the work item body ({{.Body}}) verbatim from a file (\"-\" for stdin) — on stdin, use a quoted heredoc <<'EOF'; mutually exclusive with --body")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatBody, "body", "", "Inline shortcut for the work item body ({{.Body}}); the shell expands backticks and $VARS in it — prefer --body-file; mutually exclusive with --body-file")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatId, "id", "", "Work item ID ({{.Id}}); omitting it forfeits start-verification — pogod cannot detect or auto-recover a failed start without a claim signal (mg-2437)")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatRepo, "repo", "", "Target repository path ({{.Repo}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatBranch, "branch", "", "Target branch for refinery submit ({{.Branch}})")
	cmdAgentSpawnPolecat.Flags().StringSliceVarP(&spawnPolecatEnv, "env", "e", nil, "Additional environment variables (KEY=VALUE)")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatProvider, "provider", "", "Harness provider for this polecat (claude, codex, pi); overrides config and template frontmatter")
	cmdAgentSpawnPolecat.Flags().BoolVar(&spawnPolecatNoWorktree, "no-worktree", false, "Skip git worktree creation (no --repo required); polecat edits in-place from ~/.pogo/agents/<name>/ with a refinery:NO posture ({{.NoWorktree}})")
	// A string, not a bool, and the help text says why: the reason is the
	// deliverable. See SpawnPolecatAPIRequest.PairingOverride.
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatPairingOverride, "pairing-override", "", "Dispatch over an unmet paired-item requirement (see [dispatch_pairing]), stating WHY in the value; the reason and the refusal it bypassed are recorded as a dispatch_pairing_overridden event. Overrides that gate only")
	// Also a string, for the same reason. Attribution of a branch to a work item
	// is heuristic, so this gate can be wrong — and a gate that can be wrong with
	// no way past it gets disarmed rather than overridden.
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatStrandedOverride, "stranded-override", "", "Dispatch over pushed-but-unmerged work already on a polecat branch for this item (mg-b468), stating WHY in the value; the reason and the refusal it bypassed are recorded as a dispatch_stranded_work_overridden event. Overrides that gate only")

	// Nudge command — top-level for convenience
	var nudgeImmediate bool
	var nudgeWaitIdle bool
	var nudgeTimeout int
	var cmdNudge = &cobra.Command{
		Use:   "nudge <name> <message>",
		Short: "Send a message to an agent via PTY",
		Long: `Send text to an agent's PTY via pogod.

By default, delivers immediately and then CONFIRMS delivery from the agent's own
prompt-submission receipts. If no receipt arrives, pogod escalates — a bare
return first (which submits text left unsent in the composer and cannot
duplicate anything), then the message again — and if the agent still records
nothing, the nudge FAILS rather than reporting a success nobody can check.

  --wait-idle  the previous behaviour: wait for the agent to stop producing
               output, then write and assume it landed. Note that a working
               agent is producing output by definition, so this mode cannot
               reach a busy one.
  --immediate  write once and return, with no precondition and no confirmation.

Agents whose harness reports no submissions (a provider with no receipt hook, or
an agent spawned before the hook existed) fall back to --wait-idle behaviour
automatically.

If the agent is not running, falls back to sending the message via gt mail.`,
		Args: cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			message := strings.Join(args[1:], " ")

			opts := &client.NudgeOpts{
				Mode:    "confirm",
				Timeout: nudgeTimeout,
			}
			if nudgeWaitIdle {
				opts.Mode = "wait-idle"
			}
			if nudgeImmediate {
				opts.Mode = "immediate"
			}

			fallback, err := client.NudgeOrMail(name, message, opts)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}

			if jsonOutput {
				status := "delivered"
				method := "pty"
				if fallback {
					method = "mail"
				}
				cli.PrintJSON(map[string]string{
					"status": status,
					"agent":  name,
					"method": method,
				})
			} else {
				if fallback {
					fmt.Printf("Agent %s not running — sent via mail.\n", name)
				} else {
					fmt.Printf("Nudged %s.\n", name)
				}
			}
		},
	}
	cmdNudge.Flags().BoolVarP(&nudgeImmediate, "immediate", "i", false, "Write directly to PTY with no precondition and no confirmation")
	cmdNudge.Flags().BoolVar(&nudgeWaitIdle, "wait-idle", false, "Wait for the agent to go quiet, then write and assume delivery (pre-confirmation behaviour)")
	cmdNudge.Flags().IntVarP(&nudgeTimeout, "timeout", "T", 30, "Seconds to spend confirming delivery (or waiting for idle in --wait-idle mode)")

	// Harness-invoked hooks. Not for humans: pogod registers these in an
	// agent's harness settings at spawn and the harness runs them, so they are
	// hidden from help and hold themselves to the contract a hook must meet.
	var cmdHook = &cobra.Command{
		Use:    "hook",
		Short:  "Harness-invoked hook endpoints (registered by pogod, not run by hand)",
		Hidden: true,
	}
	var cmdHookPromptSubmit = &cobra.Command{
		Use:   "prompt-submit",
		Short: "Record that the agent submitted a prompt",
		Long: `Append one submission receipt for the current agent.

pogod registers this as the harness's UserPromptSubmit hook and passes the
receipt file's location in POGO_SUBMIT_RECEIPT. The receipt is what lets a nudge
be confirmed rather than assumed — see 'pogo nudge --help'.

This command ALWAYS succeeds and always prints nothing. A hook that exits
non-zero blocks the agent's prompt, and a hook that writes to stdout has its
output injected into the agent's context: a delivery receipt must never be able
to break, or edit, the delivery it is reporting on.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if path := os.Getenv("POGO_SUBMIT_RECEIPT"); path != "" {
				if err := agent.RecordSubmit(path); err != nil {
					// stderr only: it is not fed back into the agent's context.
					fmt.Fprintf(os.Stderr, "pogo hook prompt-submit: %v\n", err)
				}
			}
		},
	}
	cmdHook.AddCommand(cmdHookPromptSubmit)

	// Scheduler commands. Talks to pogod's /scheduler/* endpoints. The daemon
	// drives fires off the heartbeat tick, so schedules persist across
	// pogod restarts and host sleep — see docs/sleep-resilience-design.md.
	var (
		schedCron     string
		schedID       string
		schedReplay   string
		schedDelivery string
		schedMessage  string
		schedOnce     bool
		schedIn       string
	)
	var cmdSchedule = &cobra.Command{
		Use:   "schedule <agent>",
		Short: "Register a sleep-resilient schedule with pogod",
		Long: `Register a recurring or one-shot wakeup with pogod.

Recurring (--cron required):

  pogo schedule crew-research --cron "*/15 * * * *" --id research-poll \
    --message "check the queue"

One-shot (--once + --in):

  pogo schedule cat-foo --once --in 30m --message "wake up"

Schedules persist in ~/.pogo/schedules.json and fire from pogod's heartbeat
loop — they survive host sleep, NTP steps, and pogod restarts (unlike Claude's
in-process CronCreate). The default replay policy is "once": after a long sleep
the schedule fires exactly once and reschedules to the next future occurrence.`,
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := scheduler.AddRequest{
				Agent:        args[0],
				ID:           schedID,
				Cron:         schedCron,
				OneShot:      schedOnce,
				In:           schedIn,
				ReplayPolicy: scheduler.ReplayPolicy(schedReplay),
				Delivery:     scheduler.DeliveryMode(schedDelivery),
				Message:      schedMessage,
			}
			if !schedOnce && schedCron == "" {
				cli.ExitWithError(jsonOutput, "either --cron or --once + --in is required", cli.ExitError)
			}
			if schedOnce && schedIn == "" {
				cli.ExitWithError(jsonOutput, "--once requires --in <duration>", cli.ExitError)
			}
			entry, err := client.AddSchedule(req)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(entry)
			} else {
				fmt.Printf("Scheduled %s for %s — next fire %s\n", entry.ID, entry.Agent, entry.NextFire.Local().Format(time.RFC3339))
			}
		},
	}
	cmdSchedule.Flags().StringVar(&schedCron, "cron", "", "Standard 5-field cron expression (e.g. \"*/15 * * * *\")")
	cmdSchedule.Flags().StringVar(&schedID, "id", "", "Schedule ID (default: random slug)")
	cmdSchedule.Flags().StringVar(&schedReplay, "replay", "", "Replay policy: once (default), count, skip")
	cmdSchedule.Flags().StringVar(&schedDelivery, "delivery", "", "Delivery: nudge (default) or mail")
	cmdSchedule.Flags().StringVar(&schedMessage, "message", "", "Optional payload delivered on each fire")
	cmdSchedule.Flags().BoolVar(&schedOnce, "once", false, "One-shot wakeup (use with --in)")
	cmdSchedule.Flags().StringVar(&schedIn, "in", "", "Duration from now for --once (e.g. 30m, 2h)")

	var schedListAgent string
	var cmdScheduleList = &cobra.Command{
		Use:   "list",
		Short: "List schedules registered with pogod",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			entries, err := client.ListSchedules(schedListAgent)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(entries)
				return
			}
			if len(entries) == 0 {
				if schedListAgent != "" {
					fmt.Printf("No schedules for %s.\n", schedListAgent)
				} else {
					fmt.Println("No schedules registered.")
				}
				return
			}
			fmt.Printf("%-20s  %-20s  %-25s  %-16s  %s\n", "ID", "AGENT", "NEXT FIRE", "CRON / ONCE", "COMPLETED")
			for _, e := range entries {
				kind := e.Cron
				if e.OneShot {
					kind = "one-shot"
				}
				// A schedule that has never acked reads "—", not "0/N": absent
				// evidence is not evidence of failure (mg-a754).
				completed := "—"
				if e.CompletionTracked() {
					completed = fmt.Sprintf("%d/%d", e.FiresCompleted, e.FiresDelivered)
					if e.UnackedStreak >= scheduler.DefaultStallThreshold {
						completed += fmt.Sprintf("  ⚠ %d unacked", e.UnackedStreak)
					}
				}
				fmt.Printf("%-20s  %-20s  %-25s  %-16s  %s\n",
					e.ID, e.Agent, e.NextFire.Local().Format(time.RFC3339), kind, completed)
			}
		},
	}
	cmdScheduleList.Flags().StringVar(&schedListAgent, "agent", "", "Filter by agent name")

	var schedRmAgent string
	var cmdScheduleRm = &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a schedule by ID",
		Long: `Remove a schedule by ID.

Schedules are keyed on (agent, id). If two agents have registered the same
id, pogod cannot tell which one to remove and the command fails with a
conflict error listing the matching agents — pass --agent <name> to
disambiguate. When the id is owned by a single agent, --agent is optional.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := client.RemoveSchedule(schedRmAgent, args[0]); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"removed": args[0], "agent": schedRmAgent})
			} else {
				fmt.Printf("Removed %s.\n", args[0])
			}
		},
	}
	cmdScheduleRm.Flags().StringVar(&schedRmAgent, "agent", "", "Owning agent (required if multiple agents share the id)")

	// Completion signal (mg-a754). `scheduler_fire_delivered` counts bytes
	// reaching the agent; `ack` is how the agent reports that the WORK those
	// bytes triggered finished. See internal/scheduler/completion.go.
	var (
		schedAckAgent string
		schedAckToken string
	)
	var cmdScheduleAck = &cobra.Command{
		Use:   "ack <id>",
		Short: "Acknowledge that a scheduler fire's work is complete",
		Long: `Acknowledge that the work a scheduler fire triggered has finished.

Every fire is delivered with a completion token in its footer, plus the exact
command that redeems it:

  [scheduler id=mail-check-a754 due=... fired=... ack=9f3c1ab2]
  When this fire's work is done, run: pogo schedule ack mail-check-a754 --agent a754 --token 9f3c1ab2

Run it when the turn's work is done. pogod records the completion and emits a
scheduler_fire_completed event.

Why this exists: scheduler_fire_delivered counts DELIVERY, not completion.
During the 23h30m fleet outage of 2026-07-22 it logged 647 successful
deliveries while every consuming turn failed in ~10ms — all true, all useless,
and a 100%-dead fleet looked exactly like a healthy one. Producing this ack
requires a live model turn that ran a tool, which a failing turn cannot do, so
the signal fails in the same direction as the work it measures.

Only the token from the most recent fire is redeemable; a stale one is rejected
rather than counted, so an old token cannot manufacture a healthy ratio.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if schedAckToken == "" {
				cli.ExitWithError(jsonOutput, "--token is required (copy it from the fire's ack= footer)", cli.ExitError)
			}
			res, err := client.AckSchedule(schedAckAgent, args[0], schedAckToken)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(res)
			} else {
				fmt.Printf("Acked %s for %s — %d/%d fires completed (latency %dms).\n",
					res.Entry.ID, res.Entry.Agent, res.Entry.FiresCompleted, res.Entry.FiresDelivered, res.LatencyMS)
			}
		},
	}
	cmdScheduleAck.Flags().StringVar(&schedAckAgent, "agent", "", "Owning agent (required if multiple agents share the id)")
	cmdScheduleAck.Flags().StringVar(&schedAckToken, "token", "", "Completion token from the fire's ack= footer")

	var (
		schedComplAgent     string
		schedComplThreshold int
	)
	var cmdScheduleCompletion = &cobra.Command{
		Use:   "completion",
		Short: "Show the delivered:completed ratio across schedules",
		Long: `Report how many delivered fires were actually acknowledged as complete.

This is the query the 2026-07-22 events log could not answer. Schedules that
have never acked are counted as UNKNOWN, not failing — only a schedule that has
proven it can ack, and then stopped, is evidence of anything.

"Never acked" spans re-registrations. A schedule re-registered at agent boot
keeps its tracked status but restarts its counters, and is reported separately
so a thin denominator is visible rather than inferred.

The shape to watch for is fleet-wide: one agent skipping one ack is noise,
every tracked schedule going to zero within the same minute is an outage.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			stats, err := client.SchedulerCompletion(schedComplAgent, schedComplThreshold)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(stats)
				return
			}
			fmt.Printf("Schedules:       %d (%d tracked, %d never acked)\n",
				stats.Schedules, stats.Tracked, stats.Schedules-stats.Tracked)
			if stats.TrackedReset > 0 {
				fmt.Printf("                 %d of the tracked were re-registered since their last ack —\n", stats.TrackedReset)
				fmt.Printf("                 they contribute nothing to the ratio below, only to the streak.\n")
			}
			fmt.Printf("Fires delivered: %d\n", stats.FiresDelivered)
			if stats.FiresDelivered == 0 {
				// A zero-denominator ratio printed as "0.0%" reads as total
				// failure when it means "nothing measured yet" — the same
				// could-not-look/looked-and-saw-nothing collapse this whole
				// signal exists to end.
				fmt.Printf("Fires completed: 0 (n/a — no fires delivered against the current counters)\n")
			} else {
				fmt.Printf("Fires completed: %d (%.1f%%)\n", stats.FiresCompleted, stats.Ratio*100)
			}
			fmt.Printf("Stalled:         %d of %d tracked (streak >= %d)\n",
				stats.Stalled, stats.Tracked, stats.StallThreshold)
			if stats.Tracked > 0 && stats.Stalled == stats.Tracked {
				fmt.Printf("\n⚠ EVERY tracked schedule is stalled. That is the fleet-wide shape:\n")
				fmt.Printf("  one upstream cause (expired credential, rate limit, spend cap), not N\n")
				fmt.Printf("  independent wedges. Check an agent's harness before restarting anything.\n")
			}
		},
	}
	cmdScheduleCompletion.Flags().StringVar(&schedComplAgent, "agent", "", "Filter by agent name")
	cmdScheduleCompletion.Flags().IntVar(&schedComplThreshold, "threshold", 0, "Unacked streak at which a schedule counts as stalled (default 2)")

	cmdSchedule.AddCommand(cmdScheduleList)
	cmdSchedule.AddCommand(cmdScheduleRm)
	cmdSchedule.AddCommand(cmdScheduleAck)
	cmdSchedule.AddCommand(cmdScheduleCompletion)

	var initForce bool
	var initMinimal bool
	var cmdInit = &cobra.Command{
		Use:   "init",
		Short: "Scaffold ~/.pogo/agents/ with the default coding profile",
		Long: `Scaffold ~/.pogo/agents/ with prompt files for a fresh workspace.

By default, copies the shipped coding-profile prompts (mayor + crew agents +
polecat templates) into ~/.pogo/agents/. If any target file already exists,
the command refuses to overwrite — pass --force to override.

Use --minimal to scaffold only an empty mayor prompt and a polecat template
skeleton, suitable for non-coding workflows.

This command does not start the daemon or initialize macguffin — for that, use
'pogo install' instead. 'pogo init' is intentionally narrow: it is safe to run
on a clean machine to lay down agent files, and it is safe to fail-fast on a
machine that already has prompts so existing customizations are not silently
overwritten.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Pin legacy role names and re-resolve BEFORE InitPrompts, which
			// expands {{.Coordinator}} into the scaffolded prompt prose and
			// whose next-step print names the coordinator. Without this, the
			// first `pogo init` of a build that flipped the role defaults
			// (mg-ce47) on an existing install scaffolds — and prints "pogo
			// agent start <new-default>" — under a name the pinned config
			// disowns, the same ordering bug `pogo install` fixes at its own
			// seam (mg-e545, xref mg-bc47 / 10d673f). Snapshot existing before
			// InitPrompts writes stamped prompts that IsExistingInstall would
			// otherwise read as an existing install. Non-fatal, like install:
			// a pin failure or rename refusal must not break `pogo init`.
			existingInstall := config.IsExistingInstall()
			_, renameRefusal, pinErr := pinAndResolveRoles(existingInstall)
			if pinErr != nil && !jsonOutput {
				fmt.Fprintf(os.Stderr, "  ⚠ could not pin role defaults: %v\n", pinErr)
			}
			if renameRefusal != nil && !jsonOutput {
				fmt.Fprintf(os.Stderr, "  ⚠ %v\n", renameRefusal)
			}
			result, err := agent.InitPrompts(initForce, initMinimal)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(result)
				return
			}
			fmt.Printf("Scaffolded %s (%s profile):\n", agent.PromptDir(), result.Mode)
			for _, f := range result.Created {
				fmt.Printf("  created: %s\n", f)
			}
			if len(result.Created) == 0 {
				fmt.Println("  (no files created)")
			}
			if result.Mode == "minimal" {
				fmt.Println("\nMinimal profile installed. Edit the files to define your workflow:")
				fmt.Printf("  %s/mayor.md\n", agent.PromptDir())
				fmt.Printf("  %s/templates/polecat.md\n", agent.PromptDir())
			} else {
				fmt.Println("\nReady. Next steps:")
				fmt.Println("  pogo server start          # Start the pogo daemon")
				fmt.Printf("  pogo agent start %-10s # Start the coordinator\n", agent.CoordinatorName())
			}
		},
	}
	cmdInit.Flags().BoolVar(&initForce, "force", false, "Overwrite existing prompt files")
	cmdInit.Flags().BoolVar(&initMinimal, "minimal", false, "Scaffold only an empty mayor and polecat template skeleton")

	var installForceFlag bool
	var installNoBackupFlag bool
	var cmdInstall = &cobra.Command{
		Use:   "install",
		Short: "Set up pogo for agent orchestration",
		Long: `Initialize everything needed for agent orchestration in one step:
1. Start the pogo daemon (if not already running)
2. Initialize macguffin workspace (mg init)
3. Install default agent prompts to ~/.pogo/agents/

Safe to run multiple times — stale prompts are auto-updated, other files are preserved.

When --force overwrites a user-edited canonical, the pre-overwrite content is
copied to <name>.bak.<timestamp> first. Pass --no-backup with --force to skip
that copy and overwrite without a safety net.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Step 1: Ensure daemon is running
			err := client.HealthCheck()
			if err != nil {
				if !jsonOutput {
					fmt.Println("Starting pogo server...")
				}
				// If the launchd/systemd service is installed, restart via the
				// service manager so pogod is properly supervised.
				if installed, _ := service.Status(); installed {
					if err := service.Restart(); err != nil {
						cli.ExitWithError(jsonOutput, "failed to restart service: "+err.Error(), cli.ExitError)
					}
				} else {
					if err := client.StartServer(); err != nil {
						cli.ExitWithError(jsonOutput, "failed to start server: "+err.Error(), cli.ExitError)
					}
				}
				if !jsonOutput {
					fmt.Println("  ✓ pogo server started")
				}
			} else {
				if !jsonOutput {
					fmt.Println("  ✓ pogo server already running")
				}
			}

			// Step 2: Initialize macguffin
			if _, lookErr := exec.LookPath("mg"); lookErr != nil {
				if !jsonOutput {
					fmt.Println("  ✗ macguffin (mg) not found in PATH")
					fmt.Println("")
					fmt.Println("  Agent orchestration requires macguffin. Install it with:")
					fmt.Println("    go install github.com/drellem2/macguffin/cmd/mg@latest")
					fmt.Println("")
					fmt.Println("  See: https://github.com/drellem2/macguffin")
				}
				cli.ExitWithError(jsonOutput, "macguffin (mg) is not installed — install it with: go install github.com/drellem2/macguffin/cmd/mg@latest", cli.ExitError)
			}
			mgInit := func() error {
				c := exec.Command("mg", "init")
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				return c.Run()
			}
			if err := mgInit(); err != nil {
				if !jsonOutput {
					fmt.Println("  ⚠ mg init failed — check macguffin installation")
				}
			} else {
				if !jsonOutput {
					fmt.Println("  ✓ macguffin initialized")
				}
			}

			// Snapshot whether this is a pre-existing install BEFORE InstallPrompts
			// writes fresh prompts — afterwards a brand-new machine would carry
			// stamped prompts and read as existing (see PinRoleDefaultsIfExistingInstall).
			existingInstall := config.IsExistingInstall()

			// Step 2b: On an existing install, pin the current role-name defaults
			// into config.toml so a default-name flip cannot silently rename this
			// deployment's coordinator/worker, and re-resolve this process's role
			// names from the pinned file. Both must happen before the prompts
			// below are synthesized, which expand the role names into prose.
			// Fresh installs are a no-op and adopt the new defaults. Non-fatal:
			// a pin failure must not break `pogo install`.
			pinRes, renameRefusal, pinErr := pinAndResolveRoles(existingInstall)
			if pinErr != nil && !jsonOutput {
				fmt.Fprintf(os.Stderr, "  ⚠ could not pin role defaults: %v\n", pinErr)
			}
			if renameRefusal != nil && !jsonOutput {
				fmt.Fprintf(os.Stderr, "  ⚠ %v\n", renameRefusal)
			}

			// Step 3: Install prompts
			result, err := agent.InstallPrompts(agent.InstallOpts{Force: installForceFlag, NoBackup: installNoBackupFlag})
			if err != nil {
				cli.ExitWithError(jsonOutput, "failed to install prompts: "+err.Error(), cli.ExitError)
			}

			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"status":       "installed",
					"prompts":      result,
					"pinnedRoles":  pinRes.Pinned,
					"configPinned": len(pinRes.Pinned) > 0,
				})
			} else {
				if len(result.Installed) > 0 {
					fmt.Printf("  ✓ installed %d prompt(s)\n", len(result.Installed))
				}
				if len(result.Updated) > 0 {
					fmt.Printf("  ✓ updated %d stale prompt(s)\n", len(result.Updated))
				}
				if len(result.Skipped) > 0 {
					fmt.Printf("  ✓ %d prompt(s) up-to-date\n", len(result.Skipped))
				}
				for _, b := range result.Backups {
					fmt.Printf("  ⚠ backed up: %s -> %s (user-edited; --force overwrote)\n", b.Path, b.BackupPath)
				}
				for _, c := range result.Conflicts {
					fmt.Fprintf(os.Stderr, "  ⚠ conflict: %s preserved (user-edited); new embed written to %s — see docs/prompt-customization.md to reconcile\n", c.Path, c.DistPath)
				}
				if len(pinRes.Pinned) > 0 {
					fmt.Printf("  ✓ pinned current role default(s) [%s] in %s (existing install)\n",
						strings.Join(pinRes.Pinned, ", "), pinRes.Path)
				}
				fmt.Println("\nReady. Next steps:")
				fmt.Printf("  pogo agent start %-9s # Start the coordinator\n", agent.CoordinatorName())
				fmt.Println("  mg new \"your task here\"   # File work for agents")
			}
		},
	}
	cmdInstall.Flags().BoolVar(&installForceFlag, "force", false, "Overwrite existing prompt files")
	cmdInstall.Flags().BoolVar(&installNoBackupFlag, "no-backup", false, "With --force, skip the pre-overwrite backup of user-edited files")

	// Doctor command — system health check
	var doctorCheck bool
	var cmdDoctor = &cobra.Command{
		Use:   "doctor [message]",
		Short: "Diagnose pogo system health",
		Long: `Start the doctor agent for interactive diagnosis, or run quick health checks.

Without --check, starts the doctor crew agent for interactive debugging:
  pogo doctor                    # Start the doctor agent
  pogo doctor "why did the refinery fail?"  # Start + nudge with question

With --check, runs a deterministic health checklist and exits:
  pogo doctor --check            # Quick health checks, no agent

The --check mode verifies:
  - Is pogod running?
  - Does "localhost:<port>" reach pogod, or is another process shadowing it?
  - Is the system service installed?
  - Does every installed launchd plist match the plist this build renders?
  - Are required tools installed (git, go, the configured agent harness)?
  - Are repos configured?
  - Are agent prompts installed?
  - Are there stale work items?
  - Is any MEMORY.md index approaching the harness read cliff?

Exits with code 1 if any critical check fails (--check mode only).`,
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if !doctorCheck {
				// Start the doctor agent
				info, err := client.StartAgent("doctor")
				if err != nil {
					cli.ExitWithError(jsonOutput, "failed to start doctor agent: "+err.Error(), cli.ExitError)
				}
				if jsonOutput {
					result := map[string]interface{}{
						"agent": info,
					}
					// If a message was provided, nudge the agent
					if len(args) > 0 {
						message := strings.Join(args, " ")
						opts := &client.NudgeOpts{Mode: "wait-idle", Timeout: 30}
						_, nudgeErr := client.NudgeOrMail("doctor", message, opts)
						if nudgeErr != nil {
							result["nudge"] = map[string]string{"status": "error", "error": nudgeErr.Error()}
						} else {
							result["nudge"] = map[string]string{"status": "delivered", "message": message}
						}
					}
					cli.PrintJSON(result)
				} else {
					fmt.Printf("Started doctor agent (pid=%d)\n", info.PID)
					if len(args) > 0 {
						message := strings.Join(args, " ")
						opts := &client.NudgeOpts{Mode: "wait-idle", Timeout: 30}
						_, nudgeErr := client.NudgeOrMail("doctor", message, opts)
						if nudgeErr != nil {
							fmt.Printf("Warning: could not nudge doctor: %s\n", nudgeErr)
						} else {
							fmt.Printf("Nudged doctor: %s\n", message)
						}
					}
					fmt.Println("Use 'pogo nudge doctor <message>' to ask questions.")
					fmt.Println("Use 'pogo agent stop doctor' when done.")
				}
				return
			}

			// --check mode: run deterministic health checks
			type checkResult struct {
				Name   string `json:"name"`
				Status string `json:"status"` // "pass", "fail", "warn"
				Detail string `json:"detail,omitempty"`
			}

			var checks []checkResult
			hasFail := false

			pass := func(name, detail string) {
				checks = append(checks, checkResult{Name: name, Status: "pass", Detail: detail})
			}
			fail := func(name, detail string) {
				checks = append(checks, checkResult{Name: name, Status: "fail", Detail: detail})
				hasFail = true
			}
			warn := func(name, detail string) {
				checks = append(checks, checkResult{Name: name, Status: "warn", Detail: detail})
			}

			// 1. Is pogod running?
			if err := client.HealthCheck(); err != nil {
				fail("pogod running", "server is not reachable")
			} else {
				pass("pogod running", "")
			}

			// 1b. Does the loopback NAME reach the daemon check 1 just
			// probed? pogod binds 127.0.0.1 only, so ::1:<port> is free for
			// any other process to claim, and whatever claims it answers for
			// pogod to everything that dials "localhost" — the shape of the
			// 2026-07-31 outage (drellem2/pogo#110). This row speaks only
			// when the two addresses disagree; see loopbackresolution.go.
			{
				loopCfg := config.Load()
				bindProbe := probePogod("http://"+loopCfg.DialAddr(), loopbackProbeTimeout)
				nameProbe := probePogod(fmt.Sprintf("http://localhost:%d", loopCfg.Port), loopbackProbeTimeout)
				lbStatus, lbDetail := loopbackResolutionLine(bindProbe, nameProbe, loopCfg.Port)
				switch lbStatus {
				case "fail":
					fail(loopbackCheckName, lbDetail)
				case "warn":
					warn(loopbackCheckName, lbDetail)
				case "pass":
					pass(loopbackCheckName, lbDetail)
				}
			}

			// 2. System service installed?
			installed, svcPath := service.Status()
			if installed {
				pass("system service", svcPath)
			} else {
				warn("system service", "not installed (run 'pogo service install')")
			}

			// 2b. The installed plists vs the plists this build renders
			// (mg-fc99). `service.Status()` above answers "is there a file
			// there", which is the question that let mg-8f7e ship a plist
			// nobody installed: the file was there, five days stale, and the
			// two retry fires the shipped code believed in did not exist. A
			// fire that never happens writes no log line, so the installed
			// plist is the only witness. See launchagentdrift.go.
			{
				// The audit's own denominator (mg-7a20): the registry is a
				// declaration and `launchctl list` is an observation, and the
				// row used to print only the first — "3 managed job(s)
				// examined: 3 match this build" while 13 pogo jobs were loaded.
				laAudits := service.AuditLaunchAgents()
				laStatus, laDetail := launchAgentActivationLine(laAudits, service.LaunchAgentsSupported(), service.ScopeLaunchAgents(laAudits))
				if laStatus == "warn" {
					warn(launchAgentCheckName, laDetail)
				} else {
					pass(launchAgentCheckName, laDetail)
				}
			}

			// 2b-ii. Is any consumer reading a source nothing writes to
			// (mg-c2f5)? Check 2b above compares an installed plist against
			// the code that ships it, which says nothing about a plist that is
			// exactly as intended and points at a box that has gone quiet.
			// com.pogo.notify was in that state for at least 40 hours —
			// loaded, healthy, polling, reporting no error, watching a
			// directory the fleet does not write to — and every instrument
			// that watches the JOB stayed green throughout. This row compares
			// the configured source against where data is actually arriving.
			// See sourceliveness.go.
			{
				slReport := sourcewatch.Audit(sourcewatch.DefaultLaunchAgentsDir(), time.Now(), sourcewatch.DefaultWindow)
				slStatus, slDetail := sourceLivenessLine(slReport, service.LaunchAgentsSupported())
				if slStatus == "warn" {
					warn(sourceLivenessCheckName, slDetail)
				} else {
					pass(sourceLivenessCheckName, slDetail)
				}
			}

			// 2c. Is some git repository versioning what pogo writes under
			// $POGO_HOME (mg-3610)? A second repo whose working tree IS the
			// pogo home is invisible from inside the pogo repo — nothing here
			// names it — and if it tracks install output, that tree is
			// permanently dirty and a pull there conflicts in the live
			// prompts. Read-only: see internal/homevcs.
			{
				hvStatus, hvDetail := homeVCSLine(homevcs.Audit(context.Background()))
				if hvStatus == "warn" {
					warn(homeVCSCheckName, hvDetail)
				} else {
					pass(homeVCSCheckName, hvDetail)
				}
			}

			// 3. Required tools. git and go are hard requirements. The agent
			// harness binary is a soft check: the pogo CLI works fine without
			// it — only spawning agents needs the harness — and which binary
			// to look for comes from the configured provider, not a hardcoded
			// "claude".
			for _, tool := range []string{"git", "go"} {
				if p, err := exec.LookPath(tool); err != nil {
					fail(tool+" in PATH", "not found")
				} else {
					pass(tool+" in PATH", p)
				}
			}
			// Crew and polecats can each select a different provider via
			// [agents.<type>] provider, so check every distinct configured
			// harness binary, not just the global one.
			agentsCfg := config.Load().Agents
			checkedProviders := map[string]bool{}
			for _, agentType := range []string{"crew", "polecat"} {
				provider, known := providers.Resolve(agentsCfg.AgentProvider(agentType))
				if checkedProviders[provider.ID] {
					continue
				}
				checkedProviders[provider.ID] = true
				if !known {
					warn("agent provider", fmt.Sprintf("unknown provider configured for %s; using fallback %q", agentType, provider.ID))
				}
				if p, err := exec.LookPath(provider.Binary); err != nil {
					warn(provider.Binary+" in PATH", fmt.Sprintf("not found (configured agent harness %q)", provider.ID))
				} else {
					pass(provider.Binary+" in PATH", p)
				}
			}

			// 4. Repos configured
			projs, projErr := client.GetProjects()
			if projErr != nil {
				warn("projects", "could not query projects: "+projErr.Error())
			} else if len(projs) == 0 {
				warn("projects", "no repos registered (run 'pogo visit <path>')")
			} else {
				pass("projects", fmt.Sprintf("%d repo(s) registered", len(projs)))
			}

			// 5. Agent prompts installed
			promptDir := agent.PromptDir()
			if _, err := os.Stat(promptDir); os.IsNotExist(err) {
				warn("agent prompts", "~/.pogo/agents/ not found (run 'pogo install')")
			} else {
				prompts, err := agent.ListPrompts()
				if err != nil {
					warn("agent prompts", "error listing: "+err.Error())
				} else if len(prompts) == 0 {
					warn("agent prompts", "no prompts found (run 'pogo agent prompt install')")
				} else {
					pass("agent prompts", fmt.Sprintf("%d prompt(s) found", len(prompts)))
				}

				// 5b. Drift: live prompt files vs embedded source-of-truth.
				// A drift means the binary has shipped prompt updates that
				// running agents cannot see. Fail (not warn) so this is loud
				// — the PM tier silently skipped roadmap.md regen for days
				// when this drift went undetected (mg-ec77).
				if drift, derr := agent.CheckPromptDrift(); derr != nil {
					warn("agent prompts up-to-date", "drift check failed: "+derr.Error())
				} else if len(drift) > 0 {
					// Two states, two remedies. Install-fixable drift
					// (missing/unstamped/stale) is cured by re-running
					// install. An "edited" canonical is NOT: the installer
					// declines to clobber the local edit and only writes
					// <name>.dist, so advising install there would exit 0 and
					// change nothing — a false "I ran the fix" (mg-04ab).
					// Never fold the two into one remedy string.
					var installable, edited []agent.PromptDrift
					for _, d := range drift {
						if agent.DriftInstallFixable(d.Reason) {
							installable = append(installable, d)
						} else {
							edited = append(edited, d)
						}
					}
					if len(installable) > 0 {
						names := make([]string, 0, len(installable))
						for _, d := range installable {
							names = append(names, fmt.Sprintf("%s (%s)", d.Path, d.Reason))
						}
						// Name the owner and the cadence, not just the manual
						// remedy. Act 3 is NOT a step somebody has to remember:
						// pogod's boot runs InstallPrompts before it auto-starts
						// any crew, so the nightly deploy's kickstart installs
						// prompts every night. Drift showing up HERE therefore
						// means the binary embeds something newer than the last
						// restart propagated — a restart is the standing fix and
						// the CLI call is the way to not wait for one. mg-b6bd
						// was filed believing nothing installed prompts at all;
						// a remedy that names only the manual command is how a
						// reader arrives at that belief.
						fail("agent prompts up-to-date",
							fmt.Sprintf("%d prompt(s) drifted from embedded source: %s — run 'pogo agent prompt install', then restart affected agents. (pogod's boot installs prompts on every restart, so the nightly deploy clears this by itself; running it now is how you avoid waiting for that. `pogo events list --type=prompt_refresh` is the record of what each restart installed.)",
								len(installable), strings.Join(names, ", ")))
					}
					if len(edited) > 0 {
						names := make([]string, 0, len(edited))
						for _, d := range edited {
							names = append(names, fmt.Sprintf("%s (reconcile against %s.dist)", d.Path, d.Path))
						}
						fail("agent prompts up-to-date (local edits)",
							fmt.Sprintf("%d hand-edited prompt(s) diverged from the embedded source: %s — 'pogo agent prompt install' will NOT overwrite your edits; it writes the shipped copy to <name>.dist. Reconcile each canonical against its .dist sidecar (run install first if the .dist is absent), then restart affected agents",
								len(edited), strings.Join(names, ", ")))
					}
				} else {
					pass("agent prompts up-to-date", "all prompts match embedded source")
				}
			}

			// 6. Macguffin available, and no stale claims. The count comes
			// from mg's machine-readable listing, never the rendered one —
			// see staleclaims.go for why counting rendered lines made every
			// clean store report exactly one claimed item (mg-b13b).
			if _, err := exec.LookPath("mg"); err != nil {
				warn("macguffin (mg)", "not found in PATH (install: go install github.com/drellem2/macguffin/cmd/mg@latest)")
			} else if count, err := claimedWorkItemCount(); err != nil {
				// mg is installed but the store could not be listed; an
				// uninitialised MG_ROOT is the ordinary cause and is not a
				// pogo health problem. Say the claims went UNCHECKED rather
				// than only "installed": a detector that quietly stopped
				// running reads exactly like one with nothing to report.
				pass("macguffin (mg)", "installed — claimed items NOT checked: "+err.Error())
			} else if count == 0 {
				pass("macguffin (mg)", "no stale claims")
			} else {
				warn("macguffin (mg)", fmt.Sprintf("%d claimed work item(s) — check for stale claims", count))
			}

			// 6b. Audits that merged and were answered by nothing (mg-28b7).
			//
			// A DETECTOR, not a gate: it reports and never refuses, and it never
			// sets hasFail. See auditsuccessors.go for why it renders here rather
			// than as mail, and internal/auditwatch for what it cannot see.
			//
			// It reads the store directly rather than shelling out to mg — unlike
			// the stale-claim count above — because the question is a JOIN across
			// the whole store (every done audit against every item that might
			// reference it) and there is no mg query that expresses it.
			{
				asCfg := config.Load().AuditSuccessor
				asRep, asErr := auditwatch.Scan(auditwatch.DefaultRoot(), asCfg, time.Now())
				asStatus, asDetail := auditSuccessorLine(asRep, asErr, asCfg, time.Now())
				if asStatus == "warn" {
					warn(auditSuccessorCheckName, asDetail)
				} else {
					pass(auditSuccessorCheckName, asDetail)
				}
			}

			// 7. Auto-memory index health, on THREE axes.
			//
			// A memory store fails in three ways, all of them invisible from
			// inside the session that has them, and only the first is a question
			// about size (mg-cb71):
			//
			//   OVER-CAP TRUNCATION  the dropped entries never arrive.
			//   AN UNINDEXED NOTE    nothing points at it.
			//   A STALE HOOK         it reads as current.
			//
			// The size axis alone does not merely miss the other two — on the
			// unindexed-note axis it reads the failure as an IMPROVEMENT, because
			// dropping a hook makes MEMORY.md smaller. All three share one walk
			// over memcheck.Locate; each reports as its own check so a warn on one
			// axis cannot be read as a verdict on the others.
			//
			// 7a. Auto-memory indexes approaching a harness cliff.
			// There are TWO cliffs in two different units, and this checks
			// both (mg-9a89): session-start AUTO-INJECTION truncates at 25000
			// CHARACTERS, and the READ TOOL refuses past 25000 TOKENS. The
			// character one binds first for ordinary index prose — ~2.6x
			// sooner — and it is the path MEMORY.md actually loads through, so
			// checking only the token cap (as this did before) passes an index
			// that is already losing entries at session start. Token counts
			// are ESTIMATED — see memcheck.EstimateTokens for the measured
			// error bounds — while character counts are exact. DETECT + WARN
			// ONLY: it never rewrites
			// MEMORY.md. Compaction is a destructive rewrite of the shared
			// durable record and stays a deliberate, human-verified judgment
			// call — a warn here, never an auto-fix (mg-15c0).
			if home, herr := os.UserHomeDir(); herr != nil {
				warn("memory index size", "could not resolve home dir: "+herr.Error())
			} else {
				// Harness memory roots come from the provider registry, not
				// from a literal inside memcheck — so this check covers
				// whichever harnesses are in play rather than Claude alone.
				memFiles := memcheck.Locate(home, providers.MemoryIndexGlobs())
				// One walk, three axes. The resolver is built once so its
				// memo cache spans every index: the same work-item id
				// recurs across memory dirs and each lookup is a process
				// spawn.
				resolve := memcheck.NewMgResolver()
				var approaching []memcheck.Result
				var offParity []memcheck.ParityResult
				var staleHooks []memcheck.StaleHook
				// The size and parity axes report separately, but a reader
				// near the cap needs each one to know about the other: they
				// compete, and whichever is read alone implies a resolution
				// that abandons the other (mg-1b2f). These carry the coupling
				// across, keyed by index path since both axes walk the same
				// population.
				sizeByPath := map[string]memcheck.Result{}
				unrefByPath := map[string]int{}
				lineCostByPath := map[string]int{}
				checked, parityChecked, notesChecked, optedOut := 0, 0, 0, 0
				idsTried, idsResolved := 0, 0
				for _, mf := range memFiles {
					res, cerr := memcheck.CheckFile(mf)
					if cerr != nil {
						continue
					}
					checked++
					sizeByPath[mf] = res
					if res.Approaching {
						approaching = append(approaching, res)
					}
					if data, rerr := os.ReadFile(mf); rerr == nil {
						lineCostByPath[mf] = memcheck.IndexLineCostChars(data)
					}
					if p, perr := memcheck.CheckParity(mf); perr == nil {
						parityChecked++
						notesChecked += p.Notes
						optedOut += len(p.OptedOut)
						if !p.InParity {
							offParity = append(offParity, p)
							unrefByPath[mf] = len(p.Unreferenced)
						}
					}
					if rep, serr := memcheck.StaleCheckFile(mf, resolve); serr == nil {
						staleHooks = append(staleHooks, rep.Hooks...)
						idsTried += rep.Attempted
						idsResolved += rep.Resolved
					}
				}
				memcheck.SortHooks(staleHooks)
				if checked == 0 {
					// No auto-memory indexes on this machine — nothing to warn
					// about, and their absence is not itself a problem.
					pass("memory index size", "no MEMORY.md indexes found")
				} else if len(approaching) == 0 {
					pass("memory index size", fmt.Sprintf("%d MEMORY.md index(es) under %.0f%% of both the %d-char auto-inject cap and the %d-token read cap",
						checked, memcheck.WarnFraction*100, memcheck.HarnessAutoInjectCapChars, memcheck.HarnessReadCapTokens))
				} else {
					for _, res := range approaching {
						var fat []string
						for _, ln := range res.FattestLines {
							text := ln.Text
							if len(text) > 100 {
								text = text[:100] + "…"
							}
							fat = append(fat, fmt.Sprintf("[~%dtok] %s", ln.Tokens, text))
						}
						// Name the cliff that is actually near. Reporting the
						// slack budget's number next to a warn is how an index
						// that is truncating reads as merely large.
						var which string
						if res.ApproachingAutoInject {
							which = fmt.Sprintf(
								"SESSION-START AUTO-INJECTION: %d chars, at/over the %d-char warn threshold (%.0f%% of the %d-char cap). Past %d chars the harness injects only the entries that fit and drops the rest for the whole session",
								res.Chars, res.ThresholdChars, memcheck.WarnFraction*100, res.CapChars, res.CapChars)
						} else {
							which = fmt.Sprintf(
								"READ TOOL: ~%d tokens, at/over the %d-token warn threshold (%.0f%% of the %d-token cap). Past %d tokens a read is refused or paginated. Token counts are ESTIMATED (±~11%%), so treat this as a margin warning, not a deadline",
								res.EstTokens, res.ThresholdTokens, memcheck.WarnFraction*100, res.CapTokens, res.CapTokens)
						}
						// If parity fires on this same index, say so HERE. A
						// reader told only "compact" resolves it the cheapest
						// way available, and the cheapest way to shrink an
						// index is to stop hooking notes (mg-1b2f).
						tension := memcheck.SizeParityTension(unrefByPath[res.Path], lineCostByPath[res.Path])
						if tension != "" {
							tension = " " + tension
						}
						warn("memory index size", fmt.Sprintf(
							"%s (%d chars, ~%d tokens, %dB) — %s. The harness announces the truncation, but an agent cannot notice entries that were never injected. Compact it deliberately (never auto — verify the entry count and links).%s Heaviest index lines: %s",
							res.Path, res.Chars, res.EstTokens, res.SizeBytes, which, tension, strings.Join(fat, " | ")))
					}
				}
				// 7b. INDEX/FILE PARITY. A note the index does not name is
				// written, costs disk, and is unreachable by recall — and this is
				// the axis where a size check moves the WRONG WAY, since dropping
				// a hook makes MEMORY.md smaller. The opt-out count is reported
				// alongside so a reader can see deliberate non-indexing is
				// accounted for rather than folded silently into the defects.
				// DETECT ONLY, for the same reason as the size axis: appending
				// missing hooks unattended would index working scratch files.
				if parityChecked == 0 {
					pass("memory index parity", "no MEMORY.md indexes found")
				} else if len(offParity) == 0 {
					pass("memory index parity", fmt.Sprintf(
						"%d note(s) across %d index(es) all referenced%s",
						notesChecked, parityChecked, optedOutNote(optedOut)))
				} else {
					for _, p := range offParity {
						names := p.Unreferenced
						if len(names) > 8 {
							names = append(names[:8:8], fmt.Sprintf("… and %d more", len(p.Unreferenced)-8))
						}
						// The remedy is budget-dependent, so it is computed
						// rather than fixed prose. Near the cap "add a hook
						// for each" is an instruction that cannot be followed
						// alongside the size warn, and a reader handed two
						// impossible instructions satisfies the cheaper one by
						// leaving the notes unreachable (mg-1b2f).
						remedy := memcheck.ParityRemedy(
							len(p.Unreferenced),
							sizeByPath[p.IndexPath].HeadroomChars(),
							lineCostByPath[p.IndexPath])
						warn("memory index parity", fmt.Sprintf(
							"%s — %d of %d note(s) NOT referenced by the index: %s. They are on disk and unreachable by recall: nothing points at them, so the agent that wrote them cannot recall them either. %s Or declare a note deliberately unindexed with '%s' in its frontmatter (a working scratch file legitimately has no hook). Never auto-append hooks%s",
							p.IndexPath, len(p.Unreferenced), p.Notes, strings.Join(names, ", "),
							remedy, memcheck.UnindexedMarker, optedOutNote(len(p.OptedOut))))
					}
				}

				// 7c. STALE TENSE-BEARING ASSERTIONS. An index line asserting a
				// live state about a work item that has since gone terminal reads
				// as current and is wrong. The predicate is CONSISTENCY, not the
				// presence of a tense word: a line that records its own resolution
				// is maintained, and an id that does not resolve UNIQUELY stays
				// silent rather than stale. That silence is deliberate — the
				// natural response to a "stale" verdict is deleting the hook, so a
				// false positive destroys a correct memory (mg-cb71).
				// "Could not look" must never render as "nothing stale". There are
				// two distinct ways to be blind, and the second bit during
				// verification of this very check: macOS ships /usr/bin/mg, an
				// unrelated micro-emacs clone, so a PATH check alone reported
				// "available" while every lookup failed and the axis passed clean.
				if !memcheck.MgAvailable() {
					warn("memory index staleness", "cannot check: no 'mg' on PATH, so no index hook's work-item status could be resolved. This is NOT a report that the hooks are current")
				} else if idsTried > 0 && idsResolved == 0 {
					warn("memory index staleness", fmt.Sprintf(
						"cannot check: all %d work-item id lookup(s) failed, so no hook could be assessed. The 'mg' on PATH may be a different program of the same name, or its workspace may be unreachable. This is NOT a report that the hooks are current", idsTried))
				} else if checked == 0 {
					pass("memory index staleness", "no MEMORY.md indexes found")
				} else if len(staleHooks) == 0 {
					pass("memory index staleness", fmt.Sprintf(
						"no index hook in %d index(es) asserts an open state about a done/archived/shelved work item (%d of %d work-item id(s) resolved)",
						checked, idsResolved, idsTried))
				} else {
					for _, h := range staleHooks {
						text := h.Text
						if len(text) > 160 {
							text = text[:160] + "…"
						}
						warn("memory index staleness", fmt.Sprintf(
							"%s:%d asserts %q but %s — the hook reads as current and is not. REWRITE it to record the outcome rather than deleting it; the note it points at may still be worth recalling. Line: %s",
							h.IndexPath, h.Line, h.Assertion, strings.Join(h.Items, ", "), text))
					}
				}
			}

			// Output
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"checks":  checks,
					"healthy": !hasFail,
				})
			} else {
				for _, c := range checks {
					var icon string
					switch c.Status {
					case "pass":
						icon = "✓"
					case "fail":
						icon = "✗"
					case "warn":
						icon = "!"
					}
					if c.Detail != "" {
						fmt.Printf("  %s  %-20s  %s\n", icon, c.Name, c.Detail)
					} else {
						fmt.Printf("  %s  %s\n", icon, c.Name)
					}
				}
				if hasFail {
					fmt.Println("\nSome checks failed.")
					os.Exit(cli.ExitError)
				} else {
					fmt.Println("\nAll critical checks passed.")
				}
			}
		},
	}

	var cmdVersion = &cobra.Command{
		Use:   "version",
		Short: "Print the pogo version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if jsonOutput {
				cli.PrintJSON(map[string]string{
					"version": version.Version,
					"build":   version.Build,
					"commit":  version.Commit,
					"branch":  version.Branch,
				})
			} else {
				fmt.Printf("pogo %s (build=%s)\n", version.Version, version.Build)
			}
		},
	}

	// Project commands
	var cmdProject = &cobra.Command{
		Use:   "project",
		Short: "Manage the project list",
		Long:  `Commands to add, remove, and list registered projects.`,
	}

	var cmdProjectAdd = &cobra.Command{
		Use:   "add <path>",
		Short: "Register a project directory",
		Long: `Register a directory (or its parent git repository) as a pogo project.
The path is resolved to an absolute path and the git root is discovered automatically.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			path := args[0]
			absPath, err := filepath.Abs(path)
			if err != nil {
				cli.ExitWithError(jsonOutput, fmt.Sprintf("invalid path: %v", err), cli.ExitError)
			}
			resp, err := client.Visit(absPath)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if resp == nil {
				cli.ExitWithError(jsonOutput, "no git repository found at or above "+absPath, cli.ExitNotFound)
			}
			if jsonOutput {
				cli.PrintJSON(resp)
			} else {
				fmt.Println(resp.ParentProject.Path)
			}
		},
	}

	var cmdProjectRemove = &cobra.Command{
		Use:   "remove <path>",
		Short: "Unregister a project directory",
		Long:  `Remove a project from pogo's tracked list. The project's files are not deleted.`,
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			path := args[0]
			absPath, err := filepath.Abs(path)
			if err != nil {
				cli.ExitWithError(jsonOutput, fmt.Sprintf("invalid path: %v", err), cli.ExitError)
			}
			if err := client.RemoveProject(absPath); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"removed": true,
					"path":    absPath,
				})
			} else {
				fmt.Printf("Removed %s\n", absPath)
			}
		},
	}

	var cmdProjectList = &cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		Long:  `Show all projects that pogo is currently tracking.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			projs, err := client.GetProjects()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(projs)
			} else {
				if len(projs) == 0 {
					fmt.Println("No projects registered.")
					return
				}
				for _, p := range projs {
					fmt.Println(p.Path)
				}
			}
		},
	}

	var gcRepo string
	var gcApply bool
	var gcForce bool
	var gcListPreserved bool
	var cmdGC = &cobra.Command{
		Use:   "gc",
		Short: "Garbage-collect stale polecat branches and leaked worktrees",
		Long: `gc deletes stale polecat-* branches and reclaims leaked git worktrees
whose work items have concluded (done or archived). It also removes orphaned
polecat directories under ~/.pogo/polecats — dirs left behind with files but
no .git when a polecat's exit teardown never ran (e.g. pogod died mid-polecat,
gh #31). The submit-time worktree unlink that used to strand these was removed
(gh #88), so these are now legacy leftovers rather than a still-active leak.

It is the manual entry point to the same internal/gitgc logic pogod runs
on startup and on a periodic ticker. Branches and worktrees of in-flight
work items, of currently-running polecats, and anything that cannot be
positively classified are always kept.

A worktree is classified by the work item of the polecat that OWNS it — the
directory's name — not by whatever branch happens to be checked out inside it;
a branch is classified by its own name. So a dead polecat's tree is reclaimed
even while it sits on someone else's unconcluded branch, and that branch is
merely un-checked-out, not deleted. When a directory name resolves to no work
item at all (a legacy or hand-made worktree), the checked-out branch decides
instead and the report says so.

Branch deletion is the only step in the polecat lifecycle that destroys
commits, so a concluded work item is never the whole reason for one. A done
item's branch must be merged into the target branch. An ARCHIVED item's branch
must be durable: some ref under refs/remotes/origin/ holds its head, or every
commit on it has a patch-equivalent already on the integration branch (which is
how a rebase-merged branch reads — the refinery rebases before merging, so a
branch whose work landed perfectly is not an ancestor of main afterwards). A
branch that passes neither, or that git could not answer for, is KEPT and named
in the report under "branches kept holding commits that may exist nowhere else"
— archiving an item concludes the work, it does not publish the commits.

"Currently-running" is read from pogod's registry unioned with the persisted
polecat witness, so a polecat that outlived the pogod that spawned it is still
protected — pogod's registry forgets those on restart, and the witness is what
remembers them. If that witness is on disk but unreadable, gc refuses to sweep
rather than treat it as an empty fleet.

A worktree holding uncommitted work is KEPT and reported, even when its work
item has concluded — a concluded ticket means the work was accepted, not that
the tree is empty, and uncommitted files are unmerged by definition (mg-ee02).
Pass --force to reclaim those too; it DISCARDS the uncommitted work, so rescue
anything you want first. A kept worktree keeps its branch checked out, so that
branch is not deletable until the worktree goes.

A worktree whose "git status" cannot be READ AT ALL — a damaged .git, a bad
permission — is likewise kept, and the report says the status could not be read
rather than naming the ticket state. gc will not destroy files it could not
look at, under any circumstances but an explicit --force: status fails precisely
when the working files are least reproducible.

Such a worktree is never reclaimed automatically, however long it sits — a
30-day-old uncommitted file is exactly as unrecoverable as a 30-second-old one.
The report says how long each kept tree has been untouched so the decision is
yours to make, and cheap to make. That is the whole use gc makes of a file
timestamp: it informs you, and it decides nothing.

By default gc only reports what it would do; pass --apply to make changes.

--list-preserved does none of that. It LISTS the worktrees currently being
retained — the population the rules above create and nothing consumes — across
every repository at once, since preserved trees accumulate wherever the fleet
works and a repo-scoped listing would report a fraction of it while looking
complete. Pass --repo explicitly to narrow it to one repository.

The listing reports facts and no verdict: whose tree it is, which branch it
pins, its work item and that item's state, how long it has gone untouched, and
every uncommitted path in it split into modified and UNTRACKED — the second
being the half that exists on no branch, in no stash and on no remote. It says
nothing about whether a tree may be reclaimed, because that needs someone to
read the files. It also says, per repository, exactly which trees a single
"pogo gc --repo=... --apply --force" would take: that command is repo-scoped and
forced, so it acts on every eligible retained tree at once and not on the one
you inspected, and it does NOT touch a tree whose work item is still in flight.

--list-preserved never changes anything, so --apply and --force do not apply
to it.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			repo, err := filepath.Abs(gcRepo)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if gcListPreserved {
				// --repo defaults to "." and would otherwise silently narrow a
				// listing whose whole value is that it spans repositories.
				// Only an EXPLICIT --repo filters.
				filter := ""
				if cmd.Flags().Changed("repo") {
					filter = repo
				}
				runGCListPreserved(jsonOutput, filter)
				return
			}
			// Exclude live polecats from the sweep.
			live, notes, lerr := gcLivePolecats()
			if lerr != nil {
				cli.ExitWithError(jsonOutput, fmt.Sprintf(
					"cannot read the polecat witness: %v\n"+
						"refusing to sweep — an unreadable witness is not an empty fleet, and a "+
						"worktree removal has no merge gate to catch the mistake (mg-1403).", lerr),
					cli.ExitError)
			}
			if !jsonOutput && len(notes) > 0 {
				for _, n := range notes {
					fmt.Println(n)
				}
				fmt.Println()
			}
			// Best-effort: without a resolvable home dir the orphan-dir
			// scan is skipped and gc still sweeps branches and worktrees.
			polecatsDir, _ := gitgc.DefaultPolecatsDir()
			res, err := gitgc.Sweep(gitgc.Options{
				Repo:         repo,
				LivePolecats: live,
				DryRun:       !gcApply,
				PolecatsDir:  polecatsDir,
				Force:        gcForce,
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(res)
				return
			}
			fmt.Print(res.Summary())
			if !gcApply {
				fmt.Println("(dry run — re-run with --apply to delete)")
			}
		},
	}
	cmdGC.Flags().StringVar(&gcRepo, "repo", ".", "git repository to garbage-collect")
	cmdGC.Flags().BoolVar(&gcApply, "apply", false, "actually delete (default: dry run)")
	cmdGC.Flags().BoolVar(&gcForce, "force", false,
		"also reclaim worktrees holding uncommitted work (DISCARDS that work)")
	cmdGC.Flags().BoolVar(&gcListPreserved, "list-preserved", false,
		"list the retained worktrees across all repos and what is in them; change nothing")

	var rootCmd = &cobra.Command{
		Use:     "pogo",
		Version: version.Version,
		Short:   "Agent-shaped work as UNIX processes",
		Long: `pogo — a daemon for agent-shaped work.

The mayor (the coordinator) dispatches work items to polecats (disposable
worker agents); the refinery (the merge queue) gates and merges their
branches; work items and mail live in mg/macguffin (the task-store CLI).`,
	}

	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.AddCommand(cmdGC)
	rootCmd.AddCommand(cmdVersion)
	rootCmd.AddCommand(cmdInit)
	rootCmd.AddCommand(cmdInstall)
	rootCmd.AddCommand(cmdVisit)
	cmdStatus.Flags().BoolVar(&statusLive, "live", false, "Continuously refresh the dashboard (like watch)")
	cmdStatus.Flags().DurationVar(&statusInterval, "interval", 2*time.Second, "Refresh interval for --live mode (must be > 0)")
	cmdStatus.Flags().StringVar(&statusTag, "tag", "", "Filter work items by tag")
	cmdStatus.Flags().StringVar(&statusAssignee, "assignee", "", "Filter work items by assignee, exact and case-insensitive ('human' for your own queue, 'none' for unassigned). Agents and refinery are never filtered.")
	rootCmd.AddCommand(cmdStatus)
	cmdDoctor.Flags().BoolVar(&doctorCheck, "check", false, "Run quick health checks without starting the doctor agent")
	rootCmd.AddCommand(cmdDoctor)
	// `pogo credential expiry` (mg-7024): the on-demand read of the harness
	// credential's refresh-grant expiry. Read-only, and the way a human confirms
	// a `/login` actually landed during the ~1h window before running sessions
	// pick up the new grant.
	rootCmd.AddCommand(newCredentialCmd(&jsonOutput))
	rootCmd.AddCommand(cmdCheckTeardown)
	rootCmd.AddCommand(cmdCheckIntake)
	rootCmd.AddCommand(cmdCheckAcks)
	rootCmd.AddCommand(cmdCheckMailLoops)
	// check-strandedmail (mg-aa96): the residue a repointed mail-check leaves —
	// mail already delivered to the mailbox nobody polls any more.
	rootCmd.AddCommand(newCheckStrandedMailCmd(&jsonOutput))
	// check-verdicts (mg-f5dd): the ninth sibling — work that landed while the
	// party who filed it was never told how it came out. Ported from macguffin's
	// verdictwatch.py, which was confirmed correct and then sat unrun in a
	// research directory, where nothing has a runner by construction.
	rootCmd.AddCommand(newCheckVerdictsCmd(&jsonOutput))
	// check-review-decl (mg-253e): the tenth sibling — a review ticket filed
	// without the `reviews:` line mg-aaf6 introduced, whose builder is therefore
	// as reapable mid-review as it was before that guard existed. The guard's own
	// residual: it is enforced by an instruction, and an unfollowed instruction
	// emits nothing.
	rootCmd.AddCommand(newCheckReviewDeclCmd(&jsonOutput))
	rootCmd.AddCommand(cmdCheckCommitBody)
	rootCmd.AddCommand(newCheckPromptsCmd(&jsonOutput))
	rootCmd.AddCommand(newCheckStalenessCmd(&jsonOutput))
	rootCmd.AddCommand(newCheckOrphansCmd(&jsonOutput))
	// turn-done / check-turns (mg-a270): the fleet's turn-completion artifact
	// and its reader. The writer is a command AGENTS run at the end of their own
	// turns — the first liveness evidence on this machine that pogod does not
	// produce, and the only kind whose silence means what it appears to mean.
	rootCmd.AddCommand(newTurnDoneCmd(&jsonOutput))
	rootCmd.AddCommand(newCheckTurnsCmd(&jsonOutput))
	// check-stranded (mg-be37): the PERIODIC half of the spawn-time stranded-work
	// guard. That guard only fires when somebody dispatches; this one asks the
	// question of every open item on a clock, and reports the merged-but-open row
	// the guard cannot see at all.
	rootCmd.AddCommand(newCheckStrandedCmd(&jsonOutput))
	// investigations (mg-22c7): search docs/investigations/ by file CONTENTS.
	// Not a check-* detector — it answers a question a person or agent asks,
	// and it is the only pogo subcommand that records its own invocation,
	// because whether anyone asks is the measurement it exists to take.
	rootCmd.AddCommand(newInvestigationsCmd(&jsonOutput))
	rootCmd.AddCommand(newHostCmd(&jsonOutput))
	cmdServer.AddCommand(cmdServerStart)
	cmdServer.AddCommand(cmdServerStop)
	cmdServer.AddCommand(cmdServerStatus)
	rootCmd.AddCommand(cmdServer)
	cmdService.AddCommand(cmdServiceInstall)
	cmdService.AddCommand(cmdServiceUninstall)
	cmdService.AddCommand(cmdServiceStatus)
	cmdService.AddCommand(cmdServiceInstallRecovery)
	cmdService.AddCommand(cmdServiceUninstallRecovery)
	cmdService.AddCommand(cmdServiceInstallDeploy)
	cmdService.AddCommand(cmdServiceUninstallDeploy)
	cmdService.AddCommand(cmdServiceInstallReclaim)
	cmdService.AddCommand(cmdServiceUninstallReclaim)
	cmdService.AddCommand(cmdServiceReconcile)
	cmdService.AddCommand(cmdServiceCheckDrift)
	cmdService.AddCommand(cmdServiceVerifyRevision)
	rootCmd.AddCommand(cmdService)

	// Recovery commands (mg-f5fc tier-3). The agent itself is installed via
	// `pogo service install-recovery`; this command is the polecat-facing
	// entry point that drops a request into the queue.
	var cmdRecovery = &cobra.Command{
		Use:   "recovery",
		Short: "Tier-3 recovery: enqueue a controlled pogod restart",
	}

	var recoveryRequestReason string
	var recoveryRequestRequester string
	var cmdRecoveryRequest = &cobra.Command{
		Use:   "request",
		Short: "Enqueue a recovery request (controlled pogod restart)",
		Long: `Drop a *.req file into ~/.pogo/recovery/queue/ so launchd's
com.pogo.recovery agent runs launchctl kickstart -k against pogod.

The write uses the temp-then-rename pattern so launchd never sees a
partial file. Exits 0 once the request is enqueued — does NOT block on
the actual restart. The recovery agent rate-limits to one kickstart per
60s and archives processed requests to ~/.pogo/recovery/processed/.

This RESTARTS pogod; it does NOT redeploy it. The recovery agent runs
kickstart and nothing else — no build, no install — so it relaunches the
binary already on disk and activates ZERO merged commits. If you merged a
pogod change and want it live, this is not the mechanism. Run
'scripts/pogo-self-deploy check' for the running/installed/main drift
report; it is safe from anywhere and never acts.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			requester := recoveryRequestRequester
			if requester == "" {
				requester = os.Getenv("AGENT_NAME")
			}
			path, err := service.EnqueueRecoveryRequest(requester, recoveryRequestReason)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"enqueued":  true,
					"path":      path,
					"requester": requester,
					"reason":    recoveryRequestReason,
				})
			} else {
				fmt.Printf("Recovery request enqueued: %s\n", path)
			}
		},
	}
	cmdRecoveryRequest.Flags().StringVar(&recoveryRequestReason, "reason", "", "Short reason for the recovery request (logged verbatim)")
	cmdRecoveryRequest.Flags().StringVar(&recoveryRequestRequester, "requester", "", "Identity of the requester (defaults to $AGENT_NAME)")
	cmdRecovery.AddCommand(cmdRecoveryRequest)
	rootCmd.AddCommand(cmdRecovery)

	// Agent commands
	cmdAgent.AddCommand(cmdAgentStart)
	cmdAgent.AddCommand(cmdAgentList)
	cmdAgent.AddCommand(cmdAgentRoster)
	cmdAgent.AddCommand(cmdAgentSpawn)
	cmdAgent.AddCommand(cmdAgentSpawnPolecat)
	cmdAgent.AddCommand(cmdAgentStop)
	cmdAgent.AddCommand(cmdAgentPark)
	cmdAgent.AddCommand(cmdAgentWake)
	cmdAgent.AddCommand(cmdAgentStatus)
	cmdAgent.AddCommand(cmdAgentDiagnose)
	cmdAgent.AddCommand(cmdAgentAttach)
	cmdAgent.AddCommand(cmdAgentOutput)
	cmdAgent.AddCommand(cmdAgentWitness)
	cmdAgentPrompt.AddCommand(cmdAgentPromptList)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInit)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInstall)
	cmdAgentPrompt.AddCommand(cmdAgentPromptShow)
	cmdAgentPrompt.AddCommand(cmdAgentPromptCreate)
	cmdAgent.AddCommand(cmdAgentPrompt)
	rootCmd.AddCommand(cmdAgent)
	rootCmd.AddCommand(cmdNudge)
	rootCmd.AddCommand(cmdHook)
	rootCmd.AddCommand(cmdSchedule)

	// Project commands
	cmdProject.AddCommand(cmdProjectAdd)
	cmdProject.AddCommand(cmdProjectRemove)
	cmdProject.AddCommand(cmdProjectList)
	rootCmd.AddCommand(cmdProject)

	// Refinery commands
	var cmdRefinery = &cobra.Command{
		Use:   "refinery",
		Short: "Interact with the merge queue",
	}

	var submitRepo string
	var submitTarget string
	var submitAuthor string
	var submitAutoCreateTarget bool
	var submitDeferDone bool
	var submitPostMergeTag string
	var submitVerdict string
	var submitVerdictFile string
	var cmdRefinerySubmit = &cobra.Command{
		Use:   "submit <branch>",
		Short: "Submit a branch to the merge queue",
		Long: `Submit a branch for the refinery to test and merge.

The refinery will fetch the branch, run quality gates (build.sh/test.sh or
.pogo/refinery.toml), and fast-forward merge to the target ref if they pass.

By default the refinery rejects MRs whose --target ref does not exist on
origin (catches typos like "fam-45" instead of "feat-45"). Pass
--auto-create-target to opt into having the refinery create the target ref
from the repo's default branch when it is missing.

The BRANCH is checked the same way and for the same reason (mg-586d). The
merge worker checks it out as origin/<branch>, so a branch that is not on
origin cannot merge — and it is refused here, while you are still running and
a push costs nothing, rather than accepted with an MR id and failed later in a
component you never ran. Push first, submit second:

  git push origin <branch> && pogo refinery submit <branch> --repo=...

Submit will not push it for you. If the refusal says the head is already held
by another origin ref, that means the work is safe but the name is not on
origin — push it under the name you are submitting.

When the merge lands on the repo's default branch, the moment it succeeds pogod
records the work item done and stops the polecat.

When --target names anything else, the refinery treats the merge as a PR-flow
integration step: the deliverable is a PR from that integration branch to the
default branch, which the author has not opened yet. pogod skips the
auto-done/auto-stop, leaves the work item claimed, and the polecat calls
'mg done' itself once its full flow finishes. This is detected from --target,
not requested — no flag needed. A bounded backstop still reaps and escalates a
deferred polecat that never completes.

--defer-done forces that same deferral for a merge onto the default branch, for
an author that owns post-merge work anyway.

Both of those depend on the SUBMITTER knowing the merge is not the end. A work
item can declare it instead, which is what a release ticket needs — it merges a
version bump to the default branch with no flag, and its tag, artifacts and
verification all come after:

  mg edit <id> --add-tags=post-merge-work

pogod reads that tag at merge time and takes the same lane: the work item stays
claimed, the polecat keeps running and calls 'mg done' itself, and the bounded
backstop still applies. Set it when you FILE the item, not when you submit.

All three of those keep the AUTHOR as the actor and only stop it being killed.
That is right for work only the author can do — opening a PR from its branch,
mailing its own report — and wrong for work that just needs the merged commit,
because the author cannot know that commit until the refinery has written it and
cannot be relied on to still be alive afterwards.

--post-merge-tag moves such a step to the refinery instead of protecting the
author while it does it. The refinery creates the tag on the commit the merge
actually landed as and pushes it, before the author's reap can fire:

  pogo refinery submit polecat-a3f --repo=/path/to/repo --post-merge-tag=v0.8.0

Use it for a release cut. Tagging from the worker's own sequence has to happen
either before the merge (dangling the tag off a SHA the refinery rewrites when
it rebases) or after it (racing the reap) — the refinery is the only actor that
both sees the merged SHA and outlives the worker. If the tag cannot be pushed,
the merge still stands but the work item is NOT marked done and the mayor is
mailed, so a half-finished release can never read as complete.

--verdict / --verdict-file carry YOUR OWN result for the work item through the
merge, so it survives being auto-done'd. On the auto-done path this is the only
moment you can record one: pogod closes the work item the instant your branch
merges and stops you about half a second later, and mg refuses a second
'mg done' rather than overwriting the first — so your own 'mg done --result'
arrives to a closed item and is turned away. Nothing overwrites your verdict;
you are simply beaten to the item, and the protocol calls that refusal success.

  pogo refinery submit polecat-a3f --repo=/path/to/repo \
      --verdict='{"verdict":"pass","summary":"what you concluded","evidence":["file:line"]}'

  ... --verdict-file=verdict.json      # same, read from a file
  ... --verdict-file=-                 # same, read from stdin

It must be a non-empty JSON object and it is rejected here, while you are still
running, if it is not. The refinery does not read the contents: no key is
required, no value is enumerated, and a merge queue is not the right actor to
rule on what you concluded. It is written into the work item's result sidecar
under a "verdict" key, nested rather than flattened so your claims can never
collide with the refinery's measurements of what actually merged.

You do not need this on the deferred paths (--defer-done, a --target that is an
integration branch, or an item tagged post-merge-work): there you call
'mg done --result' yourself and nothing preempts you. Passing it anyway is
harmless — it is recorded on the merge request and readable via
'pogo refinery show <id> --json'.

Example:
  pogo refinery submit polecat-a3f --repo=/path/to/repo`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			branch := args[0]
			if submitRepo == "" {
				cli.ExitWithError(jsonOutput, "--repo is required", cli.ExitError)
			}
			verdict, err := readSubmitVerdict(submitVerdict, submitVerdictFile)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			id, err := client.SubmitMerge(refinery.SubmitRequest{
				RepoPath:            submitRepo,
				Branch:              branch,
				TargetRef:           submitTarget,
				Author:              submitAuthor,
				AutoCreateTargetRef: submitAutoCreateTarget,
				DeferDone:           submitDeferDone,
				PostMergeTag:        submitPostMergeTag,
				Verdict:             verdict,
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"id": id, "branch": branch, "status": "queued"})
			} else {
				fmt.Printf("Submitted %s to merge queue (id=%s)\n", branch, id)
			}
		},
	}
	cmdRefinerySubmit.Flags().StringVar(&submitRepo, "repo", "", "Repository path (required)")
	cmdRefinerySubmit.Flags().StringVar(&submitTarget, "target", "main", "Target ref to merge into")
	cmdRefinerySubmit.Flags().StringVar(&submitAuthor, "author", "", "Author agent name")
	cmdRefinerySubmit.Flags().BoolVar(&submitAutoCreateTarget, "auto-create-target", false, "Create the target ref from the repo's default branch if it doesn't exist (off by default; safer to fail loudly on typos)")
	cmdRefinerySubmit.Flags().BoolVar(&submitDeferDone, "defer-done", false, "Skip pogod's auto-done/auto-stop at merge so the polecat owns its post-merge lifecycle and calls 'mg done' itself (already implied when --target is not the repo's default branch; a bounded backstop reaps a deferred polecat that never completes)")
	cmdRefinerySubmit.Flags().StringVar(&submitVerdict, "verdict", "", "YOUR OWN result for the work item, as a non-empty JSON object, carried through the merge and written into the item's result sidecar under \"verdict\" (mg-dfea). On the auto-done path this is the only moment you can record one — pogod closes the item at merge and mg refuses your later 'mg done --result' as already-done")
	cmdRefinerySubmit.Flags().StringVar(&submitVerdictFile, "verdict-file", "", "Read --verdict from this file, or from stdin when it is \"-\" (avoids shell-quoting a JSON object)")
	cmdRefinerySubmit.Flags().StringVar(&submitPostMergeTag, "post-merge-tag", "", "Have the REFINERY create this git tag on the commit the merge lands as and push it, before the author is reaped (use for release cuts — the refinery is the only actor that both sees the merged SHA and outlives the author; a failure here blocks auto-done and mails the mayor)")

	var cmdRefineryStatus = &cobra.Command{
		Use:   "status",
		Short: "Show refinery summary (enabled, running, queue/history counts)",
		Long: `Print a summary of the refinery state — whether it's enabled and
running, the configured poll interval, and the size of the queue and history.

Use this for a quick health check of the refinery. For per-MR details use
'pogo refinery show <id>', and for full lists use 'pogo refinery queue' or
'pogo refinery history'.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			status, err := client.GetRefineryStatus()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(status)
			} else {
				state := "stopped"
				if status.Running {
					state = "running"
				}
				if !status.Enabled {
					state = "disabled"
				}
				fmt.Printf("Status:  %s\n", state)
				fmt.Printf("Enabled: %t\n", status.Enabled)
				fmt.Printf("Running: %t\n", status.Running)
				fmt.Printf("Poll:    %s\n", status.PollInterval)
				// Queue counts PENDING requests only; the in-flight one is
				// reported on its own line. Printing the count alone made a
				// refinery chewing through a merge and one that had stopped
				// produce identical output (mg-0c51).
				fmt.Printf("Queue:   %d pending\n", status.QueueLen)
				// Merges run in per-repo lanes, so there can be several
				// active at once. Each row names the repo whose lane it
				// holds: an author waiting behind an unrelated repo's gate
				// could not previously see that from any view, which is half
				// of what made the 70-minute stall of 2026-08-05 unreadable
				// (mg-37ad).
				switch {
				case len(status.InFlight) > 0:
					for _, ln := range status.InFlight {
						age := ""
						if !ln.Since.IsZero() {
							age = fmt.Sprintf(", in flight for %s", time.Since(ln.Since).Round(time.Second))
						}
						fmt.Printf("Active:  %s  repo=%s  branch=%s%s\n", ln.ID, ln.Repo, ln.Branch, age)
					}
					if status.MaxConcurrentMerges > 0 {
						fmt.Printf("Lanes:   %d of %d busy (one lane per repo) — 'pogo refinery queue' shows what each gate is doing\n",
							len(status.InFlight), status.MaxConcurrentMerges)
					}
				case status.Processing != "":
					// Older pogod: single slot, no lane detail.
					inFlight := ""
					if !status.ProcessingSince.IsZero() {
						inFlight = fmt.Sprintf(", in flight for %s", time.Since(status.ProcessingSince).Round(time.Second))
					}
					fmt.Printf("Active:  %s%s  — 'pogo refinery queue' shows what its gate is doing\n",
						status.Processing, inFlight)
				case status.QueueLen > 0:
					fmt.Printf("Active:  none — %d pending with nothing being processed\n", status.QueueLen)
				default:
					fmt.Printf("Active:  none\n")
				}
				// History is printed with the retention that bounds it, so the
				// count is never read as a total. When the cap has bitten the
				// line says so outright; when the cap is unknown it says that
				// too rather than implying there is none.
				switch status.HistoryTruncation() {
				case refinery.HistoryTruncationAtCap:
					fmt.Printf("History: %d  (retained: %s — AT CAP, older merges pruned; see 'pogo refinery history --since')\n",
						status.HistoryLen, status.RetentionSummary())
				case refinery.HistoryTruncationUnknown:
					fmt.Printf("History: %d  (retention: %s — truncation UNKNOWN)\n",
						status.HistoryLen, status.RetentionSummary())
				default:
					fmt.Printf("History: %d  (retained: %s)\n", status.HistoryLen, status.RetentionSummary())
				}
			}
		},
	}

	var cmdRefineryQueue = &cobra.Command{
		Use:   "queue",
		Short: "Show the merge pipeline: the request in flight, then the pending ones",
		Long: `Print the refinery pipeline — the merge request being processed right
now, followed by the ones waiting behind it.

The in-flight request is listed with status=processing and, under it, what its
current quality gate is doing: elapsed time, how much output it has produced
and how long ago, and how much CPU its process subtree is consuming.

Those last two are a PAIR and neither alone is an answer. A healthy gate was
observed silent for 8m31s of a 10m run while a descendant burned four cores —
so a stale last_output does not mean stopped. Conversely a subtree consuming no
CPU may simply be blocked on I/O or a lock. Read them together:

  silent + burning CPU   the gate is computing between log statements — wait
  talking                the gate is visibly working — wait
  silent + idle CPU      the shape of a stall — LOOK CLOSER before intervening
  silent + unmeasurable  the view cannot tell; it says so rather than guessing

If nothing is in flight while requests are pending, that is printed outright:
it is the arrangement that used to be indistinguishable from a busy refinery,
because the row that was moving was the one row this command did not list.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			queue, err := client.GetRefineryQueue()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(queue)
				return
			}
			fmt.Print(formatQueue(queue, time.Now()))
		},
	}

	var historySince string
	var cmdRefineryHistory = &cobra.Command{
		Use:   "history",
		Short: "Show completed merge requests with status (retained window; --since reads the durable event log)",
		Long: `Print completed merge requests, oldest first.

By default this reads the refinery's RETAINED history, which is bounded: the
refinery deletes entries past its count/age caps, so the default output is a
window and not an archive. When the cap has bitten, a note saying so is written
to stderr — stdout is unchanged, so pipes are unaffected.

The cap is a COUNT (100 by default), so the window's age moves with merge
volume: at ~20 merges/hour it is under a day. This is why "no failures in
history" is not the same claim as "no failures".

--since <duration|date> answers the wider question from the durable event log
(~/.pogo/events.log and its rotated files) instead, reconstructing one row per
merge request. stdout has the same shape either way, so the same jq pipeline
works with or without the flag. There is deliberately no --all or --limit 0:
history behind the cap is deleted, not hidden, so an unbounded flag over the
retained window would return the same truncated answer while looking like it
had widened it.

If the event log cannot reach back as far as --since asks (records rotated
away), the command prints what it has, says TRUNCATED on stderr, and EXITS
NON-ZERO — so a consumer under 'set -e' fails loudly rather than reading a
short answer as an empty one.

Examples:
  pogo refinery history                       # retained window, cap stated if it bit
  pogo refinery history --since=30d           # from the durable event log
  pogo refinery history --since=2026-07-01    # from a date
  pogo refinery history --since=30d --json | jq -r '.[] | select(.status=="failed") | .branch'`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			printRows := func(rows []refinery.MergeRequest) {
				if jsonOutput {
					cli.PrintJSON(rows)
					return
				}
				if len(rows) == 0 {
					fmt.Println("No merge history.")
					return
				}
				for _, mr := range rows {
					fmt.Println(formatHistoryRow(mr))
				}
			}

			if historySince != "" {
				since, err := parseSince(historySince)
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				logPath, err := events.LogPath()
				if err != nil {
					cli.ExitWithError(jsonOutput, "could not resolve event log path: "+err.Error(), cli.ExitError)
				}
				w, err := refinery.HistoryFromLog(logPath, since)
				if err != nil {
					cli.ExitWithError(jsonOutput, "read event log: "+err.Error(), cli.ExitError)
				}
				printRows(w.Requests)
				fmt.Fprintf(os.Stderr, "refinery history --since=%s: %s\n", historySince, w.CoverageNote())
				if !w.Complete {
					// The result is short for a reason the caller cannot see in
					// stdout. Exiting non-zero is what stops it being read as a
					// complete answer.
					os.Exit(cli.ExitError)
				}
				return
			}

			history, err := client.GetRefineryHistory()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			printRows(history)

			// State the bound whenever it bit — and say so explicitly when the
			// bound cannot be read, because falling silent there is
			// indistinguishable from "not truncated" and is the original
			// defect. Retention is read from the running daemon rather than
			// from a constant here, so the number printed cannot drift from the
			// number enforced.
			status, serr := client.GetRefineryStatus()
			switch {
			case serr != nil:
				fmt.Fprintf(os.Stderr, "refinery history: showing %d retained merge requests; the retention cap could not be read (%v), so whether this window is TRUNCATED is UNKNOWN — use --since=<duration|date> for the durable event log\n",
					len(history), serr)
			case status.HistoryTruncation() == refinery.HistoryTruncationUnknown:
				fmt.Fprintf(os.Stderr, "refinery history: showing %d retained merge requests; this pogod does not report its retention cap (it predates mg-e9ee), so whether this window is TRUNCATED is UNKNOWN — use --since=<duration|date> for the durable event log\n",
					len(history))
			case status.HistoryTruncation() == refinery.HistoryTruncationAtCap:
				fmt.Fprintf(os.Stderr, "refinery history: showing %d of an unknown total — retention is %s and prunes DESTRUCTIVELY, so older merge requests are not here and are not recoverable from the refinery. Use --since=<duration|date> to read the durable event log.\n",
					len(history), status.RetentionSummary())
			}
		},
	}
	cmdRefineryHistory.Flags().StringVar(&historySince, "since", "", "reconstruct from the durable event log instead: duration (720h, 30d) or date (2026-07-01, RFC3339)")

	var cmdRefineryShow = &cobra.Command{
		Use:   "show <mr-id>",
		Short: "Show details for a single merge request",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			mr, err := client.GetRefineryMR(args[0])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(mr)
			} else {
				fmt.Printf("ID:        %s\n", mr.ID)
				fmt.Printf("Branch:    %s\n", mr.Branch)
				fmt.Printf("Target:    %s\n", mr.TargetRef)
				fmt.Printf("Author:    %s\n", mr.Author)
				fmt.Printf("Status:    %s\n", mr.StatusLabel())
				if note := mr.FailureClass.TriageNote(); note != "" && mr.Status == refinery.StatusFailed {
					fmt.Printf("           %s\n", note)
				}
				if mr.PRFlow {
					fmt.Printf("PR flow:   yes — %s is an integration branch, not the repo default.\n", mr.TargetRef)
					fmt.Printf("           Merging is an integration step, not completion: the author still\n")
					fmt.Printf("           has to open the PR and call 'mg done' itself.\n")
				}
				if mr.AlreadyMerged {
					fmt.Printf("Note:      branch had already landed on the target — resolved as merged without re-running gates\n")
				}
				if mr.MergedSHA != "" {
					fmt.Printf("Merged as: %s\n", mr.MergedSHA)
				}
				// Print the post-merge step in both directions (mg-6879). A
				// declared-but-failed step is the one case where Status reads
				// "merged" and the deliverable does not exist, so it must be
				// visible next to the status rather than inferable from its
				// absence.
				if mr.PostMergeTag != "" {
					switch {
					case mr.PostMergeError != "":
						fmt.Printf("Tag:       %s — NOT CREATED: %s\n", mr.PostMergeTag, mr.PostMergeError)
						fmt.Printf("           The merge landed; its deliverable did not. The work item is\n")
						fmt.Printf("           deliberately NOT done — do not archive it on 'merged' alone.\n")
					case mr.Status == refinery.StatusMerged:
						fmt.Printf("Tag:       %s (created by the refinery on %s and pushed to origin)\n", mr.PostMergeTag, mr.MergedSHA)
					default:
						fmt.Printf("Tag:       %s (declared; the refinery creates it on the merged commit)\n", mr.PostMergeTag)
					}
				} else if mr.PostMergeError != "" {
					fmt.Printf("Post-merge: FAILED — %s\n", mr.PostMergeError)
				}
				fmt.Printf("Repo:      %s\n", mr.RepoPath)
				fmt.Printf("Submitted: %s\n", refineryTimeSecond(mr.SubmitTime))
				if !mr.StartTime.IsZero() {
					fmt.Printf("Started:   %s\n", refineryTimeSecond(mr.StartTime))
				}
				if !mr.DoneTime.IsZero() {
					fmt.Printf("Done:      %s\n", refineryTimeSecond(mr.DoneTime))
				}
				// A queued MR says where it is in line. "Queued for 30
				// minutes" alone reads as ignored; "waiting, 1 ahead, and
				// that one is mid-gate" reads as a serialized queue working
				// normally — which is what it was (mg-0c51).
				if mr.Status == refinery.StatusQueued {
					fmt.Print(formatQueuePosition(mr.ID, time.Now()))
				}
				if mr.Error != "" {
					fmt.Printf("Error:     %s\n", mr.Error)
				}
				// The attempt block is what separates "failed once" from "failed
				// after 3 attempts", and it prints the TRANSPORT and the RAW error
				// of every failing attempt rather than one normalised summary
				// (mg-e5c2).
				fmt.Print(formatMRAttempts(mr))
				// The progress block answers "is this gate slow or dead?" —
				// printed before the gate output, since it is what an operator
				// is looking for while the MR is still in flight.
				fmt.Print(formatMRProgress(mr.Progress, time.Now()))
				if mr.GateOutput != "" {
					fmt.Printf("\n--- Gate Output ---\n%s\n", mr.GateOutput)
				}
			}
		},
	}

	var cmdRefineryPrune = &cobra.Command{
		Use:   "prune",
		Short: "Prune merged branches from refinery worktrees",
		Long: `Clean up branches in ~/.pogo/refinery/worktrees/ that have been
merged to main. Also prunes stale remote-tracking references.

This reclaims disk space and keeps the refinery worktree clones tidy.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			results, err := client.PruneWorktrees()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(results)
			} else {
				total := 0
				for _, r := range results {
					if r.Error != "" {
						fmt.Printf("%s: error: %s\n", r.Repo, r.Error)
						continue
					}
					if len(r.PrunedBranches) > 0 {
						fmt.Printf("%s: pruned %d branches: %s\n", r.Repo, len(r.PrunedBranches),
							strings.Join(r.PrunedBranches, ", "))
						total += len(r.PrunedBranches)
					}
				}
				if total == 0 {
					fmt.Println("No merged branches to prune.")
				} else {
					fmt.Printf("Pruned %d merged branches total.\n", total)
				}
			}
		},
	}

	var cmdRefineryCancel = &cobra.Command{
		Use:   "cancel <mr-id>",
		Short: "Cancel a queued merge request, or stop a processing one",
		Long: `Stop a merge request.

A QUEUED merge request is removed from the queue and resolved as cancelled
immediately.

A PROCESSING merge request has its running quality gate killed; the pipeline
then stops at the next step boundary. That is a request, not a result: if the
merge had already pushed to the target it has landed and still resolves as
merged. Poll 'pogo refinery show <id>' for the outcome — the printed status
says which of the two happened.

An already-finished merge request (merged, failed, cancelled) cannot be
cancelled.

Example:
  pogo refinery cancel mr-abc123`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			id := args[0]
			resp, err := client.CancelMerge(id)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(resp)
			} else if resp.Outcome == refinery.CancelRequestedInFlight {
				fmt.Printf("Cancel requested for merge request %s\n", id)
				fmt.Printf("  %s\n", resp.Note)
			} else {
				fmt.Printf("Cancelled merge request %s\n", id)
			}
		},
	}

	cmdRefinery.AddCommand(cmdRefinerySubmit)
	cmdRefinery.AddCommand(cmdRefineryStatus)
	cmdRefinery.AddCommand(cmdRefineryQueue)
	cmdRefinery.AddCommand(cmdRefineryHistory)
	cmdRefinery.AddCommand(cmdRefineryShow)
	cmdRefinery.AddCommand(cmdRefineryPrune)
	cmdRefinery.AddCommand(cmdRefineryCancel)
	rootCmd.AddCommand(cmdRefinery)

	// Cross-repo operations
	var cmdDeps = &cobra.Command{
		Use:   "deps",
		Short: "Show dependency graph across indexed repos",
		Long: `Analyze Go module paths and import statements across all indexed
repos to build a dependency graph showing which repos depend on which.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			graph, err := client.BuildDepGraph()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(graph)
				return
			}
			if len(graph.Nodes) == 0 {
				fmt.Println("No repos indexed.")
				return
			}
			fmt.Println("=== Repos ===")
			for _, n := range graph.Nodes {
				if n.ModulePath != "" {
					fmt.Printf("  %s  (%s)\n", n.Repo, n.ModulePath)
				} else {
					fmt.Printf("  %s\n", n.Repo)
				}
			}
			fmt.Println()
			if len(graph.Edges) == 0 {
				fmt.Println("No cross-repo dependencies found.")
				return
			}
			fmt.Println("=== Dependencies ===")
			for _, e := range graph.Edges {
				fmt.Printf("  %s → %s  (via %s)\n", e.From, e.To, e.ImportPath)
			}
			fmt.Printf("\n%d repos, %d dependencies\n", len(graph.Nodes), len(graph.Edges))
		},
	}

	var cmdRefs = &cobra.Command{
		Use:   "refs <symbol>",
		Short: "Find cross-repo references to a symbol",
		Long: `Search for a symbol across all indexed repos and classify matches
as definitions, imports, or call sites.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			total := 0
			first := true
			err := client.FindReferences(args[0], func(rr *xref.RepoRefs) {
				if jsonOutput {
					data, merr := json.Marshal(rr)
					if merr != nil {
						return
					}
					fmt.Println(string(data))
					return
				}
				if !first {
					fmt.Println()
				}
				first = false
				fmt.Printf("=== %s ===\n", rr.Repo)
				if rr.Error != "" {
					fmt.Printf("  error: %s\n", rr.Error)
					return
				}
				byKind := map[xref.RefKind][]xref.Reference{}
				for _, ref := range rr.Refs {
					byKind[ref.Kind] = append(byKind[ref.Kind], ref)
				}
				kindOrder := []xref.RefKind{xref.RefDefinition, xref.RefImport, xref.RefCall}
				for _, kind := range kindOrder {
					refs := byKind[kind]
					if len(refs) == 0 {
						continue
					}
					fmt.Printf("  [%s]\n", kind)
					for _, ref := range refs {
						fmt.Printf("    %s:%d\t%s\n", ref.File, ref.Line, ref.Content)
					}
					total += len(refs)
				}
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if !jsonOutput {
				fmt.Printf("\n%d references found across repos\n", total)
			}
		},
	}
	rootCmd.AddCommand(cmdDeps)
	rootCmd.AddCommand(cmdRefs)

	// Events commands
	var cmdEvents = &cobra.Command{
		Use:   "events",
		Short: "Append-only event log at ~/.pogo/events.log",
	}

	var (
		emitType       string
		emitAgent      string
		emitWorkItemID string
		emitRepo       string
		emitDetails    string
	)
	var cmdEventsEmit = &cobra.Command{
		Use:   "emit",
		Short: "Emit one event to ~/.pogo/events.log",
		Long: `Append a single event to ~/.pogo/events.log per the schema in docs/event-log.md.

Designed as a shell-out bridge for processes that don't link the Go events
package directly (e.g. mg). Best-effort: failures are logged to stderr but
the command always exits 0 so callers never block on emission.

Example:
  pogo events emit --type=work_item_claimed --work-item-id=mg-0241 \
      --details='{"title":"F1: design event log","tags":["pogo","phase-f"]}'`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if emitType == "" {
				cli.ExitWithError(jsonOutput, "--type is required", cli.ExitError)
			}
			ev := events.Event{
				EventType:  emitType,
				Agent:      emitAgent,
				WorkItemID: emitWorkItemID,
				Repo:       emitRepo,
			}
			if ev.Agent == "" {
				ev.Agent = events.ResolveAgent(agent.CoordinatorName())
			}
			if emitDetails != "" {
				if err := json.Unmarshal([]byte(emitDetails), &ev.Details); err != nil {
					fmt.Fprintf(os.Stderr, "events: --details is not valid JSON: %v\n", err)
					return
				}
			}
			events.Emit(context.Background(), ev)
		},
	}
	cmdEventsEmit.Flags().StringVar(&emitType, "type", "", "event_type (required, e.g. work_item_claimed)")
	cmdEventsEmit.Flags().StringVar(&emitAgent, "agent", "", "agent identity (default: derived from POGO_AGENT_NAME/TYPE, else \"human\")")
	cmdEventsEmit.Flags().StringVar(&emitWorkItemID, "work-item-id", "", "macguffin work item ID (optional)")
	cmdEventsEmit.Flags().StringVar(&emitRepo, "repo", "", "repository path (optional)")
	cmdEventsEmit.Flags().StringVar(&emitDetails, "details", "", "details payload as JSON object (optional)")
	cmdEvents.AddCommand(cmdEventsEmit)

	var (
		listSince string
		listType  string
		listAgent string
		listFile  string
	)
	var cmdEventsList = &cobra.Command{
		Use:   "list",
		Short: "List events from ~/.pogo/events.log",
		Long: `Print events from the log, optionally filtered by age, type, and agent.

By default prints a pretty one-line-per-event view (timestamp, event_type,
agent, work_item_id, repo, summarized details). With --json each matching
event is dumped as raw JSONL on stdout for piping into jq, etc.

Examples:
  pogo events list --since=1h
  pogo events list --since=24h --type=refinery_merged
  pogo events list --since=30m --agent=mayor --json | jq .`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			path := listFile
			if path == "" {
				p, err := events.LogPath()
				if err != nil {
					cli.ExitWithError(jsonOutput, "could not resolve log path: "+err.Error(), cli.ExitError)
				}
				path = p
			}

			filter := events.Filter{Type: listType, Agent: listAgent}
			if listSince != "" {
				d, err := time.ParseDuration(listSince)
				if err != nil {
					cli.ExitWithError(jsonOutput, fmt.Sprintf("--since: invalid duration %q: %v", listSince, err), cli.ExitError)
				}
				if d <= 0 {
					cli.ExitWithError(jsonOutput, "--since must be a positive duration (e.g. 1h, 30m)", cli.ExitError)
				}
				filter.SinceMin = time.Now().Add(-d)
			}

			matches, err := events.ReadFiltered(path, filter)
			if err != nil {
				cli.ExitWithError(jsonOutput, "read log: "+err.Error(), cli.ExitError)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				for _, ev := range matches {
					if err := enc.Encode(ev); err != nil {
						cli.ExitWithError(false, "encode: "+err.Error(), cli.ExitError)
					}
				}
				return
			}
			for _, ev := range matches {
				fmt.Println(events.FormatPretty(ev))
			}
		},
	}
	cmdEventsList.Flags().StringVar(&listSince, "since", "", "only show events newer than duration (e.g. 1h, 30m, 24h)")
	cmdEventsList.Flags().StringVar(&listType, "type", "", "filter by event_type (exact match)")
	cmdEventsList.Flags().StringVar(&listAgent, "agent", "", "filter by agent identity (exact match)")
	cmdEventsList.Flags().StringVar(&listFile, "file", "", "log file path (default: ~/.pogo/events.log)")
	cmdEvents.AddCommand(cmdEventsList)

	var (
		tailFile     string
		tailInterval time.Duration
	)
	var cmdEventsTail = &cobra.Command{
		Use:   "tail",
		Short: "Stream new events from ~/.pogo/events.log (like tail -f)",
		Long: `Follow the event log: prints each new line as it's appended. Starts at
the current end of file, so it only shows events written from now on.

Use Ctrl-C to stop. Pretty-printed by default; --json passes through the raw
JSONL line so the output is machine-parseable.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			path := tailFile
			if path == "" {
				p, err := events.LogPath()
				if err != nil {
					cli.ExitWithError(jsonOutput, "could not resolve log path: "+err.Error(), cli.ExitError)
				}
				path = p
			}

			stop := make(chan struct{})
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sig
				close(stop)
			}()

			err := events.Follow(path, tailInterval, stop, func(line []byte) {
				if jsonOutput {
					os.Stdout.Write(line)
					os.Stdout.Write([]byte{'\n'})
					return
				}
				ev, perr := events.ParseLine(line)
				if perr != nil {
					fmt.Fprintf(os.Stderr, "events: skipping malformed line: %v\n", perr)
					return
				}
				fmt.Println(events.FormatPretty(ev))
			})
			if err != nil {
				cli.ExitWithError(jsonOutput, "tail: "+err.Error(), cli.ExitError)
			}
		},
	}
	cmdEventsTail.Flags().StringVar(&tailFile, "file", "", "log file path (default: ~/.pogo/events.log)")
	cmdEventsTail.Flags().DurationVar(&tailInterval, "poll-interval", 200*time.Millisecond, "how often to poll for new lines")
	cmdEvents.AddCommand(cmdEventsTail)

	rootCmd.AddCommand(cmdEvents)

	completion.AddCommand(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
