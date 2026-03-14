////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI code search ////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

func main() {
	var jsonOutput bool

	var rootCmd = &cobra.Command{Use: "pose"}
	rootCmd.Flags().BoolP("list", "l", false, "List all files with matching lines")
	rootCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		if len(args) == 0 {
			cli.ExitWithError(jsonOutput, "pose requires a query argument", cli.ExitError)
		}

		var path string
		if len(args) > 1 {
			fullPath, err := filepath.Abs(args[1])
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			path = fullPath
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
			}
			path = cwd
		}
		results, err := client.Search(args[0], path)
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}
		files := results.Results.Files

		if jsonOutput {
			cli.PrintJSON(results)
			return
		}

		list, err := cobraCmd.Flags().GetBool("list")
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}

		if list {
			uniqueFiles := make(map[string]struct{})
			for _, file := range files {
				uniqueFiles[file.Path] = struct{}{}
			}

			paths := make([]string, 0, len(uniqueFiles))
			for p := range uniqueFiles {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, path := range paths {
				fmt.Println(path)
			}
		} else {
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

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}
