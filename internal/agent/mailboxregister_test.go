package agent

import (
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/mailbox"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// fakeMailboxRegistrar records RegisterMailbox calls so tests can assert which
// boxes spawn-polecat provisions, and can make provisioning fail.
type fakeMailboxRegistrar struct {
	mu    sync.Mutex
	names []string
	err   error
}

func (f *fakeMailboxRegistrar) RegisterMailbox(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	return f.err
}

func (f *fakeMailboxRegistrar) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.names...)
}

// canonicalSet reduces recorded registrations to the identities mg resolves them
// to, so an assertion compares boxes rather than spellings — `mg mail register
// mg-wi42` and `mg mail register wi42` provision the same Maildir.
func canonicalSet(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		c := mailbox.Canonical(n)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// TestSpawnPolecatRegistersBothMailboxes is the mg-7dc1 acceptance: spawning a
// polecat provisions the mailboxes it is reachable at, so mail addressed to it
// is delivered rather than refused.
//
// It exists because mg-d639 made `mg mail send` refuse an unregistered
// recipient (no_such_mailbox, exit 3), and NOTHING had ever provisioned a
// polecat's inbox — the old send-side behaviour created the box on first
// delivery, so the omission was invisible. The measurement that closed the
// question: 10 of the 12 most recent polecats had no mailbox under any name, and
// the two that did were repairs someone had already made with --create.
//
// BOTH boxes are asserted, and the fixture's two identities deliberately
// disagree ("pc-boxes" working "mg-wi42") because that disagreement is the whole
// difficulty. Which box holds a polecat's mail is a property of the SENDER, not
// of the polecat (mg-4f8c): a correspondent who typed the work item id creates a
// real box holding real unread mail. Provisioning only the agent name would
// leave the other refused, which is a live failure — pm-pogo hit exactly that
// twice on 2026-08-07, on both name forms.
func TestSpawnPolecatRegistersBothMailboxes(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "boxespc", "# boxes polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	boxes := &fakeMailboxRegistrar{}
	reg.SetMailboxRegistrar(boxes)

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-boxes",
		Template: "boxespc",
		Id:       "mg-wi42",
	})

	got := canonicalSet(boxes.recorded())
	want := []string{"pc-boxes", "wi42"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("spawn provisioned %v, want both boxes %v — a box that is not registered refuses mail addressed to it (mg-d639), so an omitted one makes the polecat unreachable at that name", got, want)
	}
}

// TestSpawnPolecatRegistersEveryMailboxItsNudgeReads is pm-pogo's explicit
// constraint on this fix, as an assertion: spawn-registration must create
// whatever set the polecat's own instructions tell it to read, and the two names
// must AGREE — otherwise this reopens as "registered the box nobody reads".
//
// It compares the provisioned set against the boxes parsed out of the real
// mail-check nudge rather than against a literal, which is the point. A literal
// would only restate what the code does; this fails whenever the two sides
// diverge, in either direction — a box added to the nudge but never provisioned
// (mail to it is refused) and a box provisioned but dropped from the nudge (mail
// to it is delivered and never read) are both caught, and both are silent in
// production.
func TestSpawnPolecatRegistersEveryMailboxItsNudgeReads(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "agreepc", "# agree polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	boxes := &fakeMailboxRegistrar{}
	mc := &fakeMailCheckRegistrar{}
	reg.SetMailboxRegistrar(boxes)
	reg.SetMailCheckRegistrar(mc)

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-agree",
		Template: "agreepc",
		Id:       "mg-7dc1",
	})

	calls := mc.recorded()
	if len(calls) != 1 {
		t.Fatalf("RegisterMailCheck called %d times, want 1", len(calls))
	}
	read := canonicalSet(mailbox.ListInvocations(calls[0].message))
	if len(read) == 0 {
		t.Fatal("the registered mail-check nudge names no mailbox at all; there is nothing for provisioning to agree with")
	}
	provisioned := canonicalSet(boxes.recorded())

	if len(read) != len(provisioned) {
		t.Fatalf("nudge reads %v but spawn provisioned %v — a box in one list and not the other is silent in both directions (refused mail, or delivered mail nobody opens)", read, provisioned)
	}
	for i := range read {
		if read[i] != provisioned[i] {
			t.Fatalf("nudge reads %v but spawn provisioned %v — they must name the same boxes", read, provisioned)
		}
	}
}

