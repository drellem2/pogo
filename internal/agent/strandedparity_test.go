package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/strandedwork"
	"github.com/drellem2/pogo/internal/strandwatch"
)

// The drift tripwire for the stranded-work surfaces (mg-8cda).
//
// Three surfaces judge one stranded branch: `pogo check-stranded`
// (strandwatch.Scan), pogod's [stranded-push] mail (StrandedAlert.Message) and
// the dispatch refusal (strandedWorkRefusal). drellem2/pogo#174 was the mail
// and check-stranded disagreeing about the same branch — each had its own
// decision table, and a fix to one was drift in the other. They now read
// strandedwork.Decide; this test is what notices when one of them stops.
//
// It runs ALL THREE over the SAME real branches, one per band, and requires
// the same cell and the same remedy class from each: an unconditional submit,
// a submit conditional on a hand check, or no submit at all.

// remedyClass is the part of a remedy that is dangerous to get wrong.
type remedyClass string

const (
	remedySubmit      remedyClass = "unconditional submit"
	remedyConditional remedyClass = "submit only after a hand check"
	remedyNoSubmit    remedyClass = "no submit"
)

func (c remedyClass) String() string { return string(c) }

// parityBand is one fixture branch and what every surface must say about it.
type parityBand struct {
	name     string
	id       string
	branch   string
	file     string
	subject  string
	lines    int // lines the branch adds
	onMain   int // how many of those main independently already has
	wantCell strandedwork.Cell
	want     remedyClass
}

func parityLines(tag string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("the %s branch contributes substantive line number %02d", tag, i))
	}
	return out
}

