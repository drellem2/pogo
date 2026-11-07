package agent

// mg-2a5d0: the release-cut hook's dedup must see SHELVED items.
//
// Plain `mg list` omits shelved items, and a release cut a PM parked (assignee
// parked, shelved, awaiting a GO) is shelved. The dedup read
// `mg list --tag=release-cut --json`, so a parked cut was invisible to it and
// was re-filed as a duplicate on every sweep while the repo stayed over
// threshold (mg-a12b4 duplicated mg-ee519).
//
// This test runs the dedup pipeline exactly as the template ships it, against
// a fake `mg` that models the real one's visibility rules: shelved and archived
// items appear only under --all (or --shelved / --archived respectively). The
// fake's fidelity is checked by a control: the pre-mg-2a5d0 line, run against
// the same fake and the same shelved item, must fail to dedup. Without that
// control a fake that returned shelved items unconditionally would pass the
// old line too and prove nothing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// preFixDedup is the dedup line as it shipped until mg-2a5d0.
const preFixDedup = `mg list --tag=release-cut --json 2>/dev/null \
      | jq -r 'select(.status != "done") | .title' | grep -q "$slug" && continue`

// fakeMg emits the NDJSON in $FAKE_MG_ITEMS (one "status<TAB>title" per line),
// filtered the way real `mg list` filters: shelved only with --all/--shelved,
// archived only with --all/--archived.
const fakeMg = `#!/bin/bash
shelved=0; archived=0
for a in "$@"; do
  case "$a" in
    --all) shelved=1; archived=1;;
    --shelved) shelved=1;;
    --archived) archived=1;;
  esac
done
while IFS=$'\t' read -r st title; do
  [ -z "$st" ] && continue
  [ "$st" = shelved ] && [ $shelved = 0 ] && continue
  [ "$st" = archived ] && [ $archived = 0 ] && continue
  jq -cn --arg s "$st" --arg t "$title" '{status:$s,title:$t}'
done <<< "$FAKE_MG_ITEMS"
`

// shippedDedup pulls the dedup pipeline out of pm-template.md: from the
// `mg list --tag=release-cut` line through the line ending `&& continue`.
func shippedDedup(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("prompts", "pm", "pm-template.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if !strings.Contains(l, "mg list --tag=release-cut") {
			continue
		}
		for j := i; j < len(lines) && j < i+6; j++ {
			if strings.HasSuffix(strings.TrimSpace(lines[j]), "&& continue") {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
		t.Fatalf("pm-template.md:%d: dedup line has no `&& continue` within 6 lines", i+1)
	}
	t.Fatal("pm-template.md: no `mg list --tag=release-cut` dedup line found")
	return ""
}

// runDedup runs a dedup snippet inside a one-iteration loop and reports
// whether it skipped (deduped) the repo.
func runDedup(t *testing.T, snippet, slug, items string) (skipped bool) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mg"), []byte(fakeMg), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "for _ in once; do\n" + snippet + "\necho FILED\ndone\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"slug="+slug,
		"FAKE_MG_ITEMS="+items,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dedup snippet failed: %v\n%s", err, out)
	}
	return !strings.Contains(string(out), "FILED")
}

func TestReleaseCutDedupSeesShelvedItems(t *testing.T) {
	dedup := shippedDedup(t)
	const slug = "drellem2/macguffin"
	shelvedCut := "shelved\trelease-cut: drellem2/macguffin past v0.4.0 [PARKED]"
	archivedCut := "archived\trelease-cut: drellem2/macguffin — main is 38 commits ahead of v0.3.0 (59d)"
	otherRepo := "pending\trelease-cut: drellem2/pogo v0.13.0"

	cases := []struct {
		name  string
		items string
		skip  bool
	}{
		{"shelved cut for the repo is already tracked", shelvedCut + "\n" + otherRepo, true},
		{"pending cut for the repo is already tracked", "pending\trelease-cut: drellem2/macguffin v0.5.0", true},
		{"claimed cut for the repo is already tracked", "claimed\trelease-cut: drellem2/macguffin v0.5.0", true},
		{"available cut for the repo is already tracked", "available\trelease-cut: drellem2/macguffin v0.5.0", true},
		// Positive control: the same item, un-shelved and archived, no longer
		// blocks a new cut.
		{"archived cut does not block a new one", archivedCut + "\n" + otherRepo, false},
		{"done cut does not block a new one", "done\trelease-cut: drellem2/macguffin v0.4.0", false},
		{"another repo's cut does not block this one", otherRepo, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runDedup(t, dedup, slug, c.items); got != c.skip {
				t.Errorf("skipped=%v, want %v\nsnippet:\n%s", got, c.skip, dedup)
			}
		})
	}

	// Control on the fake: the pre-fix line must MISS the shelved item. If it
	// does not, the fake is not modelling mg's shelved-omission and the cases
	// above say nothing about it.
	t.Run("control: pre-fix line misses a shelved cut", func(t *testing.T) {
		if runDedup(t, preFixDedup, slug, shelvedCut) {
			t.Fatal("pre-mg-2a5d0 dedup skipped a shelved item: the fake mg does not omit shelved items without --all")
		}
	})
}
