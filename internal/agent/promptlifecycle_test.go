package agent

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedPromptsNeverShipAutoStartWithoutRestart is the flag-coupling
// constraint mg-7d20 was asked to give a durable home, in the one form that
// cannot go stale: an executable invariant.
//
// THE RULE. No shipped prompt may declare auto_start = true together with
// restart_on_crash = false.
//
// WHY. That combination is the only shape that can reach cmd/pogod's
// desired-state fall-through with expected=true while the agent is durably dead
// — registry entry gone, witness gone, and auto_start still saying "should be
// running" — which leaves a mail-check firing at nobody (mg-8677). The registry
// arm of registryLiveness.AgentState already refuses to let auto_start override
// a corpse it can SEE; this is about the case where it cannot see one. With
// restart_on_crash = true that arm returns AgentAlive and the fall-through is
// never reached, so both-true is the safe form and is what every healthy crew
// agent already does.
//
// WHY A TEST AND NOT A COMMENT, AND NOT A doctor --check. The constraint's first
// proposed home was a ticket, which dies when the ticket closes; the second was a
// comment beside the flags, which is real but only reaches someone already
// editing that file. A config-invariant check inside `pogo doctor --check` was
// considered and DECLINED, correctly: mg-10e3 records that nothing on this host
// reads that checklist on a cadence, and an instrument that cannot go red for the
// failure it names is worse than none, because its presence is the reason nobody
// builds the one that would work. This test runs on every `./build.sh` and in
// every refinery gate, needs no cadence and no reader, and goes red in the diff
// that introduces the combination. The comment in crew/doctor.md stays as the
// pointer for whoever is looking at the flags themselves.
//
// SCOPE, stated rather than assumed: this covers the prompts pogo SHIPS. A
// deployment's own ~/.pogo/agents tree is not in this repo and cannot be policed
// from here — `pogo agent roster` reports the same combination against a live
// tree, and is the reader for that half.
func TestEmbeddedPromptsNeverShipAutoStartWithoutRestart(t *testing.T) {
	var checked int
	err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Errorf("read embedded %s: %v", path, err)
			return nil
		}
		meta, _, err := parsePromptFrontmatterBytes(data)
		if err != nil {
			t.Errorf("%s: embedded prompt frontmatter does not parse: %v", path, err)
			return nil
		}
		checked++
		if meta == nil {
			return nil
		}
		if autoStartWithoutRestart(meta) {
			t.Errorf("%s declares auto_start = true with restart_on_crash = false.\n"+
				"These two flags are COUPLED: that pairing is the only shape that can reach\n"+
				"pogod's desired-state fall-through with expected=true over a durably dead\n"+
				"agent, leaving its mail-check firing at nobody (mg-8677). Set\n"+
				"restart_on_crash = true alongside auto_start, or leave auto_start = false.\n"+
				"See registryLiveness.AgentState in cmd/pogod/main.go.", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded prompts: %v", err)
	}
	if checked == 0 {
		t.Fatal("checked no embedded prompts — an invariant that scans nothing passes vacuously, " +
			"which is the failure mode this whole ticket is about")
	}
	t.Logf("checked %d embedded prompt(s)", checked)
}

// autoStartWithoutRestart is the coupling predicate, named so it can be
// exercised directly. The walk above can only ever prove the SHIPPED prompts are
// clean, which is the same thing a predicate that returns false unconditionally
// would prove — and "the detector passed" meaning "the detector cannot fire" is
// the exact confusion mg-7d20 is about. TestCouplingPredicateGoesRed is the
// other half.
//
// It requires restart_on_crash to be DECLARED. An auto_start prompt that omits
// the flag inherits the crew always-on default (ResolveRestartOnCrash), so it is
// already both-true and not a finding.
func autoStartWithoutRestart(meta *AgentMeta) bool {
	if meta == nil {
		return false
	}
	return meta.AutoStart && meta.HasField("restart_on_crash") && !meta.RestartOnCrash
}

// TestCouplingPredicateGoesRed measures the invariant's red path, which the
// green scan above cannot: an unfireable check and a clean fleet produce the
// same passing test.
func TestCouplingPredicateGoesRed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"the forbidden pairing", "+++\nauto_start = true\nrestart_on_crash = false\n+++\n", true},
		{"both true — every healthy crew agent", "+++\nauto_start = true\nrestart_on_crash = true\n+++\n", false},
		{"doctor's on-demand pairing", "+++\nauto_start = false\nrestart_on_crash = false\n+++\n", false},
		{"representative's shape", "+++\nauto_start = false\nrestart_on_crash = true\n+++\n", false},
		{"auto_start alone inherits the crew default", "+++\nauto_start = true\n+++\n", false},
		{"no frontmatter at all", "# just a body\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, _, err := parsePromptFrontmatterBytes([]byte(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := autoStartWithoutRestart(meta); got != tc.want {
				t.Errorf("autoStartWithoutRestart = %v, want %v for:\n%s", got, tc.want, tc.body)
			}
		})
	}
}

// TestDoctorPromptCarriesTheCouplingNote pins mayor's acceptance criterion for
// mg-7d20: the coupling constraint must live in the EMBEDDED source beside the
// flags it constrains, not in the installed copy at ~/.pogo/agents/crew/doctor.md
// (editing that one FREEZES it against future prompt updates) and not in a ticket
// (which dies when the ticket closes).
//
// It asserts substance, not prose: the note must name the other flag, the
// defect, and the safe form. Anyone rewording it keeps the load-bearing parts or
// this goes red.
func TestDoctorPromptCarriesTheCouplingNote(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/crew/doctor.md")
	if err != nil {
		t.Fatalf("read embedded doctor prompt: %v", err)
	}
	head, _, ok := strings.Cut(string(data), "\n+++")
	if !ok {
		t.Fatal("embedded doctor prompt has no frontmatter block")
	}
	for _, want := range []string{"auto_start", "restart_on_crash", "mg-8677", "COUPLED"} {
		if !strings.Contains(head, want) {
			t.Errorf("the coupling note in the doctor frontmatter no longer mentions %q.\n"+
				"It is the only warning a reader gets before flipping the flag; keep it or\n"+
				"move it somewhere a reader of these two lines will still see.\nfrontmatter:\n%s",
				want, head)
		}
	}
	// And the note must not have drifted from the flags it describes: doctor
	// still ships on-demand, so the note's premise still holds.
	meta, _, err := parsePromptFrontmatterBytes(data)
	if err != nil {
		t.Fatalf("parse embedded doctor frontmatter: %v", err)
	}
	if meta.AutoStart {
		t.Error("doctor now declares auto_start = true; the coupling note above the flags " +
			"describes a decision that no longer holds and must be rewritten with the change")
	}
}

// TestEmbeddedPromptPathsAreStable guards the walk above from passing vacuously
// if the embed root is ever renamed: a scan over zero files is the same silence
// this ticket exists to end, one level up.
func TestEmbeddedPromptPathsAreStable(t *testing.T) {
	for _, want := range []string{
		"prompts/mayor.md",
		"prompts/crew/doctor.md",
		"prompts/pm/pm-template.md",
	} {
		if _, err := defaultPrompts.ReadFile(want); err != nil {
			t.Errorf("embedded prompt %s is gone (%v) — update the coupling invariant's scope "+
				"with it, or it will police a set that no longer contains what it was written for",
				filepath.ToSlash(want), err)
		}
	}
}
