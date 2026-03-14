////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
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

	var rootCmd = &cobra.Command{Use: "pogo"}

	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.AddCommand(cmdVisit)
	cmdServer.AddCommand(cmdServerStart)
	cmdServer.AddCommand(cmdServerStop)
	rootCmd.AddCommand(cmdServer)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
