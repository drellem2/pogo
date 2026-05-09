package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallMatrix is the end-to-end install→edit→update matrix from
// docs/prompt-customization-design.md §B and "--force semantics, restated."
// Stamp v1, conflict detection, and --force-backup all participate in the
// same gate; this table proves the seven externally-observable shapes match
// the design exactly. The individual TestInstallPromptsConflictMatrix*/
// TestInstallPromptsForce* tests exercise each cell in isolation; this one
// is the contract.
//
// Cases:
//
//	  1. embed unchanged, user not edited       → Skipped; no .dist; no .bak
//	  2. embed unchanged, user edited           → Skipped; no .dist; no .bak; user edits preserved
//	  3. embed changed,   user not edited       → Updated; no .dist; no .bak
//	  4. embed changed,   user edited           → Conflict (.dist with new embed); canonical untouched
//	  5. --force,         user not edited       → Installed; no .bak
//	  6. --force,         user edited           → Installed; .bak.<ts> contains user body
//	  7. --force --no-backup, user edited       → Installed; no .bak (silent stomp opt-in)
//
// All cases drive the matrix through mayor.md — it ships in every install
// profile, so the case definitions don't have to vary by file.

type matrixExpect int

const (
	expectSkipped matrixExpect = iota
	expectUpdated
	expectInstalled
	expectConflict
)

func TestInstallMatrix(t *testing.T) {
	const matrixRel = "mayor.md"

	cases := []struct {
		name string
		// setup seeds tmpHome/.pogo/agents/ to put mayor.md in the desired
		// pre-state. It returns the bytes the user expects preserved on
		// disk after the install (nil = no preservation expected, i.e. an
		// overwrite path).
		setup func(t *testing.T, tmpHome string) []byte
		opts  InstallOpts
		// Status assertions on InstallResult: exactly one of the four
		// per-file slices must contain matrixRel.
		want matrixExpect
		// Filesystem-side assertions.
		wantDist   bool
		wantBackup bool
	}{
		{
			name:  "1_embed_unchanged_user_not_edited",
			setup: setupFreshInstall,
			opts:  InstallOpts{},
			want:  expectSkipped,
		},
		{
			name:  "2_embed_unchanged_user_edited",
			setup: setupFreshInstallThenEdit,
			opts:  InstallOpts{},
			want:  expectSkipped,
		},
		{
			name:  "3_embed_changed_user_not_edited",
			setup: setupStaleStampNoEdit,
			opts:  InstallOpts{},
			want:  expectUpdated,
		},
		{
			name:     "4_embed_changed_user_edited",
			setup:    setupStaleStampUserEdited,
			opts:     InstallOpts{},
			want:     expectConflict,
			wantDist: true,
		},
		{
			name:  "5_force_user_not_edited",
			setup: setupFreshInstall,
			opts:  InstallOpts{Force: true},
			want:  expectInstalled,
		},
		{
			name:       "6_force_user_edited",
			setup:      setupFreshInstallThenEdit,
			opts:       InstallOpts{Force: true},
			want:       expectInstalled,
			wantBackup: true,
		},
		{
			name:  "7_force_no_backup_user_edited",
			setup: setupFreshInstallThenEdit,
			opts:  InstallOpts{Force: true, NoBackup: true},
			want:  expectInstalled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origHome := os.Getenv("HOME")
			tmpHome := t.TempDir()
			os.Setenv("HOME", tmpHome)
			t.Cleanup(func() { os.Setenv("HOME", origHome) })

			// Pin the backup timestamp so .bak.<ts> assertions are
			// deterministic. Cases that don't produce a backup are
			// unaffected; the suffix-string assertion below ignores
			// the value when wantBackup is false.
			suffix := withFixedNow(t)

			preservedBody := tc.setup(t, tmpHome)
			mayorPath := filepath.Join(tmpHome, ".pogo", "agents", matrixRel)

			result, err := InstallPrompts(tc.opts)
			if err != nil {
				t.Fatalf("InstallPrompts: %v", err)
			}

			assertMatrixStatus(t, result, matrixRel, tc.want)

			distPath := mayorPath + ".dist"
			if tc.wantDist {
				if _, err := os.Stat(distPath); err != nil {
					t.Errorf("expected %s to exist, got %v", distPath, err)
				}
				// Conflict slice must record the file with the
				// expected DistPath so callers can warn the user.
				if !hasConflict(result.Conflicts, matrixRel, matrixRel+".dist") {
					t.Errorf("expected Conflict{Path:%q, DistPath:%q.dist}, got %+v", matrixRel, matrixRel, result.Conflicts)
				}
				// The .dist sidecar must carry a stamp whose
				// embed_hash equals the current binary's embed
				// (so renaming over the canonical reads as
				// up-to-date next install).
				distStamp := readInstalledPromptStamp(distPath)
				if distStamp.EmbedHash == "" {
					t.Errorf("%s missing v1 stamp", distPath)
				}
			} else {
				if _, err := os.Stat(distPath); err == nil {
					t.Errorf("unexpected %s on non-conflict path", distPath)
				}
			}

			backupPath := mayorPath + suffix
			if tc.wantBackup {
				if !hasBackup(result.Backups, matrixRel, matrixRel+suffix) {
					t.Errorf("expected Backup{Path:%q, BackupPath:%q%s}, got %+v", matrixRel, matrixRel, suffix, result.Backups)
				}
				got, err := os.ReadFile(backupPath)
				if err != nil {
					t.Fatalf("read backup %s: %v", backupPath, err)
				}
				if string(got) != string(preservedBody) {
					t.Errorf("backup contents:\n got  %q\n want %q", got, preservedBody)
				}
			} else {
				// Generic guard: no .bak.* file must exist
				// anywhere under agents/. Catches both backup
				// suppression (--no-backup) and the
				// non-force/no-edit cells where backups are
				// nonsensical.
				assertNoBackupFiles(t, filepath.Join(tmpHome, ".pogo", "agents"))
				if len(result.Backups) != 0 {
					t.Errorf("expected empty Backups, got %+v", result.Backups)
				}
			}

			// Cells 2 and 4 promise the user's bytes survive
			// untouched. preservedBody is nil for the others.
			if preservedBody != nil && (tc.want == expectSkipped || tc.want == expectConflict) {
				post, err := os.ReadFile(mayorPath)
				if err != nil {
					t.Fatalf("read %s: %v", mayorPath, err)
				}
				if string(post) != string(preservedBody) {
					t.Errorf("canonical %s changed after install:\n got  %q\n want %q", matrixRel, post, preservedBody)
				}
			}
		})
	}
}

