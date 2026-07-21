package ghteardown

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// carrierLinePrefixes are the body lines that make an item a gh-issue carrier.
// They live in the work-item BODY, not the frontmatter — see mayor.md's filing
// recipe. internal/workitem parses frontmatter only and therefore cannot see
// them, which is why this package parses the body itself rather than reusing
// that reader.
const (
	keyWorkflow = "workflow"
	keyStage    = "stage"
	keyGH       = "gh"
	keyGHOpen   = "gh-open"

	workflowGHIssue = "gh-issue"
)

// ParseBody extracts the carrier lines from a work-item body. It returns ok
// false when the body is not a gh-issue carrier at all.
//
// Two deliberate parsing choices:
//
// Blockquoted lines are IGNORED. The mayor annotates live carriers with
// `> **CARRIER NOTE ...**` blocks that quote refs and stages in prose; matching
// those would let commentary masquerade as state. Only unquoted `key: value`
// lines are structural.
//
// The FIRST occurrence of each key wins, and the keys are matched anywhere in
// the body rather than only in a leading block. Carriers are filed with the
// lines first, but the mayor prepends notes to live ones, so anchoring to
// position would silently stop recognising exactly the carriers under active
// management — the ones that matter most.
func ParseBody(body string) (workflow, stage, ghRef, declaredOpen string, ok bool) {
	seen := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch key {
		case keyWorkflow, keyStage, keyGH, keyGHOpen:
			if _, dup := seen[key]; !dup {
				seen[key] = val
			}
		}
	}

	if !strings.EqualFold(seen[keyWorkflow], workflowGHIssue) {
		return "", "", "", "", false
	}
	return seen[keyWorkflow], seen[keyStage], seen[keyGH], seen[keyGHOpen], true
}

// ParseRef splits a `gh:` ref of the form owner/repo#number.
func ParseRef(ref string) (repo string, number int, err error) {
	repo, numStr, found := strings.Cut(strings.TrimSpace(ref), "#")
	if !found {
		return "", 0, fmt.Errorf("gh ref %q has no '#<number>'", ref)
	}
	repo = strings.TrimSpace(repo)
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, fmt.Errorf("gh ref %q: want owner/repo#number", ref)
	}
	number, err = strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("gh ref %q: bad issue number", ref)
	}
	return repo, number, nil
}

// mgListItem is the subset of `mg list --json` (NDJSON) this package reads.
type mgListItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// mgShowItem is the subset of `mg show --json` this package reads. `mg list`
// does not emit the body, so establishing whether an item is a carrier costs
// one `mg show` per candidate.
type mgShowItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Body   string `json:"body"`
}

// MGSource reads gh-issue carriers out of a macguffin store by shelling out to
// the `mg` CLI.
type MGSource struct {
	// Root overrides the store location, passed to mg as --root. Empty means
	// "resolve a default" — see resolveRoot, which does NOT resolve to the live
	// store under a test binary.
	Root string
	// Bin is the mg binary. Empty means "mg" on PATH.
	Bin string
	// IncludeArchived widens the scan to archived carriers.
	//
	// Off by default, and that is a real coverage boundary rather than an
	// oversight: this store holds 81 archived gh-issue carriers against 2 done
	// ones, and each carrier costs one network round-trip. Scanning settled
	// history on every heartbeat would trade a detector that runs for one that
	// is slow, rate-limited, and eventually switched off. `status=done` is where
	// the unfulfilled claim of completion lives.
	//
	// The gap it leaves is real and worth naming: a carrier ARCHIVED while its
	// issue is still open is the most thoroughly forgotten case of all, and the
	// default scan will not see it. That is what the opt-in is for, and why the
	// CLI reports which statuses it covered instead of implying it saw everything.
	IncludeArchived bool
}

// testRootOnce memoises one temp directory per test binary, so every
// test-binary call resolves to the same empty scratch store.
var (
	testRootOnce sync.Once
	testRootDir  string
)

