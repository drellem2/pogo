package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// drellem2/pogo#307: a work item's correlation id comes from a TAG whose
// prefix is configuration ([events] corr_tag_prefix), never hardcoded. These
// tests pin the three cases Daniel's ruling requires: the shipped default
// ("mail:") produces corr, a non-default prefix produces corr from ITS tag and
// ignores a mail: tag, and an unset mapping produces no corr at all — even when
// the item carries a mail: tag.

// corrStore writes one work item carrying tags into a fresh macguffin store
// fixture and returns the store root.
func corrStore(t *testing.T, id, tags string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\ntype: task\ntags: " + tags + "\n---\n# " + id + "\n"
	if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// assertCorr checks details.corr: equal to want when want is non-empty, and
// ABSENT (not "") when want is empty — "absent, never wrong".
func assertCorr(t *testing.T, ev map[string]any, want string) {
	t.Helper()
	details, _ := ev["details"].(map[string]any)
	got, present := details["corr"]
	if want == "" {
		if present {
			t.Errorf("%v carries details.corr=%v; want it absent", ev["event_type"], got)
		}
		return
	}
	if got != want {
		t.Errorf("%v details.corr = %v (present=%v), want %q", ev["event_type"], got, present, want)
	}
}

const (
	corrTestItem  = "mg-c307"
	corrTestMail  = "1794926654664771000.55118.2000"
	corrTestSrc   = "ext-42"
	corrTestBoth  = "[gh-issue, mail:" + corrTestMail + ", src:" + corrTestSrc + "]"
	corrTestMailT = "[gh-issue, mail:" + corrTestMail + "]"
)

func TestCorrForItem(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, tags, want string
	}{
		// The shipped default, read from config rather than spelled here, so the
		// test follows the default if it ever changes.
		{"default config reads mail:", config.DefaultCorrTagPrefix, corrTestMailT, corrTestMail},
		// Acceptance 1: a non-default prefix works, and a mail: tag is then ignored.
		{"non-default prefix", "src:", corrTestBoth, corrTestSrc},
		{"non-default prefix ignores mail:", "src:", corrTestMailT, ""},
		// Acceptance 2: an unset mapping emits no corr, even with a mail: tag.
		{"unset mapping", "", corrTestBoth, ""},
		{"tag absent", "mail:", "[gh-issue]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.corrStoreRoot = corrStore(t, corrTestItem, tc.tags)
			reg.SetCorrTagPrefix(tc.prefix)
			if got := reg.corrForItem(corrTestItem); got != tc.want {
				t.Errorf("corrForItem = %q, want %q", got, tc.want)
			}
		})
	}

	// An item the store does not have, and no id at all: no corr, no error.
	reg := newDrainTestRegistry(t)
	reg.corrStoreRoot = corrStore(t, corrTestItem, corrTestMailT)
	reg.SetCorrTagPrefix("mail:")
	if got := reg.corrForItem("mg-nope"); got != "" {
		t.Errorf("unknown item: corrForItem = %q, want none", got)
	}
	if got := reg.corrForItem(""); got != "" {
		t.Errorf("empty id: corrForItem = %q, want none", got)
	}
}

// A bare registry — the zero value — has no mapping. corr is opt-in through
// SetCorrTagPrefix, which pogod calls from config; nothing in this package
// defaults to "mail:".
func TestCorrRegistryZeroValueIsUnset(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.corrStoreRoot = corrStore(t, corrTestItem, corrTestMailT)
	if got := reg.corrForItem(corrTestItem); got != "" {
		t.Errorf("bare registry: corrForItem = %q, want none (no mapping configured)", got)
	}
}

