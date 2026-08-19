package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// the ratchet: no shell file re-derives the pogo state root (mg-5082)
// ---------------------------------------------------------------------------
//
// PogoHome above is the ONE derivation of "where my state lives". It applies
// mg-3dc3's normalisation: a POGO_HOME equal to $HOME means $HOME/.pogo,
// because this box's ~/.zshrc exports the former from an old shell integration
// that meant "where the dotfiles are".
//
// `${POGO_HOME:-$HOME/.pogo}` cannot reproduce that, and it is not a partial
// normalisation — it is a DIFFERENT ANSWER on exactly the environment the
// normalisation exists for. Measured on the reporting host on 2026-08-19:
//
//	environment                        ${POGO_HOME:-$HOME/.pogo}   PogoHome()
//	POGO_HOME unset                    /Users/daniel/.pogo         /Users/daniel/.pogo
//	POGO_HOME=$HOME  (the legacy one)  /Users/daniel               /Users/daniel/.pogo
//	POGO_HOME=$HOME/.pogo (sandbox)    <sandbox>/home/.pogo        <sandbox>/home/.pogo
//
// One row in three disagrees, and that row is live: of ten running processes
// on that host with POGO_HOME set, nine carried /Users/daniel/.pogo and one —
// an interactively-descended `pogo status --live` from ~/.zshrc:37 — carried
// /Users/daniel. Same expression, same machine, same hour, different file.
// Reading the wrong one is the worst available failure: the path usually
// EXISTS and is stale, so a grep of it returns a well-formed wrong answer that
// an `ls -l` then confirms (drellem2/pogo#145, mg-c18d).
//
// The rule this enforces, and where each half already lived before this file:
//
//	Go                       call config.PogoHome(); never read the variable,
//	                         never memoise the answer (mg-abbf).
//	a prompt / agent shell   ask the CLI. Enforced by the runnableLines check
//	                         in internal/agent/prompt_test.go (mg-c18d).
//	a tracked shell script   normalise the way PogoHome does, and assert the
//	                         parity. resolve_pogo_home in
//	                         scripts/fleet-liveness-probe.sh is the worked
//	                         example and case 7 of its _test.sh is the parity
//	                         test. Calling the CLI is NOT available to a
//	                         tracked script that may run before self-deploy
//	                         has shipped the binary — see CONTRIBUTING.md,
//	                         "Writing a hook: it must self-activate from
//	                         source", for why those two clocks must not be
//	                         coupled.
//
// The third of those three — tracked shell — is the one nothing failed for, and
// that is why this file exists.
//
// WHAT THIS CHECK DELIBERATELY DOES NOT CATCH. A bare `$POGO_HOME/...` after
// something in the same script SET POGO_HOME is correct — scripts/pogo-sandbox
// does exactly that — and no textual check can tell it from a read of an
// inherited one. So the gate is scoped to the fallback form, which has no
// correct use: writing `:-$HOME/...` says in as many words that the variable
// may be absent, i.e. that this is a read of the ambient environment.

// pogoHomeLedgerName is the file, beside this one, naming every shell file that
// still carries the fallback form.
const pogoHomeLedgerName = "pogohome-shell-ledger.txt"

// pogoHomeFallback matches `${POGO_HOME:-<a path>}` — a default that begins
// with `$`, `/` or `~`, which is what makes the expression a DERIVATION of the
// state root rather than a read of the variable. Two shapes are deliberately
// outside it, and both are live in this tree:
//
//	${POGO_HOME:-}          an unset-safe read. scripts/fleet-liveness-probe.sh:211
//	                        and shell/bashrc_test.sh:84 are this, and correct.
//	${POGO_HOME:-(unset)}   a display default. scripts/migrate-pogo-home.sh:51
//	                        prints it; no path is derived.
var pogoHomeFallback = regexp.MustCompile(`\$\{POGO_HOME:-[$/~]`)

// pogoHomeSkipDir names directories holding no shell of ours.
func pogoHomeSkipDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", "testdata", "_testdata", "bin":
		return true
	}
	return false
}

// isShellFile reports whether path is a shell file this rule covers. Extension
// is not enough: scripts/pogo-sandbox is a sourced library with no suffix, and
// it is the one file most likely to grow the form.
func isShellFile(rel string, body []byte) bool {
	switch filepath.Ext(rel) {
	case ".sh", ".bash", ".zsh":
		return true
	}
	return strings.HasPrefix(string(body), "#!") &&
		strings.Contains(strings.SplitN(string(body), "\n", 2)[0], "sh")
}

// runnableShellLines returns the 1-indexed lines of body that are not blank and
// not whole-line comments. The form appears legitimately in prose all over this
// tree — changelog entries, the deploy suite's own header explaining the defect,
// this very file — and a gate that could not tell a quotation from a command
// would be unfixable without deleting the explanations.
func runnableShellLines(body []byte) map[int]string {
	out := map[int]string{}
	for i, line := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out[i+1] = t
	}
	return out
}

