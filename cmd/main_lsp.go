////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/marginalia-gaming/pogo/internal/project"
)

func main() {
	var rootCmd = &cobra.Command{Use: "lsp"}
	client := &http.Client{}
	// The following behavior will be executed when the root command `rootCmd` is used:
	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		healthCheckReq, err := http.NewRequest("POST", "http://localhost:10000/health", nil)
		if err != nil {
			log.Fatal(err)
		}
		_, err = client.Do(healthCheckReq)
		if err != nil {
			// Store the result of os.exec("pogod") in a variable and describe its type
			// If the type is a pointer to a process, then the server is running
			cmd := exec.Command("pogod")
			if err := cmd.Run(); err != nil {
				log.Fatal(err)
			}
			success := false
			// Loop for up to half a second to check if the server is running
			// Get current time
			startTime := time.Now()
			// Inside for loop, check current time against startTime
			for time.Now().Sub(startTime) < 500*time.Millisecond {
				_, err := client.Do(healthCheckReq)
				if err == nil {
					success = true
					break
				}
			}
			if !success {
				log.Fatal("pogod is not running")
				return
			}
		}
		projReq, err := http.NewRequest("GET", "http://localhost:10000/projects", nil)
		if err != nil {
			log.Fatal(err)
		}
		r, err := client.Do(projReq)
		if err != nil {
			log.Fatal(err)
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		// Deserialize projResp
		// Do json demarshal from http response
		var projs []project.Project
		err = json.Unmarshal(body, &projs)
		if err != nil {
			log.Fatal(err)
		}
		// Sort projs by proj.Path
		sort.Slice(projs, func(i, j int) bool {
			return projs[i].Path < projs[j].Path
		})

		for _, proj := range projs {
			fmt.Println(proj.Path)
		}
	}

	rootCmd.Execute()
}
