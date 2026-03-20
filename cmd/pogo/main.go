////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/service"
)

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
					fmt.Printf("%-20s  pid=%-6d  type=%-8s  started=%s\n",
						a.Name, a.PID, a.Type, a.StartTime.Format("15:04:05"))
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

	// Nudge command — top-level for convenience
	var cmdNudge = &cobra.Command{
		Use:   "nudge <name> <message>",
		Short: "Write a message to an agent's PTY",
		Long: `Send text to an agent's PTY master fd via pogod.
The agent sees it as typed input. Use this for agent-to-agent wakeup or human-to-agent messages.`,
		Args: cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]
			message := strings.Join(args[1:], " ")
			err := client.NudgeAgent(name, message)
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			if jsonOutput {
				cli.PrintJSON(map[string]string{"status": "delivered", "agent": name})
			} else {
				fmt.Printf("Nudged %s.\n", name)
			}
		},
	}

	var rootCmd = &cobra.Command{Use: "pogo"}

	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

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
	cmdAgent.AddCommand(cmdAgentList)
	cmdAgent.AddCommand(cmdAgentSpawn)
	cmdAgent.AddCommand(cmdAgentStop)
	cmdAgent.AddCommand(cmdAgentAttach)
	cmdAgent.AddCommand(cmdAgentOutput)
	rootCmd.AddCommand(cmdAgent)
	rootCmd.AddCommand(cmdNudge)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
