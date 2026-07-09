////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/completion"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/version"
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

func main() {

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
		Short: "Start the pogo server",
		Long:  `Start the pogo server.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			err := client.HealthCheck()
			if err != nil {
				if jsonOutput {
					// Not yet running, start it
				} else {
					fmt.Println("Starting pogo server...")
				}
				err = client.StartServer()
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"status":  "started",
						"message": "pogo server started",
					})
				} else {
					fmt.Println("pogo server started")
				}
				return
			}

			// Server is running — check if orchestration is stopped
			mode, err := client.GetServerMode()
			if err == nil && mode == "index-only" {
				if !jsonOutput {
					fmt.Println("Restarting orchestration...")
				}
				if err := client.StartOrchestration(); err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"status":  "started",
						"message": "orchestration restarted",
					})
				} else {
					fmt.Println("Orchestration restarted")
				}
				return
			}

			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"status":  "running",
					"message": "the server is already running",
				})
			} else {
				fmt.Println("The server is already running")
			}
		},
	}

	var stopAll bool
	var cmdServerStop = &cobra.Command{
		Use:   "stop",
		Short: "Stop orchestration (agents + refinery); use --all for full teardown",
		Long: `By default, stops orchestration (agents and refinery) while keeping
the pogo server running for indexing and search. Use --all to fully
shut down the server process.`,
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
				err := client.StopOrchestration()
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				if jsonOutput {
					cli.PrintJSON(map[string]interface{}{
						"status":  "index-only",
						"message": "orchestration stopped, server still running",
					})
				} else {
					fmt.Println("Orchestration stopped. Server still running (indexing + search).")
					fmt.Println("Use --all to fully shut down the server.")
				}
			}
		},
	}
	cmdServerStop.Flags().BoolVar(&stopAll, "all", false, "fully shut down the server process")

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
			fmt.Printf("refinery: %s  (queue=%d, history=%d, recent_failures=%d)\n",
				refState, report.Refinery.QueueLength, report.Refinery.HistoryLength, report.Refinery.RecentFailures)
		},
	}

	var statusLive bool
	var statusInterval time.Duration
	var statusTag string

	// renderStatus fetches the current dashboard state and returns it as a
	// fully-formatted text frame. In JSON mode it prints directly and returns
	// "". The whole frame is built into a buffer before returning so live mode
	// can write it to the terminal in a single flicker-free update.
	renderStatus := func() string {
		type statusReport struct {
			Agents    []agent.AgentInfo       `json:"agents"`
			WorkItems string                  `json:"work_items,omitempty"`
			Refinery  *refinery.Status        `json:"refinery,omitempty"`
			Queue     []refinery.MergeRequest `json:"refinery_queue,omitempty"`
		}

		var report statusReport

		// Agents
		agents, agentErr := client.ListAgents()
		if agentErr == nil {
			report.Agents = agents
		}

		// Work items via mg list
		mgArgs := []string{"list"}
		if statusTag != "" {
			mgArgs = append(mgArgs, "--tag", statusTag)
		}
		mgOut, mgErr := exec.Command("mg", mgArgs...).CombinedOutput()
		if mgErr == nil {
			report.WorkItems = strings.TrimSpace(string(mgOut))
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

		// Agents section
		fmt.Fprintln(&b, "=== Agents ===")
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
		fmt.Fprintln(&b, "=== Work Items ===")
		if mgErr != nil {
			fmt.Fprintln(&b, "  (unavailable: mg not found)")
		} else if report.WorkItems == "" {
			fmt.Fprintln(&b, "  No work items.")
		} else {
			for _, line := range strings.Split(report.WorkItems, "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		fmt.Fprintln(&b)

		// Refinery section
		fmt.Fprintln(&b, "=== Refinery ===")
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
			fmt.Fprintf(&b, "  Status: %s  |  Queue: %d  |  History: %d  |  Poll: %s\n",
				state, refStatus.QueueLen, refStatus.HistoryLen, refStatus.PollInterval)
		}
		if queueErr == nil && len(queue) > 0 {
			fmt.Fprintln(&b)
			for _, mr := range queue {
				age := time.Since(mr.SubmitTime).Truncate(time.Second)
				author := mr.Author
				if author == "" {
					author = "-"
				}
				fmt.Fprintf(&b, "  %-8s  %-20s  branch=%-30s  author=%-15s  age=%s\n",
					mr.Status, mr.ID, mr.Branch, author, age)
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

	var cmdServiceStatus = &cobra.Command{
		Use:   "status",
		Short: "Check if the pogo system service is installed",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			installed, path := service.Status()
			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"installed": installed,
					"path":      path,
				})
			} else {
				if installed {
					fmt.Printf("Service installed: %s\n", path)
				} else {
					fmt.Println("Service not installed.")
				}
			}
		},
	}

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
		Short: "List running agents",
		Args:  cobra.NoArgs,
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
			}
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
		Short: "Stop a running agent",
		Args:  cobra.ExactArgs(1),
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
'pogo agent wake <name>'.`,
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
Detach with Ctrl-\ to leave the agent running and restore your terminal.`,
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
		},
	}

	var outputPlain bool
	var cmdAgentOutput = &cobra.Command{
		Use:   "output <name>",
		Short: "Show recent output from an agent",
		Long: `Show recent output from an agent's PTY buffer.

Use --plain to strip ANSI/VT escape sequences for human-readable or machine-parseable output.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			output, err := client.GetAgentOutput(args[0], outputPlain)
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
  exited       — process has exited
  dead         — registered as running but OS process is gone

A cron-driven agent (e.g. a */30 mail-check) is idle by design between firings.
While it is within one cron interval of its last scheduled firing it reports
"idle", not "stalled", even past the threshold — see cron_covered in --json.`,
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
				}
			}
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
	var spawnPolecatId string
	var spawnPolecatRepo string
	var spawnPolecatBranch string
	var spawnPolecatEnv []string
	var spawnPolecatProvider string
	var spawnPolecatNoWorktree bool
	var cmdAgentSpawnPolecat = &cobra.Command{
		Use:   "spawn-polecat <name>",
		Short: "Spawn a polecat from a prompt template",
		Long: `Spawn an ephemeral polecat (a disposable worker agent) using a prompt template from ~/.pogo/agents/templates/.
The template is expanded with the provided variables and used as the agent's prompt file.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			info, err := client.SpawnPolecat(agent.SpawnPolecatAPIRequest{
				Name:       args[0],
				Template:   spawnPolecatTemplate,
				Task:       spawnPolecatTask,
				Body:       spawnPolecatBody,
				Id:         spawnPolecatId,
				Repo:       spawnPolecatRepo,
				Branch:     spawnPolecatBranch,
				Env:        spawnPolecatEnv,
				Provider:   spawnPolecatProvider,
				NoWorktree: spawnPolecatNoWorktree,
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
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatTemplate, "template", "polecat", "Template name (from ~/.pogo/agents/templates/)")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatTask, "task", "", "Work item title ({{.Task}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatBody, "body", "", "Work item body ({{.Body}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatId, "id", "", "Work item ID ({{.Id}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatRepo, "repo", "", "Target repository path ({{.Repo}})")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatBranch, "branch", "", "Target branch for refinery submit ({{.Branch}})")
	cmdAgentSpawnPolecat.Flags().StringSliceVarP(&spawnPolecatEnv, "env", "e", nil, "Additional environment variables (KEY=VALUE)")
	cmdAgentSpawnPolecat.Flags().StringVar(&spawnPolecatProvider, "provider", "", "Harness provider for this polecat (claude, codex, pi); overrides config and template frontmatter")
	cmdAgentSpawnPolecat.Flags().BoolVar(&spawnPolecatNoWorktree, "no-worktree", false, "Skip git worktree creation (no --repo required); polecat edits in-place from ~/.pogo/agents/<name>/ with a refinery:NO posture ({{.NoWorktree}})")

	// Nudge command — top-level for convenience
	var nudgeImmediate bool
	var nudgeTimeout int
	var cmdNudge = &cobra.Command{
		Use:   "nudge <name> <message>",
		Short: "Send a message to an agent via PTY",
		Long: `Send text to an agent's PTY via pogod.

By default, waits for the agent to be idle (no PTY output for 2s) before
delivering the message. Use --immediate to write directly without waiting.

If the agent is not running, falls back to sending the message via gt mail.`,
		Args: cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			message := strings.Join(args[1:], " ")

			opts := &client.NudgeOpts{
				Mode:    "wait-idle",
				Timeout: nudgeTimeout,
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
	cmdNudge.Flags().BoolVarP(&nudgeImmediate, "immediate", "i", false, "Write directly to PTY without waiting for idle")
	cmdNudge.Flags().IntVarP(&nudgeTimeout, "timeout", "T", 30, "Seconds to wait for agent idle (wait-idle mode)")

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
			fmt.Printf("%-20s  %-20s  %-25s  %s\n", "ID", "AGENT", "NEXT FIRE", "CRON / ONCE")
			for _, e := range entries {
				kind := e.Cron
				if e.OneShot {
					kind = "one-shot"
				}
				fmt.Printf("%-20s  %-20s  %-25s  %s\n",
					e.ID, e.Agent, e.NextFire.Local().Format(time.RFC3339), kind)
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
	cmdSchedule.AddCommand(cmdScheduleList)
	cmdSchedule.AddCommand(cmdScheduleRm)

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
			pinRes, pinErr := pinAndResolveRoles(existingInstall)
			if pinErr != nil && !jsonOutput {
				fmt.Fprintf(os.Stderr, "  ⚠ could not pin role defaults: %v\n", pinErr)
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
  - Is the system service installed?
  - Are required tools installed (git, go, the configured agent harness)?
  - Are repos configured?
  - Are agent prompts installed?
  - Are there stale work items?

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

			// 2. System service installed?
			installed, svcPath := service.Status()
			if installed {
				pass("system service", svcPath)
			} else {
				warn("system service", "not installed (run 'pogo service install')")
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
					names := make([]string, 0, len(drift))
					for _, d := range drift {
						names = append(names, fmt.Sprintf("%s (%s)", d.Path, d.Reason))
					}
					fail("agent prompts up-to-date",
						fmt.Sprintf("%d prompt(s) drifted from embedded source: %s — run 'pogo agent prompt install', then restart affected agents",
							len(drift), strings.Join(names, ", ")))
				} else {
					pass("agent prompts up-to-date", "all prompts match embedded source")
				}
			}

			// 6. Macguffin available
			if _, err := exec.LookPath("mg"); err != nil {
				warn("macguffin (mg)", "not found in PATH (install: go install github.com/drellem2/macguffin/cmd/mg@latest)")
			} else {
				// Check for stale claimed items
				mgOut, mgErr := exec.Command("mg", "list", "--status=claimed").CombinedOutput()
				if mgErr != nil {
					pass("macguffin (mg)", "installed")
				} else {
					items := strings.TrimSpace(string(mgOut))
					if items == "" {
						pass("macguffin (mg)", "no stale claims")
					} else {
						count := len(strings.Split(items, "\n"))
						warn("macguffin (mg)", fmt.Sprintf("%d claimed work item(s) — check for stale claims", count))
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
	var cmdGC = &cobra.Command{
		Use:   "gc",
		Short: "Garbage-collect stale polecat branches and leaked worktrees",
		Long: `gc deletes stale polecat-* branches and reclaims leaked git worktrees
whose work items have concluded (done or archived). It also removes orphaned
polecat directories under ~/.pogo/polecats — dirs left behind with files but
no .git when teardown never ran after the worktree was unlinked (gh #31).

It is the manual entry point to the same internal/gitgc logic pogod runs
on startup and on a periodic ticker. Branches and worktrees of in-flight
work items, of currently-running polecats, and anything that cannot be
positively classified are always kept.

By default gc only reports what it would do; pass --apply to make changes.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			repo, err := filepath.Abs(gcRepo)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			// Exclude live polecats from the sweep. Best-effort: if pogod
			// is unreachable, ticket status and git's checked-out-branch
			// protection still guard in-flight work.
			live := map[string]bool{}
			if agents, lerr := client.ListAgents(); lerr == nil {
				for _, a := range agents {
					if a.Type == agent.TypePolecat {
						live[a.Name] = true
					}
				}
			} else if !jsonOutput {
				fmt.Printf("warning: could not reach pogod for the live-polecat list (%v);\n"+
					"         relying on ticket status and git checkout state only.\n\n", lerr)
			}
			// Best-effort: without a resolvable home dir the orphan-dir
			// scan is skipped and gc still sweeps branches and worktrees.
			polecatsDir, _ := gitgc.DefaultPolecatsDir()
			res, err := gitgc.Sweep(gitgc.Options{
				Repo:         repo,
				LivePolecats: live,
				DryRun:       !gcApply,
				PolecatsDir:  polecatsDir,
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
	rootCmd.AddCommand(cmdStatus)
	cmdDoctor.Flags().BoolVar(&doctorCheck, "check", false, "Run quick health checks without starting the doctor agent")
	rootCmd.AddCommand(cmdDoctor)
	cmdServer.AddCommand(cmdServerStart)
	cmdServer.AddCommand(cmdServerStop)
	cmdServer.AddCommand(cmdServerStatus)
	rootCmd.AddCommand(cmdServer)
	cmdService.AddCommand(cmdServiceInstall)
	cmdService.AddCommand(cmdServiceUninstall)
	cmdService.AddCommand(cmdServiceStatus)
	cmdService.AddCommand(cmdServiceInstallRecovery)
	cmdService.AddCommand(cmdServiceUninstallRecovery)
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
60s and archives processed requests to ~/.pogo/recovery/processed/.`,
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
	cmdAgent.AddCommand(cmdAgentSpawn)
	cmdAgent.AddCommand(cmdAgentSpawnPolecat)
	cmdAgent.AddCommand(cmdAgentStop)
	cmdAgent.AddCommand(cmdAgentPark)
	cmdAgent.AddCommand(cmdAgentWake)
	cmdAgent.AddCommand(cmdAgentStatus)
	cmdAgent.AddCommand(cmdAgentDiagnose)
	cmdAgent.AddCommand(cmdAgentAttach)
	cmdAgent.AddCommand(cmdAgentOutput)
	cmdAgentPrompt.AddCommand(cmdAgentPromptList)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInit)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInstall)
	cmdAgentPrompt.AddCommand(cmdAgentPromptShow)
	cmdAgentPrompt.AddCommand(cmdAgentPromptCreate)
	cmdAgent.AddCommand(cmdAgentPrompt)
	rootCmd.AddCommand(cmdAgent)
	rootCmd.AddCommand(cmdNudge)
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

Example:
  pogo refinery submit polecat-a3f --repo=/path/to/repo`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			branch := args[0]
			if submitRepo == "" {
				cli.ExitWithError(jsonOutput, "--repo is required", cli.ExitError)
			}
			id, err := client.SubmitMerge(refinery.SubmitRequest{
				RepoPath:            submitRepo,
				Branch:              branch,
				TargetRef:           submitTarget,
				Author:              submitAuthor,
				AutoCreateTargetRef: submitAutoCreateTarget,
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
				fmt.Printf("Queue:   %d\n", status.QueueLen)
				fmt.Printf("History: %d\n", status.HistoryLen)
			}
		},
	}

	var cmdRefineryQueue = &cobra.Command{
		Use:   "queue",
		Short: "Show pending merge requests",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			queue, err := client.GetRefineryQueue()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(queue)
			} else {
				if len(queue) == 0 {
					fmt.Println("No pending merges.")
					return
				}
				for _, mr := range queue {
					fmt.Printf("%-12s  branch=%-30s  author=%-15s  status=%-10s  submitted=%s\n",
						mr.ID, mr.Branch, mr.Author, string(mr.Status), mr.SubmitTime.Format("2006-01-02 15:04"))
				}
			}
		},
	}

	var cmdRefineryHistory = &cobra.Command{
		Use:   "history",
		Short: "Show completed merge requests with status",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			history, err := client.GetRefineryHistory()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(history)
			} else {
				if len(history) == 0 {
					fmt.Println("No merge history.")
					return
				}
				for _, mr := range history {
					line := fmt.Sprintf("%-12s  branch=%-30s  author=%-15s  status=%-10s  done=%s",
						mr.ID, mr.Branch, mr.Author, string(mr.Status), mr.DoneTime.Format("2006-01-02 15:04"))
					if mr.Error != "" {
						line += fmt.Sprintf("  error=%s", mr.Error)
					}
					fmt.Println(line)
				}
			}
		},
	}

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
				fmt.Printf("Status:    %s\n", mr.Status)
				if mr.AlreadyMerged {
					fmt.Printf("Note:      branch had already landed on the target — resolved as merged without re-running gates\n")
				}
				fmt.Printf("Repo:      %s\n", mr.RepoPath)
				fmt.Printf("Submitted: %s\n", mr.SubmitTime.Format("2006-01-02 15:04:05"))
				if !mr.DoneTime.IsZero() {
					fmt.Printf("Done:      %s\n", mr.DoneTime.Format("2006-01-02 15:04:05"))
				}
				if mr.Error != "" {
					fmt.Printf("Error:     %s\n", mr.Error)
				}
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
		Short: "Cancel a queued merge request",
		Long: `Remove a merge request from the queue without merging.

Only queued (not yet processing) merge requests can be cancelled.

Example:
  pogo refinery cancel mr-abc123`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			id := args[0]
			if err := client.CancelMerge(id); err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"id": id, "status": "cancelled"})
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
				p, err := events.DefaultLogPath()
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
				p, err := events.DefaultLogPath()
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
