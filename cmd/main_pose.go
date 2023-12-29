////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI code search ////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/marginalia-gaming/pogo/internal/client"
)

func main() {
	var rootCmd = &cobra.Command{Use: "pose"}
	// The following behavior will be executed when the root command `rootCmd` is used:
	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		var path string
		if len(args) > 1 {
			// Expand args[1] to an absolute path
			fullPath, err := filepath.Abs(args[1])
			if err != nil {
				log.Fatal(err)
			}
			path = fullPath
		} else {
			// Get current working directory
			cwd, err := os.Getwd()
			if err != nil {
				log.Fatal(err)
			}
			path = cwd
		}
		results, err := client.Search(args[0], path)
		if err != nil {
			log.Fatal(err)
		}
		files := results.Results.Files

		// Sort files by number of matching lines
		sort.Slice(files, func(i, j int) bool {
			return len(files[i].Matches) > len(files[j].Matches)
		})
		for _, file := range files {
			fmt.Println(file.Path)
		}
	}

	rootCmd.Execute()
}