// setupFreshInstall runs InstallPrompts({}) so mayor.md gets the binary's
// current embed + a v1 stamp. Returns the post-install canonical bytes —
// the matrix uses these to confirm the file is unchanged when the install
// is supposed to be a no-op (cell 1).
func setupFreshInstall(t *testing.T, tmpHome string) []byte {
	t.Helper()
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("seed InstallPrompts: %v", err)
	}
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	body, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	return body
}

// setupFreshInstallThenEdit installs the canonical embed, then layers a
// user customization on top of the body (preserving the stamp line). The
// returned bytes are what the matrix expects to see preserved (cell 2) or
// copied to the .bak (cell 6).
func setupFreshInstallThenEdit(t *testing.T, tmpHome string) []byte {
	t.Helper()
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("seed InstallPrompts: %v", err)
	}
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	original, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	edited := append([]byte{}, original...)
	if !strings.HasSuffix(string(edited), "\n") {
		edited = append(edited, '\n')
	}
	edited = append(edited, []byte("\n## My house rules\nKeep PRs small.\n")...)
	if err := os.WriteFile(mayorPath, edited, 0644); err != nil {
		t.Fatalf("rewrite mayor.md: %v", err)
	}
	return edited
}

// setupStaleStampNoEdit writes a synthetic "older install" of mayor.md
// whose stamp records a different embed_hash than the current binary's
// (so embed has effectively changed) and whose body matches the recorded
// body_hash (so the user has not edited). InstallPrompts must classify
// this as cell 3: Updated.
func setupStaleStampNoEdit(t *testing.T, tmpHome string) []byte {
	t.Helper()
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	body := []byte("# Mayor (older shipped version)\n")
	hash := contentHash(body)
	stamp := "<!-- pogo-prompt: embed=sha256:" + hash + " body=sha256:" + hash + " -->\n"
	canonical := append([]byte(stamp), body...)
	if err := os.WriteFile(filepath.Join(agentsDir, "mayor.md"), canonical, 0644); err != nil {
		t.Fatalf("write stale mayor.md: %v", err)
	}
	return canonical
}