// TestSpawnPolecatMailboxFallsBackToNameOnly covers a spawn carrying no work
// item id (an in-place, no-worktree dispatch): there is only one box to
// provision, and provisioning must not invent a second from an empty string.
// Registering "" would either error or create a junk mailbox, and the nudge
// names only the agent name in this case — so the agreement asserted above must
// hold here too.
func TestSpawnPolecatMailboxFallsBackToNameOnly(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "noidboxpc", "# no-id polecat\nbody\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	boxes := &fakeMailboxRegistrar{}
	reg.SetMailboxRegistrar(boxes)

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-noid",
		Template: "noidboxpc",
	})

	got := boxes.recorded()
	if len(got) != 1 || got[0] != "pc-noid" {
		t.Fatalf("spawn provisioned %v, want exactly [pc-noid] — a spawn with no work item has one box, and an empty second name is not a mailbox", got)
	}
}

// TestSpawnPolecatSurvivesMailboxRegistrationFailure holds the non-fatal
// contract. The polecat is already running by the time provisioning is
// attempted, so failing the spawn over it would trade a reachability problem for
// a lost work item and an orphaned worktree.
//
// The positive half of the same property is that the failure is not swallowed:
// registration is attempted for EVERY box rather than stopping at the first
// error, because the names are independent and giving up early would leave a box
// unprovisioned that would have registered fine.
func TestSpawnPolecatSurvivesMailboxRegistrationFailure(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "failboxpc", "# failing-box polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	boxes := &fakeMailboxRegistrar{err: errors.New("mg mail register failed: disk full")}
	reg.SetMailboxRegistrar(boxes)

	// A spawn that returned an agent is a spawn that survived: spawnPolecatViaAPI
	// fails the test on a non-201.
	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-failbox",
		Template: "failboxpc",
		Id:       "mg-wi99",
	})

	if got := len(boxes.recorded()); got != 2 {
		t.Fatalf("attempted %d registrations, want 2 — a failure on one box must not abandon the other", got)
	}
}

// TestSpawnPolecatWithoutMailboxRegistrarStillSpawns covers the nil registrar: a
// bare registry, or a daemon on a host with no macguffin. Provisioning is
// skipped and the polecat still runs — the pre-mg-7dc1 behaviour, which is
// degraded but not broken.
func TestSpawnPolecatWithoutMailboxRegistrarStillSpawns(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "nilboxpc", "# nil-registrar polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-nilbox",
		Template: "nilboxpc",
		Id:       "mg-wi00",
	})
}

// TestPolecatMailboxesMatchesTheNudge is the unit-level version of the agreement
// property, run without a spawn so it also pins the prefix handling.
//
// The `mg-` case is the one worth stating: the nudge names the work item as
// `mg-7dc1` while `mg mail list 7dc1` reads the same box, because mg strips the
// prefix. Provisioning passes the nudge's spelling through untouched and lets mg
// canonicalize — verified against mg v0.3.1-dev.19 on 2026-08-07, where
// `mg mail register mg-abcd` reported {"mailbox":"abcd","created":true}. A pogo
// -side stripper here would be a SECOND canonicalizer, which is the defect
// internal/mailbox exists to prevent.
func TestPolecatMailboxesMatchesTheNudge(t *testing.T) {
	cases := []struct {
		name, agentName, workItemID string
		want                        []string
	}{
		{"prefixed work item", "p7dc1", "mg-7dc1", []string{"7dc1", "p7dc1"}},
		{"bare work item", "pc-x", "wi42", []string{"pc-x", "wi42"}},
		{"no work item", "pc-y", "", []string{"pc-y"}},
		// The historically-agreeing shape: the agent name IS the work item minus
		// its prefix, so there is one box, not two spellings of it.
		{"names collapse to one box", "aa96", "mg-aa96", []string{"aa96"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalSet(polecatMailboxes(tc.agentName, tc.workItemID))
			read := canonicalSet(mailbox.ListInvocations(PolecatMailCheckMessage(tc.agentName, tc.workItemID)))
			if len(got) != len(tc.want) {
				t.Fatalf("polecatMailboxes(%q,%q) = %v, want %v", tc.agentName, tc.workItemID, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("polecatMailboxes(%q,%q) = %v, want %v", tc.agentName, tc.workItemID, got, tc.want)
				}
			}
			if len(read) != len(got) {
				t.Fatalf("provisioned %v but the nudge reads %v", got, read)
			}
		})
	}
}