// surveyPogoHomeFallback walks root and returns, sorted, every repo-relative
// shell file with the fallback form on a runnable line.
func surveyPogoHomeFallback(root string) ([]string, map[string]string, error) {
	var found []string
	where := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel != "." && pogoHomeSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel = filepath.ToSlash(rel)
		if !isShellFile(rel, body) {
			return nil
		}
		for n, line := range runnableShellLines(body) {
			if pogoHomeFallback.MatchString(line) {
				if _, seen := where[rel]; !seen {
					found = append(found, rel)
					where[rel] = strings.TrimSpace(line)
					_ = n
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(found)
	return found, where, nil
}

// readPogoHomeLedger returns the ledgered paths, sorted.
func readPogoHomeLedger(tb testing.TB, root string) []string {
	tb.Helper()
	path := filepath.Join(root, "internal", "config", pogoHomeLedgerName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // the ratchet reached zero; the file was deleted with the last line
	}
	if err != nil {
		tb.Fatalf("could not read %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// pogoHomeRepoRoot walks up from the working directory to the module root.
// Hard-coding "../.." would be a fact about this file's depth that nothing
// checks.
func pogoHomeRepoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatalf("could not read the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatalf("no go.mod above %s — cannot find the repository root to walk", dir)
		}
		dir = parent
	}
}

const pogoHomeRemedy = "Ask the CLI (`pogo events list`, `pogo doctor`) if the binary is available " +
	"to you, or copy resolve_pogo_home from scripts/fleet-liveness-probe.sh and give it a parity " +
	"test against config.PogoHome. Do NOT add a ledger line. See CONTRIBUTING.md, \"Where state lives\"."

// TestNoShellFileReDerivesThePogoStateRoot is the ratchet. Both directions are
// enforced, because only one of them is the ratchet:
//
//	carries the form and is not ledgered  ->  fail. The new occurrence.
//	ledgered and no longer carries it     ->  fail. Converting must delete the
//	                                          line, or the list rusts into an
//	                                          allowlist nobody can shorten.
//	ledgered and now gone                 ->  fail. Same, for renames/deletions.
func TestNoShellFileReDerivesThePogoStateRoot(t *testing.T) {
	root := pogoHomeRepoRoot(t)

	found, where, err := surveyPogoHomeFallback(root)
	if err != nil {
		t.Fatalf("could not walk %s: %v", root, err)
	}

	// A walk that finds no shell at all passes every assertion below while
	// checking nothing — this ticket's own defect, one level up.
	shellSeen := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() != "." && pogoHomeSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".sh") {
			shellSeen++
		}
		return nil
	})
	if shellSeen < 20 {
		t.Fatalf("the walk of %s found only %d shell files — this repository has far more, so the "+
			"check below is not looking at the tree it thinks it is", root, shellSeen)
	}

	ledger := readPogoHomeLedger(t, root)
	inLedger := map[string]bool{}
	for _, p := range ledger {
		inLedger[p] = true
	}
	carries := map[string]bool{}
	for _, p := range found {
		carries[p] = true
	}

	for _, p := range found {
		if !inLedger[p] {
			t.Errorf("%s re-derives the pogo state root in shell: %q. That expression is not a "+
				"normalisation of POGO_HOME — where POGO_HOME equals $HOME (the legacy value this "+
				"box's ~/.zshrc still exports) it names $HOME, while config.PogoHome names "+
				"$HOME/.pogo, so it reads a DIFFERENT file that may exist and be stale. %s",
				p, where[p], pogoHomeRemedy)
		}
	}
	for _, p := range ledger {
		if carries[p] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); os.IsNotExist(err) {
			t.Errorf("%s is in %s but no longer exists — delete its line. The ledger is a ratchet "+
				"and may only shrink; a stale line is an allowlist entry nobody can honestly "+
				"remove.", p, pogoHomeLedgerName)
			continue
		}
		t.Errorf("%s is in %s but no longer carries the fallback form — delete its line. Converting "+
			"a file is a one-line deletion; leaving it listed is how the list rusts into an "+
			"allowlist.", p, pogoHomeLedgerName)
	}
}

// TestThePogoHomeFallbackCheckSeesTheFormAndOnlyTheForm is the positive
// control. A check only ever observed passing on a tree that already satisfies
// it has not been tested — and both halves matter here, because a gate that
// flagged the quotations would be unfixable without deleting the explanations
// this rule is made of.
func TestThePogoHomeFallbackCheckSeesTheFormAndOnlyTheForm(t *testing.T) {
	root := t.TempDir()

	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("caught.sh", "#!/bin/bash\nSTAMP=\"${POGO_HOME:-$HOME/.pogo}/deploy-attempt.stamp\"\n")
	write("nested.sh", "#!/bin/bash\nS=\"${POGO_DEPLOY_STAMP:-${POGO_HOME:-$HOME/.pogo}/x.stamp}\"\n")
	write("noshebang", "#!/bin/sh\nD=\"${POGO_HOME:-$HOME/.pogo}\"\n")
	write("quoted.sh", "#!/bin/bash\n# never write ${POGO_HOME:-$HOME/.pogo} — it is not a normalisation\necho ok\n")
	write("unsetsafe.sh", "#!/bin/bash\nif [ -n \"${POGO_HOME:-}\" ]; then echo set; fi\n")
	write("normalised.sh", "#!/bin/bash\nh=\"${POGO_HOME:-}\"; [ -z \"$h\" ] && h=\"$HOME/.pogo\"\n")
	write("display.sh", "#!/bin/bash\necho \"current POGO_HOME=${POGO_HOME:-(unset)}\"\n")
	write("notes.md", "Do not write `${POGO_HOME:-$HOME/.pogo}` in a script.\n")

	found, _, err := surveyPogoHomeFallback(root)
	if err != nil {
		t.Fatalf("survey: %v", err)
	}
	got := strings.Join(found, ",")
	want := "caught.sh,nested.sh,noshebang"
	if got != want {
		t.Fatalf("positive control FAILED: the check reported [%s], want [%s].\n"+
			"  caught.sh/nested.sh/noshebang are the defect and must be reported "+
			"(noshebang proves the extension is not what makes a file shell — "+
			"scripts/pogo-sandbox has no suffix);\n"+
			"  quoted.sh and notes.md are the form in PROSE and must not be — a gate that "+
			"flagged them could not coexist with the documentation of the rule;\n"+
			"  unsetsafe.sh, normalised.sh and display.sh are the shapes that derive no path and must not be.",
			got, want)
	}
}
