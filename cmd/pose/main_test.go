package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/completion"
)

// TestPositionalQueryRoutesToRun guards against gh #20: with the "completion"
// subcommand registered, cobra's default legacyArgs validator rejected any
// non-subcommand first positional with `unknown command`, so `pose <query>`
// never reached Run. ArbitraryArgs must route the query through instead.
func TestPositionalQueryRoutesToRun(t *testing.T) {
	cases := [][]string{
		{"func main"},               // bare query
		{"-l", "func main"},         // query after a flag
		{"-l", "--all", "main"},     // query after multiple flags
		{"func main", "/some/path"}, // query + path
	}

	for _, args := range cases {
		t.Run(args[len(args)-1], func(t *testing.T) {
			rootCmd := newRootCmd()
			completion.AddCommand(rootCmd)

			var gotArgs []string
			ran := false
			rootCmd.Run = func(_ *cobra.Command, a []string) {
				ran = true
				gotArgs = a
			}

			rootCmd.SetArgs(args)
			rootCmd.SetOut(&bytes.Buffer{})
			rootCmd.SetErr(&bytes.Buffer{})

			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("Execute(%v) returned error: %v", args, err)
			}
			if !ran {
				t.Fatalf("Execute(%v) did not invoke Run", args)
			}
			if len(gotArgs) == 0 {
				t.Fatalf("Execute(%v) passed no positional args to Run", args)
			}
		})
	}
}

// TestCompletionSubcommandStillResolves ensures the routing fix did not break
// the registered "completion" subcommand: a matching first positional must
// still dispatch to the subcommand rather than the root Run.
func TestCompletionSubcommandStillResolves(t *testing.T) {
	rootCmd := newRootCmd()
	completion.AddCommand(rootCmd)

	rootRan := false
	rootCmd.Run = func(_ *cobra.Command, _ []string) { rootRan = true }

	rootCmd.SetArgs([]string{"completion", "bash"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute(completion bash) returned error: %v", err)
	}
	if rootRan {
		t.Fatal("completion subcommand was shadowed by root Run")
	}
}
