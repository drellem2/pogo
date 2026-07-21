package closingref

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// realIncidentBody is e83f394's actual commit message, byte for byte, captured
// with `git show e83f394 --no-patch --format=%B`.
//
// It is a testdata file rather than a Go string literal on purpose: the wrap is
// the defect, and the wrap is a property of where the newlines fall. A literal
// re-typed into source is one gofmt or one editor reflow away from silently
// becoming a DIFFERENT message that no longer exercises the bug. The fixture
// has to be the artifact, not a description of it.
func realIncidentBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "e83f394-commit-body.txt"))
	if err != nil {
		t.Fatalf("read incident fixture: %v", err)
	}
	return string(b)
}

// TestCatchesTheRealIncident is the positive control that matters. The tidy
// `Fixes #123` case is the one everybody thinks of and is NOT what happened
// here; if this test passed while the wrapped case slipped through, we would
// have shipped a check for a different bug.
func TestCatchesTheRealIncident(t *testing.T) {
	findings := Check(realIncidentBody(t))
	if len(findings) == 0 {
		t.Fatal("MISS: e83f394's body closed drellem2/pogo#89 on GitHub and the check found nothing")
	}

	var wrapped *Finding
	for i := range findings {
		if findings[i].Ref == "drellem2/pogo#89" && findings[i].Wrapped {
			wrapped = &findings[i]
			break
		}
	}
	if wrapped == nil {
		t.Fatalf("found %d finding(s) but none was the wrapped drellem2/pogo#89 adjacency: %+v", len(findings), findings)
	}
	// The wrap: "nobody closed" ends line 7, the reference opens line 8.
	if wrapped.Keyword != "closed" {
		t.Errorf("keyword = %q, want %q", wrapped.Keyword, "closed")
	}
	if wrapped.KeywordLine != 7 || wrapped.RefLine != 8 {
		t.Errorf("adjacency at lines %d→%d, want 7→8", wrapped.KeywordLine, wrapped.RefLine)
	}
}

// TestSameLineRegexWouldMissTheIncident is the refutation, kept in the tree so
// the newline clause cannot be "simplified" away by someone who has only ever
// seen the `Fixes #123` form.
//
// MEASURED: against the real body, a keyword→ref pattern restricted to spaces
// and tabs finds ZERO adjacencies. Detection on the only real instance we have:
// 0/1. That is the entire reason closingPattern uses [[:space:]].
func TestSameLineRegexWouldMissTheIncident(t *testing.T) {
	sameLineOnly := strings.NewReplacer("[[:space:]]", "[ \\t]").Replace(closingPattern.String())
	body := realIncidentBody(t)

	naive, err := regexp.Compile(sameLineOnly)
	if err != nil {
		t.Fatalf("compile same-line pattern %q: %v", sameLineOnly, err)
	}
	if got := naive.FindAllString(body, -1); len(got) != 0 {
		t.Fatalf("premise broken: the same-line pattern was supposed to miss the incident, but matched %v", got)
	}
	if len(Check(body)) == 0 {
		t.Fatal("the shipped pattern must catch what the same-line pattern misses")
	}
}

// TestPasses is the other required half. A check only ever observed CATCHING
// has not been shown to permit anything, and one that flags every issue
// reference gets disabled within a week — our commit bodies cite issues
// constantly and legitimately.
func TestPasses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			// The mandated neutral form.
			"refs trailer",
			"fix(ghteardown): route teardown misses to the fleet\n\nRefs drellem2/pogo#89\n",
		},
		{
			// A keyword and a reference in the same paragraph, separated by
			// prose. GitHub does not link these and neither may we.
			"keyword separated from ref by prose",
			"The change fixes the case where the thread behind pogo issue 89\nwent quiet. See #89 for the reporter's original transcript.\n",
		},
		{
			"reference with no keyword anywhere",
			"Discussed at length on drellem2/pogo#89 and #12 before landing.\n",
		},
		{
			// "closure" and "prefix" contain closing keywords as substrings.
			// Word boundaries are what keep them out.
			"keyword-like words are not keywords",
			"The closure over prefix #89 is resolver state, not a directive.\n",
		},
		{
			"acknowledged closure",
			"feat: land the teardown\n\nCloses #89 as part of the carrier's final step.\n\n" +
				AckPrefix + " #89 — intentional; teardown lands with this commit\n",
		},
		{
			"acknowledged closure, wrapped form",
			"nobody closed\ndrellem2/pogo#89 until now.\n\n" +
				AckPrefix + " drellem2/pogo#89 — intentional\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if f := Check(tc.body); len(f) != 0 {
				t.Errorf("false positive — this body closes nothing on GitHub: %+v", f)
			}
		})
	}
}

// TestCatches covers the adjacency forms beyond the incident itself.
func TestCatches(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wrapped bool
	}{
		{"tidy same-line", "Fixes #123\n", false},
		{"colon form", "Resolves: drellem2/pogo#89\n", false},
		{"wrapped, bare ref", "this is the change that finally closed\n#89 after four days\n", true},
		{"wrapped, qualified ref", "but nobody closed\ndrellem2/pogo#89, and it sat OPEN\n", true},
		{"past tense mid-sentence", "The work fixed #89 before anyone noticed.\n", false},
		{"blank line between", "resolve\n\n#89\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Check(tc.body)
			if len(f) != 1 {
				t.Fatalf("want exactly 1 finding, got %d: %+v", len(f), f)
			}
			if f[0].Wrapped != tc.wrapped {
				t.Errorf("Wrapped = %v, want %v", f[0].Wrapped, tc.wrapped)
			}
		})
	}
}

// TestAckIsPerReference: the escape hatch must not become a blanket mute. An
// ack naming one issue leaves every other adjacency in the same body flagged —
// otherwise the first intentional closure in a commit disarms the check for the
// accidental one beside it.
func TestAckIsPerReference(t *testing.T) {
	body := "Closes #1 deliberately, and this one fixes\n#2 by accident.\n\n" +
		AckPrefix + " #1 — intentional\n"
	f := Check(body)
	if len(f) != 1 {
		t.Fatalf("want 1 unacknowledged finding, got %d: %+v", len(f), f)
	}
	if f[0].Ref != "#2" {
		t.Errorf("surviving finding is %q, want %q", f[0].Ref, "#2")
	}
	if !f[0].Wrapped {
		t.Error("the #2 adjacency is a wrap and should be reported as one")
	}
}

// TestAckCannotTriggerItself — an ack line that quotes a keyword must not be
// reported as the very hazard it is acknowledging.
func TestAckCannotTriggerItself(t *testing.T) {
	if f := Check(AckPrefix + " closes #89 — intentional\n"); len(f) != 0 {
		t.Errorf("the ack line reported itself: %+v", f)
	}
}

// TestReportNamesTheNeutralForm: if the failure message does not hand the
// author the string that makes their commit land, the next attempt is
// --no-verify and the check is decorative.
func TestReportNamesTheNeutralForm(t *testing.T) {
	out := Report("commit-msg", Check(realIncidentBody(t)))
	for _, want := range []string{"Refs drellem2/pogo#89", AckPrefix, "WRAPPED"} {
		if !strings.Contains(out, want) {
			t.Errorf("report omits %q:\n%s", want, out)
		}
	}
}
