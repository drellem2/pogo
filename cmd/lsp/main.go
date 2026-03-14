////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI tool ///////////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"sort"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

func main() {
	var jsonOutput bool

	var rootCmd = &cobra.Command{Use: "lsp"}

	rootCmd.Flags().BoolP("profile", "", false, "Enable CPU profiling")
	rootCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		profileEnabled, err := cobraCmd.Flags().GetBool("profile")
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}
		if profileEnabled {
			f, err := os.Create("cpu.prof")
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}

		projs, err := client.GetProjects()
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}

		sort.Slice(projs, func(i, j int) bool {
			return projs[i].Path < projs[j].Path
		})

		if jsonOutput {
			cli.PrintJSON(projs)
		} else {
			for _, proj := range projs {
				fmt.Println(proj.Path)
			}
		}
	}

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
