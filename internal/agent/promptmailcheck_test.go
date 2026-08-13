package agent

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// Every shipped prompt boots a long-lived agent, and every long-lived agent is
// reachable ONLY through a mail-check schedule it registers for itself. This
// test is the reason crew/doctor.md's registration cannot silently go missing
// again.
//
// # Why a corpus-wide test rather than one assertion about doctor
//
// doctor.md was the only prompt under prompts/ with no `pogo schedule`
// registration, and it stayed that way through two deaf incidents. The first
// (2026-07-22) was closed by the mayor hand-registering `mail-check-doctor`
// */10 after 24h44m deaf; that entry was gone eight days later and doctor
// respawned deaf, because reachability is a PER-BOOT property and a one-off
// registration cannot establish one. Adding the paragraph to doctor.md fixes
// the instance. What fixes the class is that the next crew prompt someone adds
// fails this test on the day it is added rather than on the day something
// mailed it.
//
// The population is DERIVED from the corpus walk, not enumerated here. An
// enumeration would have to be edited to cover a new prompt — the same
// "someone must remember" dependency that produced the defect.
func TestEveryShippedPromptRegistersAMailCheckSchedule(t *testing.T) {
	// registration matches the shape all shipped prompts use:
	//
	//	pogo schedule <agent> --cron "<cron>" --id mail-check-<suffix>
	//
	// The agent token varies by role ($POGO_AGENT_NAME, a literal name, a
	// {{.Coordinator}} placeholder), so it is matched loosely; the parts that
	// carry the guarantee — that this is `pogo schedule` and that the id is
	// keyed on mail-check — are matched exactly.
	registration := regexp.MustCompile(`pogo schedule\s+\S+[^\n]*--cron`)
	mailCheckID := regexp.MustCompile(`--id\s+mail-check-(\S+)`)

	// exempt lists prompts that legitimately register no mail-check loop, with
	// the reason. It is empty: every prompt currently shipped boots an agent
	// that must be reachable by mail. A future non-agent document under
	// prompts/ belongs here WITH a reason — adding an entry should feel like a
	// claim someone has to defend, which is exactly what was missing before.
	exempt := map[string]string{}

	var checked int
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		if why, ok := exempt[p]; ok {
			t.Logf("%s: exempt (%s)", p, why)
			return nil
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			return err
		}
		checked++
		body := string(b)

		if !registration.MatchString(body) {
			t.Errorf("%s: no `pogo schedule ... --cron` registration.\n"+
				"An agent booted from this prompt is DEAF: it can be mailed and nothing "+
				"will ever wake it to read the mail. Add the startup registration the "+
				"other prompts use, or add an entry to exempt with a reason.", p)
			return nil
		}

		ids := mailCheckID.FindAllStringSubmatch(body, -1)
		if len(ids) == 0 {
			t.Errorf("%s: registers a schedule but none with a `--id mail-check-<agent>`.\n"+
				"The mail-check loop is the reachability channel; a schedule that is not "+
				"one does not make the agent reachable.", p)
			return nil
		}
		for _, m := range ids {
			suffix := strings.TrimSuffix(m[1], `"`)
			if suffix == "" {
				// A bare `mail-check` id is not merely untidy: pogod's registry
				// compaction has purged short / generic ids after ~1h (mg-8e5d),
				// so the loop disappears while the agent keeps running.
				t.Errorf("%s: schedule id %q is not suffixed with an agent identity; "+
					"generic ids have been purged by registry compaction (mg-8e5d)", p, m[0])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking shipped prompts: %v", err)
	}
	if checked == 0 {
		// An empty walk passing every assertion is the failure mode this whole
		// lineage is about: "nothing is missing a mail loop" and "I looked at
		// nothing" must not render identically.
		t.Fatal("no shipped prompts were checked — the corpus walk found nothing, " +
			"which is not the same statement as a clean corpus")
	}
}

// The doctor prompt is called out separately because its registration is the
// one that was absent, and because two of its details are load-bearing in ways
// the corpus-wide rule above cannot express: the agent it addresses must be
// `doctor` (a schedule addressed to anything else is delivered to someone
// else), and the cadence is an argued choice rather than a copied number.
func TestDoctorPromptRegistersItsOwnMailCheckAtTenMinutes(t *testing.T) {
	b, err := fs.ReadFile(DefaultPromptsFS(), "crew/doctor.md")
	if err != nil {
		t.Fatalf("reading crew/doctor.md: %v", err)
	}
	body := string(b)

	want := []string{
		// Addressed to doctor, keyed on doctor: pogod delivers on the agent
		// name and reaps mail-check-* on that same identity.
		`pogo schedule doctor --cron "*/10 * * * *" --id mail-check-doctor`,
		// at-most-once replay, so a slept-through window produces one catch-up
		// fire rather than one per missed 10-minute mark.
		"--replay once",
		// Re-registration must be stated as a per-boot step. The 2026-07-22
		// hand-registration is precisely the failure of treating it as one-off.
		"every startup",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("crew/doctor.md does not contain %q", w)
		}
	}

	// Exactly one mail-check registered FOR ITSELF. Extra self-cadences are the
	// hazard this counts; the prompt says "do not add additional schedules
	// beyond this one".
	if n := strings.Count(body, "--id mail-check-doctor"); n != 1 {
		t.Errorf("crew/doctor.md should register exactly one mail-check schedule for itself, found %d", n)
	}
	if n := strings.Count(body, "pogo schedule doctor "); n != 1 {
		t.Errorf("crew/doctor.md addresses %d schedule registrations to `doctor`, want exactly 1 — "+
			"a second self-schedule under a different --id would slip past the count above", n)
	}
	// Since mg-477a doctor also RE-REGISTERS a mail-check for an agent it found
	// deaf, so the corpus legitimately carries other `--id mail-check-` strings.
	// Each one must be a placeholder addressed to some other agent; a concrete
	// id that is not `doctor` would be a schedule quietly delivered elsewhere.
	for _, occ := range strings.Split(body, "--id mail-check-")[1:] {
		suffix := occ
		if i := strings.IndexAny(suffix, " \n\t`"); i >= 0 {
			suffix = suffix[:i]
		}
		if suffix != "doctor" && suffix != "<name>" {
			t.Errorf("crew/doctor.md registers `--id mail-check-%s`: a mail-check keyed on "+
				"anything but `doctor` or the `<name>` placeholder is delivered to someone else", suffix)
		}
	}
}
