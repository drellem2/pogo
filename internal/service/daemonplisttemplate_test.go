package service

import (
	"encoding/xml"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// daemonPlistTemplate is the reference copy a manual install sed-copies into
// ~/Library/LaunchAgents/. `pogo service install` renders its own template and
// never reads this file — see the comment inside it, and mg-b7b7.
const daemonPlistTemplate = "../../scripts/launchd/com.pogo.daemon.plist"

// logLevelKeyLine is the line docs/customizing.md tells an operator to uncomment.
const logLevelKeyLine = "<key>POGO_LOG_LEVEL</key>"

// TestDaemonPlistTemplateSurvivesTheDocumentedUncomment performs, on the shipped
// template, exactly the edit docs/customizing.md:526 tells a manual-install
// operator to make — "the repo template ships a commented-out POGO_LOG_LEVEL
// key, so uncommenting it before the copy sets the level on the plist you
// install" — and requires the result to be a plist launchd can read, carrying
// the level.
//
// The template was valid as shipped and invalid only once that sentence was
// obeyed (mg-dae3, fixed in mg-f3ae): the explanatory prose and the two key
// lines shared a single <!-- --> span, so deleting its delimiters left ~15 lines
// of prose as character data inside <dict>. The operator got a broken file, a
// plutil diagnostic that blamed a line of the prose rather than anything to do
// with POGO_LOG_LEVEL, and no log level.
//
// Every review of the file itself passed, because the file itself was fine. Only
// performing the documented action finds this. That is why the test performs it
// rather than pattern-matching the comment layout.
func TestDaemonPlistTemplateSurvivesTheDocumentedUncomment(t *testing.T) {
	shipped := readRepoFile(t, daemonPlistTemplate)

	if err := checkNoTextInPlistContainers(shipped); err != nil {
		t.Fatalf("%s is not a readable plist as shipped: %v", daemonPlistTemplate, err)
	}

	uncommented := uncommentLogLevelKey(t, shipped)
	if err := checkNoTextInPlistContainers(uncommented); err != nil {
		t.Fatalf("uncommenting POGO_LOG_LEVEL in %s — the action docs/customizing.md documents —\n"+
			"produces a plist launchd cannot read: %v\n"+
			"The likely cause is not at that line: it is prose sharing an XML comment with the\n"+
			"key lines, so deleting the delimiters leaves the prose as text inside <dict>.\n"+
			"Give the key a <!-- --> of its own and close the prose comment after its own text.",
			daemonPlistTemplate, err)
	}

	env := environmentVariablesOf(t, uncommented)
	if got := env["POGO_LOG_LEVEL"]; got != "debug" {
		t.Errorf("after the documented uncomment POGO_LOG_LEVEL = %v, want \"debug\"", got)
	}
	// The uncomment must not cost the keys that were already active. A delimiter
	// deletion that swallowed a neighbour would still leave POGO_LOG_LEVEL
	// readable, so checking only that key would miss it.
	for _, key := range []string{"PATH", "HOME", "POGO_HOME", "POGO_PLUGIN_PATH"} {
		if env[key] == nil {
			t.Errorf("after the documented uncomment %s is gone from EnvironmentVariables", key)
		}
	}
}

// TestDaemonPlistUncommentGuardIsArmed is the control for the test above: it
// rebuilds the defect out of the live template and requires the guard to reject
// it.
//
// This is not ceremony. The obvious way to write the guard — decode with this
// package's own decodePlistDict and check the key — passes on the broken file:
// its <dict> loop handles StartElement and EndElement and ignores CharData, so
// it accepts the stray prose and reports POGO_LOG_LEVEL="debug" from a file
// plutil rejects outright. A guard written that way would have been satisfied by
// the exact defect it exists to catch. Reconstructing the defect from the
// current file, rather than pasting a copy of the old one, keeps this control
// from rotting into a fixture nobody re-derives.
func TestDaemonPlistUncommentGuardIsArmed(t *testing.T) {
	regressed := uncommentLogLevelKey(t, shareOneCommentWithTheProse(t, readRepoFile(t, daemonPlistTemplate)))

	if err := checkNoTextInPlistContainers(regressed); err == nil {
		t.Fatal("the guard accepts a template whose prose and POGO_LOG_LEVEL key share one XML\n" +
			"comment — the mg-dae3 defect — so it is guarding nothing. Do not relax it into\n" +
			"decodePlistDict: that decoder ignores character data inside <dict> by design.")
	}

	// Same bytes, permissive reader: the trap this control exists to document.
	if _, err := decodePlistDict([]byte(regressed)); err != nil {
		t.Logf("decodePlistDict now rejects the regressed template too (%v) — it did not when this\n"+
			"control was written. That is an improvement, not a failure; the strict check stays\n"+
			"because it is what pins the guard to plutil's rule.", err)
	}
}

// TestDaemonPlistGuardAgreesWithPlutil pins checkNoTextInPlistContainers to the
// parser that actually decides, on the one host where that parser exists. The
// strict check is a second opinion about plist validity; a second opinion that
// was never compared against the first is just an assertion.
func TestDaemonPlistGuardAgreesWithPlutil(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS-only; checkNoTextInPlistContainers runs everywhere, this cross-check does not")
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skipf("plutil not on PATH: %v", err)
	}

	shipped := readRepoFile(t, daemonPlistTemplate)
	cases := []struct {
		name  string
		plist string
		valid bool
	}{
		{"as shipped", shipped, true},
		{"documented uncomment applied", uncommentLogLevelKey(t, shipped), true},
		{"mg-dae3 defect reconstructed", uncommentLogLevelKey(t, shareOneCommentWithTheProse(t, shipped)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "com.pogo.daemon.plist")
			writeFileT(t, path, tc.plist)

			out, err := exec.Command("plutil", "-lint", path).CombinedOutput()
			plutilOK := err == nil
			ours := checkNoTextInPlistContainers(tc.plist) == nil

			if plutilOK != tc.valid {
				t.Errorf("plutil -lint says valid=%v, want %v: %s", plutilOK, tc.valid, strings.TrimSpace(string(out)))
			}
			if ours != plutilOK {
				t.Errorf("checkNoTextInPlistContainers says valid=%v but plutil says %v: %s\n"+
					"The two readers disagree, so the non-darwin guard is no longer a proxy for launchd.",
					ours, plutilOK, strings.TrimSpace(string(out)))
			}
		})
	}
}

