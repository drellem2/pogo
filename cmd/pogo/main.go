////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/service"
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

	var cmdServerStop = &cobra.Command{
		Use:   "stop",
		Short: "Stop the pogo server",
		Long:  `Stop the pogo server.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
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
		},
	}

	var cmdStatus = &cobra.Command{
		Use:   "status",
		Short: "Show indexing status of projects",
		Long:  `Show the indexing status of all registered projects.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			statuses, err := client.GetStatus()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(statuses)
			} else {
				if len(statuses) == 0 {
					fmt.Println("No projects registered.")
					return
				}
				for _, s := range statuses {
					fmt.Printf("%-12s %s (%d files)\n", s.Status, s.Path, s.FileCount)
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
					fmt.Printf("%-20s  pid=%-6d  type=%-8s  status=%-10s  uptime=%s\n",
						a.Name, a.PID, a.Type, a.Status, a.Uptime)
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

	var cmdAgentOutput = &cobra.Command{
		Use:   "output <name>",
		Short: "Show recent output from an agent",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			output, err := client.GetAgentOutput(args[0])
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
						restart := ""
						if a.RestartCount > 0 {
							restart = fmt.Sprintf("  restarts=%d", a.RestartCount)
						}
						fmt.Printf("  %-20s  %-12s  %-8s  pid=%-6d  uptime=%s%s\n",
							a.Name, a.ProcessName, a.Status, a.PID, a.Uptime, restart)
					}
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
Existing files are not overwritten unless --force is used.`,
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
				for _, f := range result.Skipped {
					fmt.Printf("  skipped (exists): %s\n", f)
				}
				if len(result.Installed) == 0 && len(result.Skipped) > 0 {
					fmt.Println("All prompts already installed. Use --force to overwrite.")
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

	// Spawn polecat from template
	var spawnPolecatTemplate string
	var spawnPolecatTask string
	var spawnPolecatBody string
	var spawnPolecatId string
	var spawnPolecatRepo string
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

Safe to run multiple times — existing files are preserved unless --force is passed.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Step 1: Ensure daemon is running
			err := client.HealthCheck()
			if err != nil {
				if !jsonOutput {
					fmt.Println("Starting pogo server...")
				}
				if err := client.StartServer(); err != nil {
					cli.ExitWithError(jsonOutput, "failed to start server: "+err.Error(), cli.ExitError)
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
			mgInit := func() error {
				c := exec.Command("mg", "init")
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				return c.Run()
			}
			if err := mgInit(); err != nil {
				// mg init is idempotent — if it fails, mg might not be installed
				if !jsonOutput {
					fmt.Println("  ⚠ mg init failed (is macguffin installed?)")
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
				if len(result.Skipped) > 0 {
					fmt.Printf("  ✓ %d prompt(s) already exist (use --force to overwrite)\n", len(result.Skipped))
				}
				fmt.Println("\nReady. Next steps:")
				fmt.Println("  pogo agent start mayor    # Start the coordinator")
				fmt.Println("  mg new \"your task here\"   # File work for agents")
			}
		},
	}
	cmdInstall.Flags().BoolVar(&installForceFlag, "force", false, "Overwrite existing prompt files")

	var rootCmd = &cobra.Command{Use: "pogo"}

	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.AddCommand(cmdInstall)
	rootCmd.AddCommand(cmdVisit)
	rootCmd.AddCommand(cmdStatus)
	cmdServer.AddCommand(cmdServerStart)
	cmdServer.AddCommand(cmdServerStop)
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
	cmdAgent.AddCommand(cmdAgentAttach)
	cmdAgent.AddCommand(cmdAgentOutput)
	cmdAgentPrompt.AddCommand(cmdAgentPromptList)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInit)
	cmdAgentPrompt.AddCommand(cmdAgentPromptInstall)
	cmdAgentPrompt.AddCommand(cmdAgentPromptShow)
	cmdAgent.AddCommand(cmdAgentPrompt)
	rootCmd.AddCommand(cmdAgent)
	rootCmd.AddCommand(cmdNudge)

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

	cmdRefinery.AddCommand(cmdRefinerySubmit)
	rootCmd.AddCommand(cmdRefinery)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
