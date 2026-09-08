package promptstale

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/staleness"
)

func recipientOf(paths ...string) Recipient {
	rc := Recipient{Agent: "mayor"}
	for _, p := range paths {
		rc.Findings = append(rc.Findings, Finding{Path: p, Kind: KindDiffers, Agent: "mayor", Owned: true})
	}
	return rc
}

// TestSelfCeilingSeparatesNoOpFromFixable is the discrimination the notice
// exists for, with both arms in one test so neither is a lone negative.
//
// The live state on 2026-09-08 was the FROZEN arm: pogod's boot installer had
// reported ok=true seven times over a mayor.md 129 lines behind, because it
// carried exactly the bytes already on disk. A notice that said "run pogo agent
// prompt install" there was sending the reader at the very daemon that had just
// declined to change anything.
func TestSelfCeilingSeparatesNoOpFromFixable(t *testing.T) {
	rc := recipientOf("mayor.md")

	frozen := Report{SelfCeiling: &staleness.InstallerCeiling{
		Name: SelfCeilingName, Frozen: []string{"mayor.md"},
	}}
	got := selfCeilingLines(rc, frozen)
	for _, want := range []string{"NO-OP", "NEWER BINARY", "AND BE RUN"} {
		if !strings.Contains(got, want) {
			t.Errorf("frozen notice does not say %q:\n%s", want, got)
		}
	}

	// POSITIVE CONTROL — same function, a daemon that carries the shipped text.
	// Without this, the assertions above cannot tell a working discriminator
	// from a block that prints the same refusal over every input.
	fixable := Report{SelfCeiling: &staleness.InstallerCeiling{
		Name: SelfCeilingName, Closes: []string{"mayor.md"},
	}}
	ctl := selfCeilingLines(rc, fixable)
	if strings.Contains(ctl, "NO-OP") {
		t.Errorf("a daemon carrying the shipped text was called a no-op:\n%s", ctl)
	}
	if !strings.Contains(ctl, "SHIPPED content") {
		t.Errorf("the fixable arm does not say the daemon carries it:\n%s", ctl)
	}
	if got == ctl {
		t.Error("the two arms produced identical text — the block decides nothing")
	}
}

// TestSelfCeilingThirdVersionSendsToFetch — a daemon ahead of the reference is
// not a daemon that cannot help, and the reference here is a lagging mirror.
func TestSelfCeilingThirdVersionSendsToFetch(t *testing.T) {
	got := selfCeilingLines(recipientOf("mayor.md"), Report{
		SelfCeiling: &staleness.InstallerCeiling{Name: SelfCeilingName, Third: []string{"mayor.md"}},
	})
	if !strings.Contains(got, "--fetch") {
		t.Errorf("a third-version daemon must send the reader to --fetch:\n%s", got)
	}
	if strings.Contains(got, "NO-OP") {
		t.Errorf("a third-version daemon is not a no-op:\n%s", got)
	}
}

// TestSelfCeilingUnknownIsNotAnAllClear — an unreadable ceiling must say so and
// must not read as either remedy.
func TestSelfCeilingUnknownIsNotAnAllClear(t *testing.T) {
	got := selfCeilingLines(recipientOf("mayor.md"), Report{
		SelfCeiling: &staleness.InstallerCeiling{Name: SelfCeilingName, Unknown: "embed unreadable"},
	})
	if !strings.Contains(got, "UNKNOWN") || !strings.Contains(got, "not an all-clear") {
		t.Errorf("an unknown ceiling must be stated as unknown:\n%s", got)
	}
	if strings.Contains(got, "NO-OP") || strings.Contains(got, "SHIPPED content") {
		t.Errorf("an unknown ceiling prescribed a remedy:\n%s", got)
	}
}

// TestSelfCeilingJudgesOnlyThisRecipientsFiles — the sweep mails one agent per
// group, and a ceiling counted over somebody else's files would not match the
// list printed directly above it.
func TestSelfCeilingJudgesOnlyThisRecipientsFiles(t *testing.T) {
	rep := Report{SelfCeiling: &staleness.InstallerCeiling{
		Name:   SelfCeilingName,
		Frozen: []string{"mayor.md", "pm/pm-template.md", "templates/polecat-triage.md"},
	}}
	got := selfCeilingLines(recipientOf("mayor.md"), rep)
	if !strings.Contains(got, "all 1 file(s)") {
		t.Errorf("the ceiling counted files this recipient was not mailed about:\n%s", got)
	}
}

func TestSelfCeilingAbsentPrintsNothing(t *testing.T) {
	if got := selfCeilingLines(recipientOf("mayor.md"), Report{}); got != "" {
		t.Fatalf("a sweep with no ceiling must print no block, got:\n%s", got)
	}
}

// TestFromStalenessMatchesTheSelfRowByName — the witness returns one row per
// supplied source and the runner supplies one today. Matching by name rather
// than by index is what keeps a second source, added later, from being reported
// as this daemon's own.
func TestFromStalenessMatchesTheSelfRowByName(t *testing.T) {
	raw := staleness.PromptReport{
		InstalledRoot: "/root",
		Deltas:        []staleness.PromptDelta{{Path: "mayor.md", Kind: "differs"}},
		Ceilings: []staleness.InstallerCeiling{
			{Name: "some other installer", Closes: []string{"mayor.md"}},
			{Name: SelfCeilingName, Frozen: []string{"mayor.md"}},
		},
	}
	got := FromStaleness(raw, "mayor")
	if got.SelfCeiling == nil {
		t.Fatal("the self row was not picked up")
	}
	if got.SelfCeiling.Name != SelfCeilingName {
		t.Fatalf("picked the wrong row: %q", got.SelfCeiling.Name)
	}
	if len(got.SelfCeiling.Frozen) != 1 {
		t.Errorf("the wrong row's cells were carried: %+v", got.SelfCeiling)
	}

	// POSITIVE CONTROL for absence: a report with no self row must leave it nil
	// rather than fall back to whatever row happens to be first.
	raw.Ceilings = []staleness.InstallerCeiling{{Name: "some other installer"}}
	if other := FromStaleness(raw, "mayor"); other.SelfCeiling != nil {
		t.Errorf("a foreign row was adopted as this daemon's: %+v", other.SelfCeiling)
	}
}

// TestBodyCarriesTheCeilingAndWarnsBesideTheCommand — the block has to reach
// the rendered mail, and the `pogo agent prompt install` line has to carry the
// warning beside it. A caveat three paragraphs from the command a reader copies
// does not exist.
func TestBodyCarriesTheCeilingAndWarnsBesideTheCommand(t *testing.T) {
	rc := recipientOf("mayor.md")
	body := rc.Body(Report{
		Root:        "/root",
		Shipped:     9,
		SelfCeiling: &staleness.InstallerCeiling{Name: SelfCeilingName, Frozen: []string{"mayor.md"}},
	})
	if !strings.Contains(body, "WHAT THE DAEMON THAT MAILED YOU CARRIES") {
		t.Errorf("the ceiling block did not reach the mail body:\n%s", body)
	}
	idx := strings.Index(body, "pogo agent prompt install")
	if idx < 0 {
		t.Fatal("the install command vanished from the notice")
	}
	// The warning must sit in the same few lines as the command.
	near := body[idx:min(idx+300, len(body))]
	if !strings.Contains(near, "no-op") {
		t.Errorf("the install command carries no warning beside it:\n%s", near)
	}
}
