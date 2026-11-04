package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSONObject(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not a JSON object after the merge: %v\n%s", path, err, b)
	}
	return m
}

func TestMergeOnboardingCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	changed, err := MergeOnboardingComplete(path)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want true,nil", changed, err)
	}
	if got := readJSONObject(t, path); got[onboardingKey] != true || len(got) != 1 {
		t.Fatalf("fresh file = %v, want only %s:true", got, onboardingKey)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("fresh file mode %v, want 0600", fi.Mode().Perm())
	}
}

// Acceptance (#286): an existing .claude.json keeps all its other keys.
func TestMergeOnboardingKeepsEveryOtherKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	orig := `{
  "numStartups": 41,
  "theme": "light",
  "hasCompletedOnboarding": false,
  "projects": {"/x": {"hasTrustDialogAccepted": true, "history": [{"display": "hi"}]}},
  "customApiKeyResponses": {"approved": ["abc"], "rejected": []},
  "oauthAccount": null,
  "bigNumber": 12345678901234567890
}`
	if err := os.WriteFile(path, []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	changed, err := MergeOnboardingComplete(path)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want true,nil", changed, err)
	}

	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(orig), &before); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &after); err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("key count %d -> %d:\n%s", len(before), len(after), b)
	}
	for k, v := range before {
		if k == onboardingKey {
			continue
		}
		if !jsonEqual(t, v, after[k]) {
			t.Errorf("key %q changed: %s -> %s", k, v, after[k])
		}
	}
	if string(after[onboardingKey]) != "true" {
		t.Errorf("%s = %s, want true", onboardingKey, after[onboardingKey])
	}
	// Byte-exact for a number float64 would round.
	if string(after["bigNumber"]) != "12345678901234567890" {
		t.Errorf("bigNumber = %s, want it untouched", after["bigNumber"])
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want the original 0640 kept", fi.Mode().Perm())
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// A profile that already says true is not rewritten. This is every spawn on a
// used profile, so the race with Claude's own writes never opens there.
func TestMergeOnboardingLeavesACompletedProfileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	orig := []byte(`{"z":1,  "hasCompletedOnboarding": true, "a":2}`)
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	fi0, _ := os.Stat(path)
	changed, err := MergeOnboardingComplete(path)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want false,nil", changed, err)
	}
	b, _ := os.ReadFile(path)
	fi1, _ := os.Stat(path)
	if string(b) != string(orig) || !os.SameFile(fi0, fi1) {
		t.Fatalf("a completed profile was rewritten:\n%s", b)
	}
}

func TestMergeOnboardingRefusesAFileItCannotRead(t *testing.T) {
	for _, body := range []string{`{"truncated": `, `[1,2]`, `null`, ``} {
		path := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if changed, err := MergeOnboardingComplete(path); err == nil || changed {
			t.Errorf("%q: changed=%v err=%v, want an error and no write", body, changed, err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%q: file rewritten to %q", body, b)
		}
	}
}

func TestMergeOnboardingWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "claude.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(`{"keep":"me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".claude.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeOnboardingComplete(link); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced by a regular file (err=%v)", err)
	}
	if got := readJSONObject(t, real); got["keep"] != "me" || got[onboardingKey] != true {
		t.Fatalf("link target = %v", got)
	}
}

func TestSeedOnboardingFollowsTheHarnessEnvironment(t *testing.T) {
	home := t.TempDir()
	cfgDir := t.TempDir()

	// Last assignment wins, as in exec.Cmd: the earlier HOME is pogod's own.
	if err := SeedOnboarding([]string{"HOME=/nonexistent-pogod-home", "HOME=" + home}); err != nil {
		t.Fatal(err)
	}
	if got := readJSONObject(t, filepath.Join(home, ".claude.json")); got[onboardingKey] != true {
		t.Fatalf("HOME profile not seeded: %v", got)
	}

	// CLAUDE_CONFIG_DIR moves the file, exactly as ClaudeGlobalConfigPath says.
	home2 := t.TempDir()
	if err := SeedOnboarding([]string{"HOME=" + home2, "CLAUDE_CONFIG_DIR=" + cfgDir}); err != nil {
		t.Fatal(err)
	}
	if got := readJSONObject(t, filepath.Join(cfgDir, ".claude.json")); got[onboardingKey] != true {
		t.Fatalf("CLAUDE_CONFIG_DIR profile not seeded: %v", got)
	}
	if _, err := os.Stat(filepath.Join(home2, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("seeded $HOME/.claude.json although CLAUDE_CONFIG_DIR was set (err=%v)", err)
	}
}

func TestProviderDeclaresTheOnboardingSeed(t *testing.T) {
	if Provider.ProfileSeedHook == nil {
		t.Fatal("claude.Provider has no ProfileSeedHook: token-only fresh installs stall on the theme picker (#286)")
	}
}