func appendLines(t *testing.T, repo, file string, lines []string) {
	t.Helper()
	path := filepath.Join(repo, file)
	prev, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(prev, []byte(strings.Join(lines, "\n")+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildParityBand pushes band's branch, then puts band.onMain of its lines on
// main in a commit of main's own — same text, different patch — so `git cherry`
// still calls the branch unmerged while the content check sees the overlap.
func buildParityBand(t *testing.T, repo string, b parityBand) {
	t.Helper()
	lines := parityLines(b.name, b.lines)
	gitRun(t, repo, "checkout", "-q", "-b", b.branch, "main")
	appendLines(t, repo, b.file, lines)
	gitRun(t, repo, "add", b.file)
	gitRun(t, repo, "commit", "-q", "-m", b.subject)
	gitRun(t, repo, "push", "-q", "origin", b.branch)
	gitRun(t, repo, "checkout", "-q", "main")
	if b.onMain == 0 {
		return
	}
	// One extra line keeps main's patch id different even when it carries every
	// line the branch adds.
	appendLines(t, repo, b.file, append(append([]string{}, lines[:b.onMain]...),
		"and main wrote one line of its own right here"))
	gitRun(t, repo, "add", b.file)
	gitRun(t, repo, "commit", "-q", "-m", fmt.Sprintf("feat: %s work, as main has it (%s)", b.name, b.id))
	gitRun(t, repo, "push", "-q", "origin", "main")
}

// classify* read each surface's rendering into a remedyClass. Each is written
// against the surface's own layout, and each returns "" for a rendering it does
// not recognise, so a wording change fails loudly instead of classifying as
// whatever is left.

func classifyRow(remedy string) remedyClass {
	switch {
	case !strings.Contains(remedy, "refinery submit"):
		return remedyNoSubmit
	case strings.HasPrefix(remedy, "pogo refinery submit"):
		return remedySubmit
	case strings.Contains(remedy, "only if it did NOT land: pogo refinery submit"):
		return remedyConditional
	}
	return ""
}

func classifyMail(body string) remedyClass {
	switch {
	case !strings.Contains(body, "refinery submit"):
		return remedyNoSubmit
	case strings.Contains(body, "WHAT TO DO — resubmit"):
		return remedySubmit
	case strings.Contains(body, "Only if it did NOT land:\n\n    pogo refinery submit"):
		return remedyConditional
	}
	return ""
}

func classifyRefusal(msg string) remedyClass {
	switch {
	case !strings.Contains(msg, "refinery submit"):
		return remedyNoSubmit
	case strings.Contains(msg, "Instead, get the branch merged (`pogo refinery submit"):
		return remedySubmit
	case strings.Contains(msg, "Instead, check by hand first") &&
		strings.Contains(msg, "only if it did NOT, get it merged (`pogo refinery submit"):
		return remedyConditional
	}
	return ""
}

func TestStrandedSurfacesAgreeOnEveryBand(t *testing.T) {
	repo := strandedRepo(t)
	bands := []parityBand{
		{name: "low", id: "mg-10a1", branch: "polecat-p10a1", file: "low.md",
			subject: "feat: low-band work (mg-10a1)", lines: 30, onMain: 0,
			wantCell: strandedwork.CellResubmit, want: remedySubmit},
		{name: "partly", id: "mg-20b2", branch: "polecat-p20b2", file: "partly.md",
			subject: "feat: partly-present work (mg-20b2)", lines: 30, onMain: 21, // 70%
			wantCell: strandedwork.CellCheckByHand, want: remedyConditional},
		{name: "landed", id: "mg-30c3", branch: "polecat-p30c3", file: "landed.md",
			subject: "feat: landed work (mg-30c3)", lines: 30, onMain: 30, // 100%
			wantCell: strandedwork.CellSuggestsLanded, want: remedyNoSubmit},
		{name: "rescue", id: "mg-40d4", branch: "polecat-p40d4", file: "rescue.md",
			subject: "RESCUE(mg-40d4): recovered from preserved worktree p40d4 — UNREVIEWED", lines: 30,
			wantCell: strandedwork.CellRescueUnbuilt, want: remedyNoSubmit},
		// Not one of the ticket's four bands, but the table has to answer it and
		// #174 answered it for the mail: too small to measure is not corroborated.
		{name: "small", id: "mg-50e5", branch: "polecat-p50e5", file: "small.md",
			subject: "fix: a small change (mg-50e5)", lines: 3,
			wantCell: strandedwork.CellCheckByHand, want: remedyConditional},
	}
	var items []strandwatch.Item
	for _, b := range bands {
		buildParityBand(t, repo, b)
		items = append(items, strandwatch.Item{ID: b.id, Status: "available", Repo: repo, Title: b.name})
	}

	rep, err := strandwatch.Scan(strandwatch.Options{
		Items:      func() ([]strandwatch.Item, error) { return items, nil },
		LiveAgents: func() (map[string]bool, error) { return map[string]bool{}, nil },
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("strandwatch.Scan: %v", err)
	}
	reg := newDrainTestRegistry(t)

	for _, b := range bands {
		t.Run(b.name, func(t *testing.T) {
			// --- check-stranded
			var row *strandwatch.Row
			for i := range rep.Rows {
				if rep.Rows[i].Item.ID == b.id {
					row = &rep.Rows[i]
				}
			}
			if row == nil {
				t.Fatalf("check-stranded has no row for %s:\n%s", b.id, strandwatch.Render(rep, true))
			}

			// --- the mail, assembled the way reportStrandedWorkOnRelease does
			f, err := strandedwork.Inspect(repo, b.branch, "main")
			if err != nil || !f.Stranded() {
				t.Fatalf("Inspect(%s) = %+v, %v; the fixture is not stranded", b.branch, f, err)
			}
			presence, note := strandedwork.Corroborate(repo, f)
			alert := StrandedAlert{
				Polecat: strings.TrimPrefix(b.branch, "polecat-"), WorkItemID: b.id, Repo: repo,
				Reason: "released", Route: RouteRelease, ItemStatus: "available",
				Finding: f, Presence: presence, SecondOpinion: note,
			}
			_, body := alert.Message()

			// --- the dispatch refusal
			refusal := reg.strandedWorkRefusal(b.id, repo, "main")
			if refusal == "" {
				t.Fatalf("the dispatch gate did not refuse %s", b.id)
			}

			// SAME CELL. The positive control for the fixture is that the cell is
			// the band's: a fixture that failed to reach its band would agree with
			// itself everywhere and prove nothing.
			if row.Cell != b.wantCell || alert.cell() != b.wantCell {
				t.Fatalf("cell: check-stranded=%q mail=%q, want %q (presence %s)",
					row.Cell, alert.cell(), b.wantCell, presence.Describe())
			}

			// SAME REMEDY CLASS.
			got := map[string]remedyClass{
				"check-stranded": classifyRow(row.Remedy()),
				"mail":           classifyMail(body),
				"refusal":        classifyRefusal(refusal),
			}
			for surface, c := range got {
				if c != b.want {
					t.Errorf("%s remedy is %q, want %q", surface, c, b.want)
				}
			}
			if t.Failed() {
				t.Logf("check-stranded: %s\n\nmail:\n%s\n\nrefusal: %s", row.Remedy(), body, refusal)
				return
			}

			// SAME WORDS where the cell has words in common.
			lower := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
			switch b.wantCell {
			case strandedwork.CellCheckByHand, strandedwork.CellSuggestsLanded:
				hand := strandedwork.HandCheckCommand(repo, b.id, f.Target)
				for surface, text := range map[string]string{"check-stranded": row.Remedy(), "mail": body, "refusal": refusal} {
					if !strings.Contains(lower(text), "check by hand first") {
						t.Errorf("%s does not say \"check by hand first\":\n%s", surface, text)
					}
					if !strings.Contains(text, hand) {
						t.Errorf("%s does not carry the hand check %q:\n%s", surface, hand, text)
					}
				}
			case strandedwork.CellRescueUnbuilt:
				read := strandedwork.ReadRescueCommand(repo, f.Target, f.Ref)
				for surface, text := range map[string]string{"check-stranded": row.Remedy(), "mail": body, "refusal": refusal} {
					if !strings.Contains(text, read) {
						t.Errorf("%s does not print %q:\n%s", surface, read, text)
					}
				}
			}
		})
	}
}