// TestSpawnRequestedCarriesCorr drives the real handler. The drain gate refuses
// the spawn right after agent_spawn_requested is written, so the test reads
// exactly the event the handler emitted for each configuration.
func TestSpawnRequestedCarriesCorr(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, tags, want string
	}{
		{"corr-default", config.DefaultCorrTagPrefix, corrTestBoth, corrTestMail},
		{"corr-src", "src:", corrTestBoth, corrTestSrc},
		{"corr-src-ignores-mail", "src:", corrTestMailT, ""},
		{"corr-unset", "", corrTestBoth, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := useTempEventLog(t)
			reg := newDrainTestRegistry(t)
			reg.corrStoreRoot = corrStore(t, corrTestItem, tc.tags)
			reg.SetCorrTagPrefix(tc.prefix)
			reg.SetDraining(true)

			rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
				Name: tc.name, Id: corrTestItem, Repo: "/some/repo", Template: "polecat-build-pr",
			})
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("spawn while draining = %d, want 503", rr.Code)
			}
			evs := readEventLines(t, logPath)
			i := eventIndex(evs, "agent_spawn_requested", "cat-"+tc.name)
			if i < 0 {
				t.Fatal("no agent_spawn_requested event")
			}
			assertCorr(t, evs[i], tc.want)
			// The refusal is not one of the corr events.
			if j := eventIndex(evs, "agent_spawn_failed", "cat-"+tc.name); j >= 0 {
				assertCorr(t, evs[j], "")
			}
		})
	}
}

// The Agent carries the corr it was spawned with onto agent_spawned,
// agent_stopped (both branches) and agent_crashed; an agent with none writes
// none.
func TestLifecycleEventsCarryCorr(t *testing.T) {
	path := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetClaimReleaser(&recordingReleaser{})

	for _, tc := range []struct {
		name, corr string
		cmd        []string
		stop       bool
		exitEvent  string
	}{
		{"corr-clean", corrTestMail, []string{"true"}, false, "agent_stopped"},
		{"corr-clean-none", "", []string{"true"}, false, "agent_stopped"},
		{"corr-stop", corrTestSrc, []string{"sleep", "30"}, true, "agent_stopped"},
		{"corr-stop-none", "", []string{"sleep", "30"}, true, "agent_stopped"},
		{"corr-crash", corrTestMail, []string{"false"}, false, "agent_crashed"},
		{"corr-crash-none", "", []string{"false"}, false, "agent_crashed"},
	} {
		a, err := reg.Spawn(SpawnRequest{
			Name: tc.name, Type: TypePolecat, Command: tc.cmd, WorkItemID: corrTestItem, Corr: tc.corr,
		})
		if err != nil {
			t.Fatal(err)
		}
		if tc.stop {
			if err := reg.Stop(tc.name, 2*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		<-a.Done()
		ev := waitForEvent(t, path, "agent_spawned", "cat-"+tc.name, 2*time.Second)
		if ev == nil {
			t.Fatalf("no agent_spawned event for %s", tc.name)
		}
		assertCorr(t, ev, tc.corr)
		ev = waitForEvent(t, path, tc.exitEvent, "cat-"+tc.name, 3*time.Second)
		if ev == nil {
			t.Fatalf("no %s event for %s", tc.exitEvent, tc.name)
		}
		assertCorr(t, ev, tc.corr)
	}
}

// A respawn keeps the corr of the original spawn, on agent_restarted and on
// the agent_spawned the respawn writes.
func TestRestartedCarriesCorr(t *testing.T) {
	testsandbox.Isolate(t)
	path := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	for _, tc := range []struct{ name, corr string }{
		{"corr-restart", corrTestMail},
		{"corr-restart-none", ""},
	} {
		a, err := reg.Spawn(SpawnRequest{
			Name: tc.name, Type: TypeCrew, Command: []string{"true"}, Corr: tc.corr,
		})
		if err != nil {
			t.Fatal(err)
		}
		<-a.Done()
		a.Command = []string{"sleep", "10"}
		na, err := reg.Respawn(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if na.Corr != tc.corr {
			t.Errorf("respawned agent Corr = %q, want %q", na.Corr, tc.corr)
		}
		ev := waitForEvent(t, path, "agent_restarted", "crew-"+tc.name, 2*time.Second)
		if ev == nil {
			t.Fatalf("no agent_restarted event for %s", tc.name)
		}
		assertCorr(t, ev, tc.corr)
	}
}
