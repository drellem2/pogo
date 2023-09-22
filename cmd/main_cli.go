////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func main() {

	var cmdVisit = &cobra.Command{
		Use:   "visit [file]",
		Short: "Visit file or directory",
		Long: `Checks if the file is contained in a repository, and if so indexes the
 repository.`,
		Args: cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			_, err := http.Post("http://localhost:10000/file", "application/json",
				strings.NewReader(fmt.Sprintf(`{"path": "%s"}`, args[0])))
			if err != nil {
				log.Fatal(err)
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
			_, err := http.NewRequest("POST", "http://localhost:10000/health", nil)
			if err != nil {
				fmt.Println("Starting pogo server...")
				// Store the result of os.exec("pogod") in a variable and describe its type
				// If the type is a pointer to a process, then the server is running
				cmd := exec.Command("pogod")
				if err := cmd.Run(); err != nil {
					log.Fatal(err)
				}
				return
			}
			fmt.Println("The server is already running")
		},
	}

	var cmdServerStop = &cobra.Command{
		Use:   "stop",
		Short: "Stop the pogo server",
		Long:  `Stop the pogo server.`,
		Args:  cobra.MinimumNArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			// Stop the process "pogod"
		},
	}

	var rootCmd = &cobra.Command{Use: "pogo"}

	rootCmd.AddCommand(cmdVisit)
	cmdServer.AddCommand(cmdServerStart)
	cmdServer.AddCommand(cmdServerStop)
	rootCmd.AddCommand(cmdServer)
	rootCmd.Execute()
}