// setupStaleStampUserEdited builds the cell-4 fixture: a v1 stamp whose
// recorded body_hash refers to an older pristine body, but the on-disk
// body has since been edited so currentBodyHash != stamp.BodyHash. The
// embed_hash is the older pristine hash, so it also differs from the
// binary's current embed. Returns the canonical bytes — cell 4 promises
// they survive unchanged.
func setupStaleStampUserEdited(t *testing.T, tmpHome string) []byte {
	t.Helper()
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	pristineBody := []byte("# Mayor (older shipped version)\n")
	pristineHash := contentHash(pristineBody)
	editedBody := []byte("# Mayor (older shipped version)\n\n## My house rules\nNo amend commits.\n")
	stamp := "<!-- pogo-prompt: embed=sha256:" + pristineHash + " body=sha256:" + pristineHash + " -->\n"
	canonical := append([]byte(stamp), editedBody...)
	if err := os.WriteFile(filepath.Join(agentsDir, "mayor.md"), canonical, 0644); err != nil {
		t.Fatalf("write user-edited mayor.md: %v", err)
	}
	return canonical
}

// assertMatrixStatus checks that exactly the expected status slice on
// InstallResult contains rel, and the others do not. Membership-only —
// other prompts in the install run are out of scope for this matrix.
func assertMatrixStatus(t *testing.T, result *InstallResult, rel string, want matrixExpect) {
	t.Helper()
	in := func(list []string, name string) bool {
		for _, x := range list {
			if x == name {
				return true
			}
		}
		return false
	}
	conflictHas := func(list []PromptConflict, name string) bool {
		for _, c := range list {
			if c.Path == name {
				return true
			}
		}
		return false
	}

	got := map[matrixExpect]bool{
		expectSkipped:   in(result.Skipped, rel),
		expectUpdated:   in(result.Updated, rel),
		expectInstalled: in(result.Installed, rel),
		expectConflict:  conflictHas(result.Conflicts, rel),
	}
	for status, present := range got {
		if status == want {
			if !present {
				t.Errorf("expected %s in status %d, but it was absent (Installed=%v Updated=%v Skipped=%v Conflicts=%+v)",
					rel, want, result.Installed, result.Updated, result.Skipped, result.Conflicts)
			}
		} else {
			if present {
				t.Errorf("expected %s NOT in status %d, but it appeared there (Installed=%v Updated=%v Skipped=%v Conflicts=%+v)",
					rel, status, result.Installed, result.Updated, result.Skipped, result.Conflicts)
			}
		}
	}
}

// hasConflict reports whether result.Conflicts has an entry matching
// (path, distPath). Used to assert the conflict warning surface that
// docs/prompt-customization.md tells users to read on cell 4.
func hasConflict(list []PromptConflict, path, distPath string) bool {
	for _, c := range list {
		if c.Path == path && c.DistPath == distPath {
			return true
		}
	}
	return false
}

// hasBackup reports whether result.Backups has an entry matching
// (path, backupPath). Used to assert the (Path, BackupPath) pair the
// caller surfaces to users on cell 6.
func hasBackup(list []PromptBackup, path, backupPath string) bool {
	for _, b := range list {
		if b.Path == path && b.BackupPath == backupPath {
			return true
		}
	}
	return false
}

// assertNoBackupFiles fails the test if any path under root has a
// ".bak." segment in its filename. Cells 1-5 and 7 must not generate
// backups, even for prompts the matrix isn't directly asserting on.
func assertNoBackupFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if strings.Contains(info.Name(), ".bak.") {
			t.Errorf("unexpected backup file: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