// uncommentLogLevelKey mechanizes the action docs/customizing.md documents:
// delete the <!-- and --> that bracket the POGO_LOG_LEVEL key. It deletes the
// delimiters of whichever comment span contains the key, which is what an
// operator reading that sentence does — so if the key ever shares a span with
// something else, that something else comes out of the comment too, and the
// caller sees the operator's result rather than the intended one.
func uncommentLogLevelKey(t *testing.T, src string) string {
	t.Helper()
	key := strings.Index(src, logLevelKeyLine)
	if key < 0 {
		t.Fatalf("%s no longer contains %s, but docs/customizing.md still tells manual-install\n"+
			"operators to uncomment one. Update both together or neither.", daemonPlistTemplate, logLevelKeyLine)
	}
	open := strings.LastIndex(src[:key], "<!--")
	if open < 0 {
		t.Fatalf("%s: %s is not inside an XML comment; docs/customizing.md describes it as\n"+
			"commented out and tells operators to uncomment it.", daemonPlistTemplate, logLevelKeyLine)
	}
	if closed := strings.Index(src[open:key], "-->"); closed >= 0 {
		t.Fatalf("%s: %s ships uncommented (its nearest <!-- closes before it), so the level is\n"+
			"set for everyone rather than opt-in as docs/customizing.md describes.", daemonPlistTemplate, logLevelKeyLine)
	}
	end := strings.Index(src[key:], "-->")
	if end < 0 {
		t.Fatalf("%s: the comment holding %s is never closed", daemonPlistTemplate, logLevelKeyLine)
	}
	end += key
	return src[:open] + src[open+len("<!--"):end] + src[end+len("-->"):]
}

// shareOneCommentWithTheProse reconstructs the mg-dae3 defect from the current
// template by joining the key's comment back onto the prose comment above it —
// deleting the --> that closes the prose and the <!-- that opens the key, which
// is precisely the pair that fixing it added.
func shareOneCommentWithTheProse(t *testing.T, src string) string {
	t.Helper()
	key := strings.Index(src, logLevelKeyLine)
	if key < 0 {
		t.Fatalf("%s no longer contains %s", daemonPlistTemplate, logLevelKeyLine)
	}
	open := strings.LastIndex(src[:key], "<!--")
	if open < 0 {
		t.Fatalf("%s: %s is not inside an XML comment", daemonPlistTemplate, logLevelKeyLine)
	}
	prose := strings.LastIndex(src[:open], "-->")
	if prose < 0 {
		t.Fatalf("%s: no comment closes above %s, so the defect cannot be reconstructed and the\n"+
			"control below would pass without testing anything", daemonPlistTemplate, logLevelKeyLine)
	}
	merged := src[:prose] + src[prose+len("-->"):open] + src[open+len("<!--"):]
	if merged == src {
		t.Fatal("reconstructing the defect changed nothing, so the control below would assert against\n" +
			"the healthy template and report the guard as armed without having tried it")
	}
	return merged
}

// environmentVariablesOf reads the EnvironmentVariables dict through this
// package's own decoder, so the values this test asserts are the values pogo's
// LaunchAgent audit would read out of the installed file.
func environmentVariablesOf(t *testing.T, plist string) map[string]any {
	t.Helper()
	root, err := decodePlistDict([]byte(plist))
	if err != nil {
		t.Fatalf("decode plist: %v", err)
	}
	env, ok := root["EnvironmentVariables"].(map[string]any)
	if !ok {
		t.Fatalf("EnvironmentVariables is %T, want a <dict>", root["EnvironmentVariables"])
	}
	return env
}

// plistContainers are the elements whose content is other elements. Text in one
// is what a deleted comment delimiter leaves behind, and is what plutil rejects.
var plistContainers = map[string]bool{"plist": true, "dict": true, "array": true}

// checkNoTextInPlistContainers reports the stray character data that makes a
// plist unreadable to launchd.
//
// It deliberately answers a narrower question than decodePlistDict rather than
// re-deciding what a key holds — one plist reader disagreeing with another about
// a value is the failure that decoder's comment warns about. This checks only
// the property that decoder ignores, and TestDaemonPlistGuardAgreesWithPlutil
// holds it to plutil's verdict on the host where plutil exists.
func checkNoTextInPlistContainers(src string) error {
	dec := xml.NewDecoder(strings.NewReader(src))
	var open []string
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("line %d: %w", lineAt(src, start), err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			open = append(open, t.Name.Local)
		case xml.EndElement:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		case xml.CharData:
			if len(open) == 0 || !plistContainers[open[len(open)-1]] {
				continue
			}
			if text := strings.TrimSpace(string(t)); text != "" {
				return fmt.Errorf("line %d: text where <%s> expects an element: %.60q",
					lineAt(src, start), open[len(open)-1], text)
			}
		}
	}
}

// lineAt converts a byte offset into a 1-based line number, so a failure names a
// line the reader can open rather than an offset they have to convert.
func lineAt(src string, off int64) int {
	if off > int64(len(src)) {
		off = int64(len(src))
	}
	return 1 + strings.Count(src[:off], "\n")
}
