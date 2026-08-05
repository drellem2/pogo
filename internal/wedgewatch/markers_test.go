package wedgewatch

import (
	"testing"

	"github.com/drellem2/pogo/internal/claude"
)

// TestEveryEnumeratedStateHasAPositiveControl is the check mg-fc8d asked for by
// name: a detector like this must be PROVEN able to fire before it is trusted
// to stay quiet. Every state markers.go claims to recognise gets a fixture
// reconstructed from the terminal it was read off, and every one must produce
// its signature.
//
// This test is the one that would fail if the enumeration silently stopped
// matching — which is precisely what mg-f36b was: a watcher installed, running,
// and unable to match its own marker for two months, reporting nothing and
// looking healthy the whole time.
func TestEveryEnumeratedStateHasAPositiveControl(t *testing.T) {
	cases := []struct {
		name string
		pty  []byte
		want Signature
	}{
		{"login prompt", wedgedLoginPTY("3m 2s"), SigLoginPrompt},
		{"api 401", wedgedLoginPTY("3m 2s"), SigAPI401},
		{"ENOTFOUND", outagePTY("2m 56s"), SigConnectivity},
		{"rating dialog", ratingDialogPTY("1m 4s"), SigRatingDialog},
		{"rate-limit modal", rateLimitModalPTY("1m 4s"), SigRateLimitModal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Signatures(ScanMarkers(tc.pty, nil))
			if !hasSig(got, tc.want) {
				t.Fatalf("%s did NOT fire: signatures=%v, want %s to be present.\n"+
					"A dead-end state the detector claims to recognise but cannot is worse than "+
					"one it never claimed: it reports quiet and is believed.", tc.name, got, tc.want)
			}
		})
	}
}

// TestConnectivityCodesAllRaiseOneSignature pins that the network-failure
// vocabulary collapses to a single signature. The response for all of them is
// the same, and splitting an outage across two codes would report half of it.
func TestConnectivityCodesAllRaiseOneSignature(t *testing.T) {
	for _, text := range []string{
		"Unable to connect to API (ENOTFOUND api.anthropic.com)",
		"getaddrinfo ENOTFOUND api.anthropic.com",
		"getaddrinfo EAI_AGAIN api.anthropic.com",
	} {
		got := Signatures(ScanMarkers([]byte(text), nil))
		if !hasSig(got, SigConnectivity) {
			t.Errorf("%q raised %v, want a connectivity signature", text, got)
		}
	}
}

// TestModalMarkersSurviveColumnSpacing is the mg-f36b regression, expressed as
// a positive control. The fixtures space their option rows with cursor-forward
// escapes, so if Normalize were ever removed — or changed to collapse
// whitespace to a single space rather than to nothing — these markers would
// stop matching in production while every naively-written test kept passing.
func TestModalMarkersSurviveColumnSpacing(t *testing.T) {
	if !hasSig(Signatures(ScanMarkers(ratingDialogPTY("1m 4s"), nil)), SigRatingDialog) {
		t.Error("the rating dialog did not match through cursor-forward column spacing — " +
			"this is exactly mg-f36b, where the watcher logged zero dismissals for two months")
	}
	if !hasSig(Signatures(ScanMarkers(rateLimitModalPTY("1m 4s"), nil)), SigRateLimitModal) {
		t.Error("the rate-limit modal did not match through cursor-forward column spacing")
	}
}

// TestModalMarkersMatchTheModalWatcher pins the two shared marker strings
// byte-identical to internal/claude's.
//
// They are duplicated rather than imported so this package stays a pure
// detector over plain data, which is the same trade internal/deafwatch makes
// with agent.MailLoopFinding. The trade is only safe with this pin: mg-4421
// owns dismissing these modals, this package owns reporting that the dismissal
// did not win, and two copies of a marker that drift apart would leave the
// second silently blind — the failure this whole family of detectors exists to
// prevent.
func TestModalMarkersMatchTheModalWatcher(t *testing.T) {
	if RatingDialogText != claude.RatingDialogMarker {
		t.Errorf("RatingDialogText = %q, claude.RatingDialogMarker = %q — these must stay identical",
			RatingDialogText, claude.RatingDialogMarker)
	}
	if RateLimitText != claude.RateLimitMarker {
		t.Errorf("RateLimitText = %q, claude.RateLimitMarker = %q — these must stay identical",
			RateLimitText, claude.RateLimitMarker)
	}
}

// TestScanMarkersIsQuietOnHealthyOutput is the negative control. It does NOT by
// itself justify trusting the detector's silence — the test above is what does
// that — but a scanner that fires on ordinary tool output would be muted within
// a day.
func TestScanMarkersIsQuietOnHealthyOutput(t *testing.T) {
	if got := ScanMarkers(workingPTY("12s"), nil); len(got) != 0 {
		t.Errorf("healthy working output raised %v, want none", got)
	}
	if got := ScanMarkers(noCounterPTY(), nil); len(got) != 0 {
		t.Errorf("counterless output raised %v, want none", got)
	}
}

// TestScanMarkersFiresOnAnAgentMerelyWritingAboutTheWedge documents the
// deliberate limitation the hold-down exists to cover.
//
// The marker scan CANNOT distinguish a wedged agent from one writing this very
// package: both have the strings on screen. That is not a bug in the scanner
// and it is not fixable at this layer — it is why the watcher requires the
// agent to be stalled as well, and why DefaultMarkerHoldDown is not zero. If
// this test ever fails, someone has "fixed" the scanner in a way that will make
// it miss the real thing.
func TestScanMarkersFiresOnAnAgentMerelyWritingAboutTheWedge(t *testing.T) {
	got := Signatures(ScanMarkers(quotingPTY("8s"), nil))
	if !hasSig(got, SigLoginPrompt) {
		t.Fatalf("expected the scan to be fooled by quoted markers (got %v) — "+
			"the watcher's hold-down is what handles this, not the scanner", got)
	}
}
