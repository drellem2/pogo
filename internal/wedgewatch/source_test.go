package wedgewatch

import (
	"errors"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/credexpiry"
	"github.com/drellem2/pogo/internal/hostload"
)

var convNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// TestCredentialFromNeverReadsAbsenceAsHealth pins the conversion that decides
// whether the classifier is allowed to name a bad credential at all.
//
// The dangerous mistake here is the quiet one: a credential that could not be
// read rendering as a credential that is fine. `StateAbsent` and
// `StateUnreadable` must both come back Readable=false, which routes to
// CauseUnknown rather than to a verdict.
func TestCredentialFromNeverReadsAbsenceAsHealth(t *testing.T) {
	for _, st := range []credexpiry.Status{
		{State: credexpiry.StateAbsent, Reason: credexpiry.ReasonItemNotFound},
		{State: credexpiry.StateUnreadable, Reason: credexpiry.ReasonReadTimedOut},
		{State: credexpiry.StateUnreadable, Reason: credexpiry.ReasonFieldMissing},
	} {
		got := credentialFrom(st, convNow)
		if got.Readable {
			t.Errorf("%s read as readable — 'could not look' must never render as 'fine'", st.State)
		}
		if got.RefreshValid {
			t.Errorf("%s claimed a valid refresh grant it never read", st.State)
		}
		if got.Reason == "" {
			t.Errorf("%s carries no reason, so a report cannot say why it is blind", st.State)
		}
	}
}

// TestCredentialFromReadsOnlyTheRefreshGrant pins the field choice. The 8-hour
// access-token expiry is deliberately ignored: internal/credexpiry records that
// it is routinely in the past on a healthy machine because the harness re-mints
// on demand without rewriting the stored blob, and on 2026-08-04 it was in fact
// VALID with 7.7h left while every agent showed "401 ... has been revoked".
// Deciding on it either way would be noise.
func TestCredentialFromReadsOnlyTheRefreshGrant(t *testing.T) {
	// The live 2026-08-04 shape: access expiry in the past, refresh grant good.
	st := credexpiry.Status{
		State:         credexpiry.StatePresent,
		AccessExpiry:  convNow.Add(-3 * time.Hour),
		RefreshExpiry: convNow.Add(395 * time.Hour),
	}
	got := credentialFrom(st, convNow)
	if !got.Readable {
		t.Fatal("a present credential must be readable")
	}
	if !got.RefreshValid {
		t.Error("a stale ACCESS expiry was allowed to invalidate the credential — that reading " +
			"would have manufactured a false 'credential is bad' verdict during the incident")
	}

	lapsed := credexpiry.Status{State: credexpiry.StatePresent, RefreshExpiry: convNow.Add(-time.Hour)}
	if credentialFrom(lapsed, convNow).RefreshValid {
		t.Error("a lapsed refresh grant read as valid")
	}
}

// TestHostViewFromNeverReadsAnUnmeasurableHostAsIdle is the same rule for the
// newest input, and the specific trap hostload names: below its source's
// resolution the differenced CPU figure is not a small number, it is zero — for
// a saturated host exactly as much as an idle one. Reading that as headroom is
// how a guard goes blind (mg-79e3), and here it would report a starved agent as
// a wedged one.
func TestHostViewFromNeverReadsAnUnmeasurableHostAsIdle(t *testing.T) {
	cases := []struct {
		name   string
		sample hostload.Sample
		err    error
	}{
		{"read failed", hostload.Sample{}, errors.New("ps: exit status 1")},
		{"unresolvable window", hostload.Sample{Cores: 10, Unresolvable: "window too short"}, nil},
		{"no cores", hostload.Sample{Cores: 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostViewFrom(tc.sample, tc.err)
			if got.Readable {
				t.Fatalf("%s read as a measurement", tc.name)
			}
			if got.Saturated {
				t.Errorf("%s claimed a saturation verdict it could not compute", tc.name)
			}
			if got.Reason == "" {
				t.Errorf("%s carries no reason, so a report cannot say why it is blind", tc.name)
			}
		})
	}
}

// TestHostViewFromCarriesNoProcessOutput pins the sanitisation rule inherited
// from internal/credexpiry: an error's text can quote whatever the process was
// looking at, so reasons come from a fixed vocabulary and never from err.
func TestHostViewFromCarriesNoProcessOutput(t *testing.T) {
	got := hostViewFrom(hostload.Sample{}, errors.New("secret-looking-output"))
	if got.Reason != ReasonHostReadFailed {
		t.Errorf("reason = %q, want the fixed constant — an error's text can quote its input", got.Reason)
	}
}

// TestHostViewFromMeasuresUsedCoresNotTheLoadAverage is the positive control
// for the saturation reading, and for the instrument CHOICE.
//
// The 2026-08-05 report described the load event as "1-min average 300 on a
// 10-core box", so the load average is the obvious thing to key on. It is the
// wrong one: internal/hostload disqualified it with a measurement on this very
// host (mg-1b8c) where a load average of 214 coincided with roughly 7.5 of 10
// cores actually in use, because Darwin counts uninterruptible-sleep tasks too.
// The row below carries exactly that shape — an alarming load average beside a
// host that still has headroom — and must NOT read as saturated.
func TestHostViewFromMeasuresUsedCoresNotTheLoadAverage(t *testing.T) {
	full := hostViewFrom(hostload.Sample{
		Cores: 10, FleetCores: 9.0, ExternalCores: 0.8, Attributed: true, LoadAvg1: 300,
	}, nil)
	if !full.Readable || !full.Saturated {
		t.Errorf("9.8 of 10 cores in use did not read as saturated: %+v", full)
	}
	if full.UsedCores < 9.7 || full.Cores != 10 {
		t.Errorf("the view must carry the measurement it acted on; got %+v", full)
	}

	// mg-1b8c's actual measurement: load average 214, ~7.5 of 10 cores in use.
	ioHeavy := hostViewFrom(hostload.Sample{
		Cores: 10, FleetCores: 6.5, ExternalCores: 1.0, Attributed: true, LoadAvg1: 214,
	}, nil)
	if ioHeavy.Saturated {
		t.Error("a host with 2.5 free cores read as saturated because its load average was high — " +
			"that is the instrument mg-1b8c disqualified, and keying on it would excuse a real " +
			"wedge whenever something was doing heavy I/O")
	}
}

// TestRegistrySourceWithNoRegistryIsAnError keeps the nil case from rendering
// as a clean fleet.
func TestRegistrySourceWithNoRegistryIsAnError(t *testing.T) {
	_, err := RegistrySource(nil, nil, nil)(convNow)
	if !errors.Is(err, ErrNoRegistry) {
		t.Fatalf("err = %v, want ErrNoRegistry — a detector that cannot look has not found a clean fleet", err)
	}
}
