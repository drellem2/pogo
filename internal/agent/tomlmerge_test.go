package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeTOMLDropInsAbsentDir confirms that an absent drop-in directory is
// not an error: callers receive base unchanged and a nil names slice. Drop-ins
// are an opt-in customization slot, not a required part of the config.
func TestMergeTOMLDropInsAbsentDir(t *testing.T) {
	base := []byte("name = \"pm-pogo\"\nrepos = [\"a\", \"b\"]\n")
	got, names, err := MergeTOMLDropIns(base, filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	if string(got) != string(base) {
		t.Errorf("absent dir: got %q, want %q", got, base)
	}
	if names != nil {
		t.Errorf("absent dir: expected nil names, got %v", names)
	}
}

// TestMergeTOMLDropInsScalarOverride covers the systemd-style scalar override:
// the drop-in's value for an existing key replaces the base value, and the
// base's other keys flow through unchanged.
func TestMergeTOMLDropInsScalarOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-rename.toml"),
		[]byte("display = \"Pogo Override\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("name = \"pm-pogo\"\ndisplay = \"Pogo / Macguffin\"\n")
	got, names, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	if len(names) != 1 || names[0] != "01-rename.toml" {
		t.Errorf("names = %v, want [01-rename.toml]", names)
	}
	out := string(got)
	if !strings.Contains(out, "name = \"pm-pogo\"") {
		t.Errorf("base name should pass through: %s", out)
	}
	if !strings.Contains(out, "display = \"Pogo Override\"") {
		t.Errorf("display should be overridden: %s", out)
	}
	if strings.Contains(out, "display = \"Pogo / Macguffin\"") {
		t.Errorf("base display should be replaced, not duplicated: %s", out)
	}
}

// TestMergeTOMLDropInsArrayReplace is the canonical acceptance case from the
// design doc: pm/pogo.toml repos=["a","b"] + drop-in repos=["c"] yields
// repos=["c"], NOT ["a","b","c"]. Arrays are replaced wholesale, not appended.
func TestMergeTOMLDropInsArrayReplace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-extra.toml"),
		[]byte("repos = [\"c\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("repos = [\"a\", \"b\"]\n")
	got, _, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "repos = [\"c\"]") {
		t.Errorf("expected drop-in array to replace base array, got:\n%s", out)
	}
	if strings.Contains(out, `"a"`) || strings.Contains(out, `"b"`) {
		t.Errorf("base array values must not survive replacement: %s", out)
	}
}

// TestMergeTOMLDropInsNewKey confirms drop-ins can add brand-new keys that
// the base config didn't declare.
func TestMergeTOMLDropInsNewKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "10-new.toml"),
		[]byte("priority = \"high\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("name = \"pm-pogo\"\n")
	got, _, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "name = \"pm-pogo\"") {
		t.Errorf("base key should pass through: %s", out)
	}
	if !strings.Contains(out, "priority = \"high\"") {
		t.Errorf("drop-in should add new key: %s", out)
	}
}

