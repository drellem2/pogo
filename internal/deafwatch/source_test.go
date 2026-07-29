package deafwatch

import (
	"errors"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// TestFromReport_CarriesBothNameAndIdentity pins the conversion. The two forms
// are consumed by different readers — the bare name is what an operator types
// after `pogo agent diagnose`, the identity is what the notifier matches
// senders against — and collapsing them has bitten this tree before.
func TestFromReport_CarriesBothNameAndIdentity(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	rep := agent.MailLoopReport{
		Scanned: 5, Judged: 3,
		Missing: []agent.MailLoopFinding{{
			Name: "doctor", Identity: "crew-doctor", Type: agent.TypeCrew, Alive: true,
		}},
	}
	snap := FromReport(rep, now)

	if snap.Scanned != 5 || snap.Judged != 3 {
		t.Errorf("Scanned/Judged = %d/%d, want 5/3", snap.Scanned, snap.Judged)
	}
	if len(snap.Missing) != 1 {
		t.Fatalf("Missing = %+v, want one finding", snap.Missing)
	}
	got := snap.Missing[0]
	if got.Name != "doctor" || got.Identity != "crew-doctor" || got.Type != "crew" || !got.Alive {
		t.Errorf("Finding = %+v, want name/identity/type/alive carried through intact", got)
	}
}

// TestRegistrySource_NilRegistryIsAnError: a source that cannot look has not
// found a reachable fleet. Returning an empty Snapshot here would make pogod
// announce nothing and log nothing, which is precisely the silence this package
// exists to end.
func TestRegistrySource_NilRegistryIsAnError(t *testing.T) {
	_, err := RegistrySource(nil)(time.Now())
	if !errors.Is(err, agent.ErrNoMailCheckJudgement) {
		t.Fatalf("err = %v, want ErrNoMailCheckJudgement", err)
	}
}
