package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// The stranded sweep's enumerator on an enth checkout (enth-vcs slice 6,
// mg-30da0): read through enth, never git, and its trailer-less base reported
// as stranded work (§5, guard 1). The enthd is a stub that lists base b0 as an
// unlanded Change, with or without a Pogo-Item trailer.
func TestPolecatWorktreesReportsATrailerlessEnthBase(t *testing.T) {
	for _, tc := range []struct {
		name, trailers, want string
	}{
		{"trailer-less", `[]`, "STRANDED WORK: "},
		{"trailered (control)", `[{"key":"Pogo-Item","value":"mg-1"}]`, "no branch to measure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pogoHome := filepath.Join(root, "pogohome")
			dir := filepath.Join(pogoHome, "polecats", "p1")
			os.MkdirAll(filepath.Join(dir, ".enth"), 0o755)
			t.Setenv("POGO_HOME", pogoHome)
			os.WriteFile(filepath.Join(dir, enthvcs.StateRelPath),
				[]byte(`{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}`+"\n"), 0o644)
			if err := enthvcs.WriteRecord(dir, enthvcs.Record{Repo: "/src/enthproj", Base: "r0"}); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			os.MkdirAll(bin, 0o755)
			line := `{"type":"change","id":"b0","entry":2,"author":{"name":"w","email":"w@x"},"created":1700000000,"base":"r0","trailers":` + tc.trailers + `,"landed":null}`
			os.WriteFile(filepath.Join(bin, "enth"), []byte("#!/bin/sh\ncase \"$*\" in *'changes --unlanded'*) printf '%s\\n' '"+line+"' ;; *) exit 2 ;; esac\n"), 0o755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			wts, err := polecatWorktrees()
			if err != nil || len(wts) != 1 {
				t.Fatalf("polecatWorktrees = %+v, %v", wts, err)
			}
			w := wts[0]
			if w.Repo != "/src/enthproj" || w.Branch != "" || !strings.Contains(w.Error, tc.want) {
				t.Errorf("worktree = %+v; want repo /src/enthproj, no branch, %q", w, tc.want)
			}
			if tc.want == "STRANDED WORK: " && !strings.Contains(w.Error, "this Change has no Pogo-Item trailer") {
				t.Errorf("the report must carry the remedy: %s", w.Error)
			}
		})
	}
}