// TestMergeTOMLDropInsLexicalOrder confirms that multiple drop-ins are applied
// in lexical (not directory) order, and that the latest file's value wins for
// any key all of them touch — same `*.d/` convention as systemd / cron.d.
func TestMergeTOMLDropInsLexicalOrder(t *testing.T) {
	dir := t.TempDir()
	// Write in non-lexical order to confirm the merge sorts internally.
	if err := os.WriteFile(filepath.Join(dir, "50-mid.toml"),
		[]byte("name = \"pm-mid\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "10-first.toml"),
		[]byte("name = \"pm-first\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "90-last.toml"),
		[]byte("name = \"pm-last\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("name = \"pm-base\"\n")
	got, names, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	wantNames := []string{"10-first.toml", "50-mid.toml", "90-last.toml"}
	if len(names) != len(wantNames) {
		t.Fatalf("names = %v, want %v", names, wantNames)
	}
	for i, n := range wantNames {
		if names[i] != n {
			t.Errorf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
	out := string(got)
	if !strings.Contains(out, "name = \"pm-last\"") {
		t.Errorf("expected last-file value to win, got:\n%s", out)
	}
}

// TestMergeTOMLDropInsIgnoresNonToml confirms non-.toml files and
// subdirectories under the drop-in directory are ignored. Same convention as
// the markdown drop-in loader: the directory is scanned for fragments with
// the matching extension and nothing else.
func TestMergeTOMLDropInsIgnoresNonToml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00-real.toml"),
		[]byte("name = \"override\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte("# notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	got, names, err := MergeTOMLDropIns([]byte("name = \"base\"\n"), dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	if len(names) != 1 || names[0] != "00-real.toml" {
		t.Errorf("names = %v, want [00-real.toml]", names)
	}
	if !strings.Contains(string(got), "name = \"override\"") {
		t.Errorf("expected override to apply, got:\n%s", got)
	}
}

// TestMergeTOMLDropInsMultiLineArray confirms the parser correctly groups
// continuation lines of a multi-line array as part of the same logical entry,
// so a drop-in single-line `repos = [...]` replaces the entire multi-line
// base value (not just its first line).
func TestMergeTOMLDropInsMultiLineArray(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-replace.toml"),
		[]byte("repos = [\"only\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("repos = [\n  \"a\",\n  \"b\",\n  \"c\",\n]\nname = \"pm\"\n")
	got, _, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "repos = [\"only\"]") {
		t.Errorf("expected single-line replacement, got:\n%s", out)
	}
	for _, dead := range []string{`"a"`, `"b"`, `"c"`} {
		if strings.Contains(out, dead) {
			t.Errorf("base multi-line value %s leaked into merged output:\n%s", dead, out)
		}
	}
	if !strings.Contains(out, "name = \"pm\"") {
		t.Errorf("base sibling key should pass through: %s", out)
	}
}

// TestMergeTOMLDropInsTableSection confirms drop-ins deep-merge into named
// `[table]` sections: a same-named key inside `[server]` replaces the base
// value, base sibling keys in the same table flow through, and a brand-new
// key from the drop-in is appended to that table.
func TestMergeTOMLDropInsTableSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-server.toml"),
		[]byte("[server]\nport = 20000\nbind = \"0.0.0.0\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("[server]\nport = 10000\nname = \"primary\"\n")
	got, _, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "port = 20000") {
		t.Errorf("expected port override under [server], got:\n%s", out)
	}
	if strings.Contains(out, "port = 10000") {
		t.Errorf("base port should be replaced: %s", out)
	}
	if !strings.Contains(out, "name = \"primary\"") {
		t.Errorf("base sibling key in [server] should flow through: %s", out)
	}
	if !strings.Contains(out, "bind = \"0.0.0.0\"") {
		t.Errorf("drop-in's new key in [server] should be appended: %s", out)
	}
	// Single [server] header — drop-in must merge into the existing block,
	// not start a duplicate.
	if got := strings.Count(out, "[server]"); got != 1 {
		t.Errorf("expected exactly one [server] header, got %d:\n%s", got, out)
	}
}

// TestMergeTOMLDropInsPreservesCommentsAndStamp confirms that base comments
// around non-overridden keys flow through, and that a leading pogo-prompt
// stamp on the base file is stripped (not emitted into the merged output,
// since the stamp is install-recorded metadata, not user-visible content).
//
// Comments immediately before an overridden key travel WITH that key — they
// get replaced alongside the key's value lines. That's by design: a comment
// like `# Name of this PM` is documentation of the key it precedes, so it
// should not survive a replacement that re-documents the key. The comment on
// the *non-overridden* key (`name`) is the one that must pass through.
func TestMergeTOMLDropInsPreservesCommentsAndStamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01-x.toml"),
		[]byte("display = \"Override\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stamped := stampedContent("pm/pogo.toml", []byte(
		"# Name of this PM\nname = \"pm-pogo\"\ndisplay = \"orig\"\n# trailing note\n",
	))
	got, _, err := MergeTOMLDropIns(stamped, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	out := string(got)
	if strings.Contains(out, "pogo-prompt") {
		t.Errorf("merged output should not carry the pogo-prompt stamp:\n%s", out)
	}
	if !strings.Contains(out, "# Name of this PM") {
		t.Errorf("comment above non-overridden key should flow through:\n%s", out)
	}
	if !strings.Contains(out, "# trailing note") {
		t.Errorf("trailing comment should flow through:\n%s", out)
	}
	if !strings.Contains(out, "display = \"Override\"") {
		t.Errorf("override should apply:\n%s", out)
	}
}

// TestMergeTOMLDropInsEmptyDropinDir confirms that a directory containing no
// .toml files (only README.md, etc.) is treated the same as an absent dir:
// base passes through unchanged.
func TestMergeTOMLDropInsEmptyDropinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := []byte("name = \"pm\"\n")
	got, names, err := MergeTOMLDropIns(base, dir)
	if err != nil {
		t.Fatalf("MergeTOMLDropIns: %v", err)
	}
	if string(got) != string(base) {
		t.Errorf("empty dropin dir: got %q, want %q", got, base)
	}
	if names != nil {
		t.Errorf("empty dropin dir: expected nil names, got %v", names)
	}
}

// TestSynthesizeExtendsPromptTOMLDropIns is the end-to-end wiring test that
// confirms the prompt synthesizer feeds drop-in TOMLs through MergeTOMLDropIns
// before inlining the config block: an array key declared in the base is
// replaced (not appended) by a same-named key in a drop-in under
// dropins/pm/<instance>/.
func TestSynthesizeExtendsPromptTOMLDropIns(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"),
		[]byte("# PM Template\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pogo.toml"),
		[]byte("name = \"pm-pogo\"\nrepos = [\"a\", \"b\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dropDir := filepath.Join(PromptDir(), "dropins", "pm", "pogo")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "01-extra.toml"),
		[]byte("repos = [\"c\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	crewPath := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(crewPath,
		[]byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	if _, err := SynthesizeExtendsPrompt(crewPath, outPath); err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "repos = [\"c\"]") {
		t.Errorf("expected drop-in to replace repos array, got:\n%s", out)
	}
	if strings.Contains(out, `"a"`) || strings.Contains(out, `"b"`) {
		t.Errorf("base repos values must not survive replacement:\n%s", out)
	}
	if !strings.Contains(out, "name = \"pm-pogo\"") {
		t.Errorf("base name key should still flow through:\n%s", out)
	}
}
