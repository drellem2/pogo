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

	"github.com/drellem2/pogo/internal/client"
)

func main() {
	var rootCmd = &cobra.Command{Use: "pose"}
	// Add -l flag to the root command
	rootCmd.Flags().BoolP("list", "l", false, "List all files with matching lines")
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

		// Get the value of the -l flag
		list, err := cobraCmd.Flags().GetBool("list")
		if err != nil {
			log.Fatal(err)
		}

		if list {
			// Remove duplicates from files
			uniqueFiles := make(map[string]struct{})
			for _, file := range files {
				uniqueFiles[file.Path] = struct{}{}
			}

			// Sort
			paths := make([]string, len(uniqueFiles))
			i := 0
			for p, _ := range uniqueFiles {
				paths[i] = p
				i++
			}
			sort.Strings(paths)
			// iterate over keys of uniqueFiles
			for _, path := range paths {
				fmt.Println(path)
			}
		} else {
			// Sort files by number of matching lines
			sort.Slice(files, func(i, j int) bool {
				return len(files[i].Matches) > len(files[j].Matches)
			})

			for _, file := range files {
				fmt.Printf("%s\n", file.Path)
				for _, match := range file.Matches {
					fmt.Printf("\t%d:\t%s\n", match.Line, match.Content)
				}
			}
		}

	}

	rootCmd.Execute()
}
