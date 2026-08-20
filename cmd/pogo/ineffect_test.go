package main

import (
	"os"
	"strings"
	"testing"
)

// The wiring. The report machinery can be perfect while nothing registers the
// command, and TOP-LEVEL placement is load-bearing for the same reason
// check-activation's is: cobra answers an unknown SUBcommand of a parent with
// exit 0, so `pogo service in-effect` on a binary predating this change would
// print help and succeed. A top-level name makes an old binary's absence loud —
// which matters more here than anywhere, because this command's whole subject
// is a merged change that is not running, and it ships in the pogo CLI, so it
// is subject to its own condition.
func TestInEffectIsRegisteredTopLevel(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "rootCmd.AddCommand(newInEffectCmd(") {
		t.Error("`pogo in-effect` is not registered on rootCmd — the command exists and nothing can reach it")
	}
	if strings.Contains(s, "cmdService.AddCommand(newInEffectCmd(") {
		t.Error("`in-effect` is registered under `pogo service`, where an old binary answers an unknown subcommand with exit 0 rather than failing")
	}
}

func TestInEffectCommandShape(t *testing.T) {
	jsonOutput := false
	cmd := newInEffectCmd(&jsonOutput)

	if !strings.HasPrefix(cmd.Use, "in-effect ") {
		t.Errorf("Use = %q, want it to start with `in-effect `", cmd.Use)
	}
	for _, f := range []string{"repo", "ref", "pogo-home", "deploy-src"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("--%s is missing; every path the report reads must be overridable, or the command cannot be pointed at a sandbox", f)
		}
	}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("the command accepted no arguments; `in-effect` with no commit has nothing to answer about")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("the command accepted two commits; the report is about one")
	}
}

// The help text is where a reader learns that `unknown` is not a soft `inert`
// and that `half-live` exists. Both are the point of the command and neither is
// guessable from the name, so their absence is a real regression.
func TestInEffectHelpKeepsTheDistinctionsThatMatter(t *testing.T) {
	jsonOutput := false
	long := newInEffectCmd(&jsonOutput).Long
	for _, want := range []string{"half-live", "unknown", "inert", "mg-385f"} {
		if !strings.Contains(long, want) {
			t.Errorf("the long help no longer mentions %q — the command's whole content is the distinction it names", want)
		}
	}
}