// resolveRoot returns the store root to hand mg.
//
// Under a test binary with no explicit Root, it returns a per-binary temp
// directory — NEVER the live ~/.macguffin. This is a test-safe DEFAULT, not an
// opt-in helper, and the distinction is the entire lesson of mg-da48: the
// witness store shipped a `sandboxWitness(t)` helper, and the tests that
// remembered to call it were the ones whose subject WAS the witness. The tests
// that touched the store incidentally never called it and wrote phantom
// polecats into the live store, which pogod's orphan detector then read back
// and mailed the mayor authoritative `kill <pid>` instructions about. The fix
// there — witness.go's testing.Testing() default — is the shape copied here.
//
// An opt-in guard is only ever remembered by the tests that least need it.
func (s MGSource) resolveRoot() string {
	if s.Root != "" {
		return s.Root
	}
	if testing.Testing() {
		testRootOnce.Do(func() {
			dir, err := os.MkdirTemp("", "ghteardown-test-store-")
			if err != nil {
				// Deliberately no error return: every fallback path must lead
				// somewhere that is not the live store. A temp path we failed to
				// create yields "store unreadable", which is a loud, correct
				// failure; falling back to $HOME/.macguffin would be a silent,
				// incorrect success.
				dir = filepath.Join(os.TempDir(), "ghteardown-test-store-fallback")
			}
			testRootDir = dir
		})
		return testRootDir
	}
	return "" // production: let mg apply its own default ($MG_ROOT, then ~/.macguffin)
}

func (s MGSource) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "mg"
}

func (s MGSource) run(args ...string) ([]byte, error) {
	if root := s.resolveRoot(); root != "" {
		args = append([]string{"--root", root}, args...)
	}
	cmd := exec.Command(s.bin(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", s.bin(), strings.Join(args, " "), msg)
	}
	return out, nil
}

// Statuses reports which mg statuses this source scans, so a report can state
// its own coverage rather than implying it saw everything.
func (s MGSource) Statuses() []string {
	if s.IncludeArchived {
		return []string{"done", "archived"}
	}
	return []string{"done"}
}

// Carriers returns every gh-issue carrier in the scanned statuses.
//
// A store that cannot be read is an ERROR, never an empty slice. Zero carriers
// and an unreadable store both render as "nothing to report", and conflating
// them would let this detector go quietly blind — the precise failure mode it
// was built to catch, reproduced inside itself.
func (s MGSource) Carriers() ([]Carrier, error) {
	var out []Carrier
	for _, status := range s.Statuses() {
		listed, err := s.run("list", "--status="+status, "--json")
		if err != nil {
			return nil, fmt.Errorf("listing %s work items: %w", status, err)
		}
		for _, line := range strings.Split(string(listed), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var item mgListItem
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return nil, fmt.Errorf("parsing mg list output: %w", err)
			}
			if item.ID == "" {
				continue
			}
			c, ok, err := s.carrier(item.ID)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// carrier loads one item and decides whether it is a gh-issue carrier.
func (s MGSource) carrier(id string) (Carrier, bool, error) {
	raw, err := s.run("show", id, "--json")
	if err != nil {
		return Carrier{}, false, fmt.Errorf("reading work item %s: %w", id, err)
	}
	var item mgShowItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return Carrier{}, false, fmt.Errorf("parsing work item %s: %w", id, err)
	}

	_, stage, ghRef, declaredOpen, ok := ParseBody(item.Body)
	if !ok || ghRef == "" {
		return Carrier{}, false, nil
	}
	repo, number, err := ParseRef(ghRef)
	if err != nil {
		// A carrier whose ref does not parse cannot be checked, and dropping it
		// silently would hide a carrier from the audit forever. Surface it as an
		// unresolvable carrier so it lands in the Indeterminate bucket rather
		// than vanishing.
		return Carrier{
			ID: item.ID, Title: item.Title, Status: item.Status, Stage: stage,
			Repo: ghRef, Number: 0,
		}, true, nil
	}
	return Carrier{
		ID: item.ID, Title: item.Title, Status: item.Status, Stage: stage,
		Repo: repo, Number: number, DeclaredOpenReason: declaredOpen,
	}, true, nil
}
