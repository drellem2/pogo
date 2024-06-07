////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"sort"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/client"
)

func main() {
	var rootCmd = &cobra.Command{Use: "lsp"}

	rootCmd.Flags().BoolP("profile", "", false, "Enable CPU profiling")
	// The following behavior will be executed when the root command `rootCmd` is used:
	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		profileEnabled, err := cobraCmd.Flags().GetBool("profile")
		if err != nil {
			log.Fatal(err)
		}
		if profileEnabled {
			// Enable CPU profiling
			f, err := os.Create("cpu.prof")
			if err != nil {
				log.Fatal(err)
			}
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}

		projs, err := client.GetProjects()
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
