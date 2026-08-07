package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/revcheck"
)

// The expectation must come from the plist, because the plist is what launchd
// actually execs. PATH can disagree with it — a plist written when pogod lived
// in one place keeps pointing there after a second copy lands earlier on PATH —
// and when they disagree the PATH answer is the wrong thing to expect.
func TestLaunchdProgramPathReadsThePlistNotThePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0755); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, "somewhere", "else", "pogod")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.pogo.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + want + `</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(agents, "com.pogo.daemon.plist"), []byte(plist), 0644); err != nil {
		t.Fatal(err)
	}

	if got := launchdProgramPath(); got != want {
		t.Fatalf("launchdProgramPath = %q, want the plist's ProgramArguments %q", got, want)
	}
}

// No plist installed is a normal state (a fresh box, the systemd side). It must
// fall back rather than blow up, and whatever it falls back to must still flow
// through the sentinel path rather than becoming an empty string that compares
// equal to another empty string.
func TestExpectedRevisionWithNoPlistIsNeverAgreementWithNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Point PATH somewhere with no pogod so the fallback also finds nothing.
	t.Setenv("PATH", t.TempDir())

	exp := expectedDaemonRevision()
	if !revcheck.IsSentinel(exp) {
		t.Fatalf("expectedDaemonRevision = %q with nothing installed; want a sentinel", exp)
	}
	if revcheck.Compare("", exp).OK() {
		t.Fatal("two absences compared equal — that is the defect this check exists to remove")
	}
	if revcheck.Compare("73757a8", exp).Verdict != revcheck.Unknown {
		t.Fatal("an unresolvable expectation did not render as UNKNOWN")
	}
}

// The report is the deliverable on these two paths — they do not fail on the
// verdict, so if the line does not carry the finding, nothing does.
func TestRevisionReportSaysWhatItFoundAndThatItDidNotFail(t *testing.T) {
	// revisionReport names the binary launchd execs, which reads the plist out
	// of $HOME. Point it at a throwaway one so the assertions below do not
	// depend on what is installed on the machine running the suite.
	t.Setenv("HOME", t.TempDir())

	t.Run("agrees is one quiet line", func(t *testing.T) {
		out := revisionReport("install", revcheck.Result{
			Verdict: revcheck.Agrees, Running: "73757a8abc", Expected: "73757a8abc",
		})
		if strings.Count(out, "\n") != 0 {
			t.Fatalf("a passing check printed a block:\n%s", out)
		}
		if !strings.Contains(out, "73757a8a") {
			t.Fatalf("the passing line does not name the revision:\n%s", out)
		}
	})

	t.Run("differs names both revisions and the remedy", func(t *testing.T) {
		out := revisionReport("restart", revcheck.Result{
			Verdict:  revcheck.Differs,
			Running:  "d31297f0000000000000000000000000000000ab",
			Expected: "73757a81111111111111111111111111111111cd",
		})
		for _, want := range []string{"DIFFERS", "d31297f", "73757a8", "/version", "REPORT-ONLY", "verify-revision"} {
			if !strings.Contains(out, want) {
				t.Fatalf("report is missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("unknown says it measured nothing, in those words", func(t *testing.T) {
		out := revisionReport("install", revcheck.Compare(revcheck.RevUnreachable, "73757a8abc"))
		if !strings.Contains(out, "UNKNOWN") {
			t.Fatalf("report does not state UNKNOWN:\n%s", out)
		}
		if !strings.Contains(out, "did NOT measure") {
			t.Fatalf("report does not say the property went unmeasured — an UNKNOWN that reads like a pass is the original defect:\n%s", out)
		}
		if !strings.Contains(out, "not the\n    same as it being fine") {
			t.Fatalf("report does not distinguish 'unmeasured' from 'fine':\n%s", out)
		}
	})
}

// mg-ed4a's explicit constraint: install's exit semantics do not change, so the
// report must state that it did not fail the run. If that sentence goes away, a
// reader will act on the verdict as though the install had already refused.
func TestEveryNonPassingReportSaysItDidNotFailTheRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, res := range []revcheck.Result{
		revcheck.Compare("d31297f", "73757a8"),
		revcheck.Compare(revcheck.RevUnreachable, "73757a8"),
		revcheck.Compare(revcheck.RevUnstamped, "73757a8"),
		revcheck.Compare("73757a8", revcheck.RevMissing),
	} {
		out := revisionReport("install", res)
		if !strings.Contains(out, "install did not fail because of it") {
			t.Fatalf("verdict %s does not state the report-only contract:\n%s", res.Verdict, out)
		}
	}
}
