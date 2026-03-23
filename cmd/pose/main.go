////////////////////////////////////////////////////////////////////////////////
///////////// Main file for the CLI code search ////////////////////////////////
////////////////////////////////////////////////////////////////////////////////

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/completion"
	"github.com/drellem2/pogo/internal/version"
)

func main() {
	var jsonOutput bool
	var searchAll bool

	var rootCmd = &cobra.Command{Use: "pose", Version: version.Version}
	rootCmd.Flags().BoolP("list", "l", false, "List all files with matching lines")
	rootCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.Flags().BoolVar(&searchAll, "all", false, "Search across all known projects")

	rootCmd.Run = func(cobraCmd *cobra.Command, args []string) {
		if len(args) == 0 {
			cli.ExitWithError(jsonOutput, "pose requires a query argument", cli.ExitError)
		}

		list, err := cobraCmd.Flags().GetBool("list")
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}

		if searchAll {
			runSearchAll(args[0], jsonOutput, list)
			return
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

	completion.AddCommand(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(cli.ExitError)
	}
}

func runSearchAll(query string, jsonOutput bool, list bool) {
	first := true
	count := 0

	if jsonOutput {
		// Use newline-delimited JSON: emit each repo's result object as it arrives
		err := client.SearchAllStreaming(query, func(resp *client.SearchResponse) {
			count++
			data, err := json.Marshal(resp)
			if err != nil {
				fmt.Fprintf(os.Stderr, `{"error": "failed to marshal JSON: %s"}`+"\n", err)
				return
			}
			fmt.Println(string(data))
		})
		if err != nil {
			cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
		}
		return
	}

	err := client.SearchAllStreaming(query, func(resp *client.SearchResponse) {
		count++
		if !first {
			fmt.Println()
		}
		first = false

		fmt.Printf("=== %s ===\n", resp.Index.Root)
		if resp.Error != "" {
			fmt.Printf("  error: %s\n", resp.Error)
			return
		}

		files := resp.Results.Files
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
				fmt.Printf("  %s\n", path)
			}
		} else {
			sort.Slice(files, func(i, j int) bool {
				return len(files[i].Matches) > len(files[j].Matches)
			})
			for _, file := range files {
				fmt.Printf("  %s\n", file.Path)
				for _, match := range file.Matches {
					fmt.Printf("    %d:\t%s\n", match.Line, match.Content)
				}
			}
		}
	})
	if err != nil {
		cli.ExitWithError(jsonOutput, err.Error(), cli.ExitError)
	}
}
