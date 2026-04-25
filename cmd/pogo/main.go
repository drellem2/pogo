////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
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
	"github.com/drellem2/pogo/internal/refinery"
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

func main() {

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

	renderStatus := func() {
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
			return
		}

		// --- Text output ---

		if statusLive {
			fmt.Printf("pogo status --live  (every %s, Ctrl-C to quit)\n\n", statusInterval)
		}

		// Agents section
		fmt.Println("=== Agents ===")
		if agentErr != nil {
			fmt.Printf("  (unavailable: %s)\n", agentErr)
		} else if len(agents) == 0 {
			fmt.Println("  No agents running.")
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
			fmt.Printf("  %d total (%d crew, %d polecat), %d running\n",
				len(agents), crew, polecats, running)
			for _, a := range agents {
				fmt.Printf("  %-20s  %-8s  %-10s  pid=%-6d  uptime=%s\n",
					a.Name, a.Type, a.Status, a.PID, a.Uptime)
			}
		}
		fmt.Println()

		// Work items section
		fmt.Println("=== Work Items ===")
		if mgErr != nil {
			fmt.Println("  (unavailable: mg not found)")
		} else if report.WorkItems == "" {
			fmt.Println("  No work items.")
		} else {
			for _, line := range strings.Split(report.WorkItems, "\n") {
				fmt.Printf("  %s\n", line)
			}
		}
		fmt.Println()

		// Refinery section
		fmt.Println("=== Refinery ===")
		if refErr != nil {
			fmt.Printf("  (unavailable: %s)\n", refErr)
		} else {
			state := "stopped"
			if refStatus.Running {
				state = "running"
			}
			if !refStatus.Enabled {
				state = "disabled"
			}
			fmt.Printf("  Status: %s  |  Queue: %d  |  History: %d  |  Poll: %s\n",
				state, refStatus.QueueLen, refStatus.HistoryLen, refStatus.PollInterval)
		}
		if queueErr == nil && len(queue) > 0 {
			fmt.Println()
			for _, mr := range queue {
				age := time.Since(mr.SubmitTime).Truncate(time.Second)
				author := mr.Author
				if author == "" {
					author = "-"
				}
				fmt.Printf("  %-8s  %-20s  branch=%-30s  author=%-15s  age=%s\n",
					mr.Status, mr.ID, mr.Branch, author, age)
			}
		}
	}

	var cmdStatus = &cobra.Command{
		Use:   "status",
		Short: "Show unified dashboard of agents, work items, and refinery queue",
		Long: `Show a unified read-only dashboard aggregating:
  - Running agents (from pogod)
  - Work items (from macguffin)
  - Refinery merge queue (from pogod)

Use --live for a continuously updating view (like watch).`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if !statusLive {
				renderStatus()
				return
			}

			// Live mode: clear screen and refresh on interval
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

			ticker := time.NewTicker(statusInterval)
			defer ticker.Stop()

			// Initial render
			fmt.Print("\033[2J\033[H") // clear screen, cursor to top
			renderStatus()

			for {
				select {
				case <-sig:
					fmt.Println()
					return
				case <-ticker.C:
					fmt.Print("\033[2J\033[H") // clear screen, cursor to top
					renderStatus()
				}
			}
		},
	}

	var cmdService = &cobra.Command{
		Use:   "service",
		Short: "Manage the pogo system service",
		Long:  `Install, uninstall, or check the status of the pogo daemon as a system service (launchd on macOS, systemd on Linux).`,
	}

	var cmdServiceInstall = &cobra.Command{
		Use:   "install",
		Short: "Install pogo as a system service",
		Long:  `Generate and install a launchd plist (macOS) or systemd unit (Linux) so the pogo daemon starts on login and restarts on crash.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := service.Install(); err != nil {
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
					activity := ""
					if a.LastActivity != "" {
						activity = "  last-activity=" + a.LastActivity
					}
					fmt.Printf("%-20s  pid=%-6d  type=%-8s  status=%-10s  uptime=%s%s\n",
						a.Name, a.PID, a.Type, a.Status, a.Uptime, activity)
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

	var cmdAgentAttach = &cobra.Command{
		Use:   "attach <name>",
		Short: "Attach terminal to a running agent",
		Long: `Connect your terminal to a running agent's PTY via its unix domain socket.
The agent's output streams to your terminal and your input goes to the agent.
Detach with Ctrl-\ (SIGQUIT).`,
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
  healthy  — running and producing output normally
  idle     — running but quiet for over half the stall threshold
  stalled  — running but quiet for longer than the stall threshold
  exited   — process has exited
  dead     — registered as running but OS process is gone`,
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
	var cmdAgentPromptInstall = &cobra.Command{
		Use:   "install",
		Short: "Install default prompt files to ~/.pogo/agents/",
		Long: `Copy the default mayor prompt and polecat template to ~/.pogo/agents/.
Stale files are auto-updated when the embedded version changes. Use --force to overwrite all files.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			result, err := agent.InstallPrompts(installForce)
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
				if len(result.Installed) == 0 && len(result.Updated) == 0 && len(result.Skipped) > 0 {
					fmt.Println("All prompts up-to-date.")
				}
			}
		},
	}
	cmdAgentPromptInstall.Flags().BoolVar(&installForce, "force", false, "Overwrite existing prompt files")

	var cmdAgentPromptShow = &cobra.Command{
		Use:   "show <name>",
		Short: "Show contents of a prompt file",
		Long:  `Show the raw contents of a crew prompt, template, or the mayor prompt.`,
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			// Try mayor first
			if name == "mayor" {
				path, err := agent.ResolveMayorPrompt()
				if err != nil {
					cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
				}
				showPromptFile(path, jsonOutput)
				return
			}
			// Try crew
			if path, err := agent.ResolveCrewPrompt(name); err == nil {
				showPromptFile(path, jsonOutput)
				return
			}
			// Try template
			if path, err := agent.ResolveTemplate(name); err == nil {
				showPromptFile(path, jsonOutput)
				return
			}
			cli.ExitWithError(jsonOutput, fmt.Sprintf("prompt %q not found (checked crew/, templates/, and mayor.md)", name), cli.ExitError)
		},
	}

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
	var cmdAgentSpawnPolecat = &cobra.Command{
		Use:   "spawn-polecat <name>",
		Short: "Spawn a polecat from a prompt template",
		Long: `Spawn an ephemeral polecat agent using a prompt template from ~/.pogo/agents/templates/.
The template is expanded with the provided variables and used as the agent's prompt file.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			info, err := client.SpawnPolecat(agent.SpawnPolecatAPIRequest{
				Name:     args[0],
				Template: spawnPolecatTemplate,
				Task:     spawnPolecatTask,
				Body:     spawnPolecatBody,
				Id:       spawnPolecatId,
				Repo:     spawnPolecatRepo,
				Branch:   spawnPolecatBranch,
				Env:      spawnPolecatEnv,
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

	var installForceFlag bool
	var cmdInstall = &cobra.Command{
		Use:   "install",
		Short: "Set up pogo for agent orchestration",
		Long: `Initialize everything needed for agent orchestration in one step:
1. Start the pogo daemon (if not already running)
2. Initialize macguffin workspace (mg init)
3. Install default agent prompts to ~/.pogo/agents/

Safe to run multiple times — stale prompts are auto-updated, other files are preserved.`,
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

			// Step 3: Install prompts
			result, err := agent.InstallPrompts(installForceFlag)
			if err != nil {
				cli.ExitWithError(jsonOutput, "failed to install prompts: "+err.Error(), cli.ExitError)
			}

			if jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					"status":  "installed",
					"prompts": result,
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
				fmt.Println("\nReady. Next steps:")
				fmt.Println("  pogo agent start mayor    # Start the coordinator")
				fmt.Println("  mg new \"your task here\"   # File work for agents")
			}
		},
	}
	cmdInstall.Flags().BoolVar(&installForceFlag, "force", false, "Overwrite existing prompt files")

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
  - Are required tools installed (git, go, claude)?
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

			// 3. Required tools
			for _, tool := range []string{"git", "go", "claude"} {
				if p, err := exec.LookPath(tool); err != nil {
					if tool == "claude" {
						warn(tool+" in PATH", "not found")
					} else {
						fail(tool+" in PATH", "not found")
					}
				} else {
					pass(tool+" in PATH", p)
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

	var rootCmd = &cobra.Command{Use: "pogo", Version: version.Version}

	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.AddCommand(cmdVersion)
	rootCmd.AddCommand(cmdInstall)
	rootCmd.AddCommand(cmdVisit)
	cmdStatus.Flags().BoolVar(&statusLive, "live", false, "Continuously refresh the dashboard (like watch)")
	cmdStatus.Flags().DurationVar(&statusInterval, "interval", 2*time.Second, "Refresh interval for --live mode")
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
	rootCmd.AddCommand(cmdService)

	// Agent commands
	cmdAgent.AddCommand(cmdAgentStart)
	cmdAgent.AddCommand(cmdAgentList)
	cmdAgent.AddCommand(cmdAgentSpawn)
	cmdAgent.AddCommand(cmdAgentSpawnPolecat)
	cmdAgent.AddCommand(cmdAgentStop)
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
	var cmdRefinerySubmit = &cobra.Command{
		Use:   "submit <branch>",
		Short: "Submit a branch to the merge queue",
		Long: `Submit a branch for the refinery to test and merge.

The refinery will fetch the branch, run quality gates (build.sh/test.sh or
.pogo/refinery.toml), and fast-forward merge to the target ref if they pass.

Example:
  pogo refinery submit polecat-a3f --repo=/path/to/repo`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			branch := args[0]
			if submitRepo == "" {
				cli.ExitWithError(jsonOutput, "--repo is required", cli.ExitError)
			}
			id, err := client.SubmitMerge(refinery.SubmitRequest{
				RepoPath:  submitRepo,
				Branch:    branch,
				TargetRef: submitTarget,
				Author:    submitAuthor,
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
	completion.AddCommand(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
