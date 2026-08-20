package main

// The LIVE arm of the mg-0c96 control: `pogo check-prompt-edits`, run as a real
// process against a real corpus tree.
//
// internal/promptedit's tests prove the classification against fixtures. They
// cannot prove the command exists, is registered, is reachable by the name the
// mail body tells recipients to type, or exits the way its help text says — and
// a detector whose on-demand half cannot be invoked is a detector nobody can
// check the runner's work with.
//
// pogoBin comes from main_test.go's TestMain: a binary built from the working
// tree, NOT the one on PATH.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCheckPromptEdits invokes the command against a scratch root and returns
// stdout plus the exit code.
func runCheckPromptEdits(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(pogoBin, append([]string{"check-prompt-edits"}, args...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running check-prompt-edits: %v\n%s", err, out)
	}
	return string(out), code
}

// TestCheckPromptEdits_IsRegisteredAndReadsACleanTree. The mail body tells
// recipients to run this exact string; if the command is not registered under
// it, every notice this detector sends points at nothing.
func TestCheckPromptEdits_IsRegisteredAndReadsACleanTree(t *testing.T) {
	root := t.TempDir()
	writeCorpusFile(t, root, "mayor.md", stampedFile("# mayor\n"))
	writeCorpusFile(t, root, "crew/architect.md", "# a local prompt with no upstream\n")

	out, code := runCheckPromptEdits(t, "--root", root)
	if code != 0 {
		t.Fatalf("a clean tree must exit 0, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "no hand-edits") {
		t.Errorf("output does not report a clean sweep:\n%s", out)
	}
	// The census prints even on a clean run. A detector that printed only its
	// findings would read as though it had judged everything it enumerated, and
	// here most of what it enumerates is deliberately unjudged.
	if !strings.Contains(out, "OUT OF DOMAIN") || !strings.Contains(out, "no-upstream — 1 file(s)") {
		t.Errorf("the classified census must print on a clean run too:\n%s", out)
	}
}

// TestCheckPromptEdits_ExitsNonZeroOnAFinding, as the help text says. A
// detector whose exit status does not follow its findings cannot be used by any
// runner keyed on exit status.
func TestCheckPromptEdits_ExitsNonZeroOnAFinding(t *testing.T) {
	root := t.TempDir()
	writeCorpusFile(t, root, "mayor.md", editedFile("# mayor\n", "# mayor\nlocal edit\n"))

	out, code := runCheckPromptEdits(t, "--root", root)
	if code == 0 {
		t.Fatalf("a hand-edited prompt must exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "HAND-EDITED") || !strings.Contains(out, "mayor.md") {
		t.Errorf("the finding is not named in the output:\n%s", out)
	}
	// The two commands that reproduce the reading, so a recipient can check the
	// tool rather than take its word.
	if !strings.Contains(out, "head -1") || !strings.Contains(out, "tail -n +2") {
		t.Errorf("the report must hand over the commands that reproduce it:\n%s", out)
	}
}

// TestCheckPromptEdits_JSONCarriesTheDenominatorsAndTheCensus. "0 findings"
// over 0 judged files is not the same answer as "0 findings" over 9, and an
// exclusion a consumer cannot see is indistinguishable from a scan that missed
// it.
func TestCheckPromptEdits_JSONCarriesTheDenominatorsAndTheCensus(t *testing.T) {
	root := t.TempDir()
	writeCorpusFile(t, root, "mayor.md", stampedFile("# mayor\n"))
	writeCorpusFile(t, root, "crew/architect.md", "# local\n")
	writeCorpusFile(t, root, "crew/doctor.md", editedFile("# doctor\n", "# doctor edited\n"))

	out, code := runCheckPromptEdits(t, "--root", root, "--json")
	if code == 0 {
		t.Fatalf("a finding must exit non-zero:\n%s", out)
	}
	var got struct {
		Root         string `json:"root"`
		Enumerated   int    `json:"enumerated"`
		ShippedPaths int    `json:"shipped_paths"`
		Judged       int    `json:"judged"`
		Findings     []struct {
			Path         string `json:"path"`
			Agent        string `json:"agent"`
			RecordedHash string `json:"recorded_hash"`
			ActualHash   string `json:"actual_hash"`
		} `json:"findings"`
		OutOfDomain []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"out_of_domain"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json did not produce parseable JSON (%v):\n%s", err, out)
	}
	if got.Enumerated != 3 || got.Judged != 2 {
		t.Errorf("enumerated=%d judged=%d, want 3 and 2", got.Enumerated, got.Judged)
	}
	if got.ShippedPaths == 0 {
		t.Error("shipped_paths is 0 — the domain's denominator is missing, so a binary that judged " +
			"nothing would read identically to a clean fleet")
	}
	if len(got.Findings) != 1 || got.Findings[0].Path != "crew/doctor.md" {
		t.Fatalf("findings = %+v, want crew/doctor.md", got.Findings)
	}
	if got.Findings[0].Agent != "doctor" {
		t.Errorf("finding is addressed to %q, want doctor — every finding must name the agent that "+
			"can judge whether the edit is still load-bearing", got.Findings[0].Agent)
	}
	if got.Findings[0].RecordedHash == got.Findings[0].ActualHash {
		t.Error("the finding carries no evidence: the recorded and actual hashes are equal")
	}
	if len(got.OutOfDomain) != 1 || got.OutOfDomain[0].Reason != "no-upstream" {
		t.Errorf("out_of_domain = %+v, want one no-upstream row", got.OutOfDomain)
	}
}

// stampedFile renders a file the way InstallPrompts would: a v1 stamp recording
// the hash of the body that follows.
func stampedFile(body string) string {
	h := sha256Hex(body)
	return "<!-- pogo-prompt: embed=sha256:" + h + " body=sha256:" + h + " -->\n" + body
}

// editedFile renders a stamp claiming a body OTHER than the one written — the
// shape a prompt takes after somebody edits it in place.
func editedFile(recorded, body string) string {
	h := sha256Hex(recorded)
	return "<!-- pogo-prompt: embed=sha256:" + h + " body=sha256:" + h + " -->\n" + body
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeCorpusFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
