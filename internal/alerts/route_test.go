package alerts

import (
	"errors"
	"os/exec"
	"sync"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// drellem2/pogo#262 slice 3: route.

// withRoutes installs m and a box probe for one test and captures what the
// seam records. exists nil treats every box as existing.
func withRoutes(t *testing.T, m map[Name]Route, exists func(string) bool) *[]events.Event {
	t.Helper()
	SetRoutes(m)
	oldExists := BoxExists
	BoxExists = exists
	knownBoxes = sync.Map{}
	var got []events.Event
	old := emitEvent
	emitEvent = func(e events.Event) { got = append(got, e) }
	t.Cleanup(func() {
		SetRoutes(nil)
		BoxExists = oldExists
		knownBoxes = sync.Map{}
		emitEvent = old
	})
	return &got
}

type sent struct{ to, subject string }

func recorder(fail map[string]error) (MailFunc, *[]sent) {
	var log []sent
	return func(to, _, subject, _ string) error {
		log = append(log, sent{to, subject})
		return fail[to]
	}, &log
}

// The primary copy goes to the route; any other copy (the escalation box's,
// a merge author's) is untouched — escalation_box stays the default target
// for escalations.
func TestMailRoutesOnlyThePrimaryCopy(t *testing.T) {
	evs := withRoutes(t, map[Name]Route{
		StrandedWorkBranch: {To: "human", Match: "mayor", Fallback: "mayor", Source: "k"},
	}, nil)
	via, log := recorder(nil)

	for _, to := range []string{"mayor", "pm-pogo"} {
		if err := Mail(StrandedWorkBranch, via, to, "pogod", "s", "b"); err != nil {
			t.Fatal(err)
		}
	}
	want := []sent{{"human", "s"}, {"pm-pogo", "s"}}
	if len(*log) != 2 || (*log)[0] != want[0] || (*log)[1] != want[1] {
		t.Errorf("sent %+v, want %+v", *log, want)
	}
	if len(*evs) != 0 {
		t.Errorf("a usable route recorded %+v", *evs)
	}
	// Positive control: an alert with no route is sent as addressed.
	if err := Mail(RefineryMerged, via, "mayor", "refinery", "m", "b"); err != nil || (*log)[2].to != "mayor" {
		t.Errorf("unrouted alert: err=%v sent=%+v", err, *log)
	}
}

// A route with no Match (a per-message recipient) applies to every send.
func TestMailRoutesEverySendWhenMatchIsEmpty(t *testing.T) {
	withRoutes(t, map[Name]Route{FilerNotifyCompleted: {To: "pm-pogo", Source: "k"}}, nil)
	via, log := recorder(nil)
	for _, to := range []string{"cat-a", "cat-b"} {
		_ = Mail(FilerNotifyCompleted, via, to, "pogod", "s", "b")
	}
	if len(*log) != 2 || (*log)[0].to != "pm-pogo" || (*log)[1].to != "pm-pogo" {
		t.Errorf("sent %+v", *log)
	}
}

// An unknown box falls back to the default recipient and is recorded; mail
// is never dropped. A box found once is not probed again.
func TestMailUnknownRouteFallsBackAndIsRecorded(t *testing.T) {
	probes := 0
	known := false
	evs := withRoutes(t, map[Name]Route{
		RefineryMerged: {To: "esc-box", Match: "mayor", Fallback: "mayor", Source: `[alerts."refinery.merged"] route = "esc-box"`},
	}, func(box string) bool { probes++; return known })
	via, log := recorder(nil)

	if err := Mail(RefineryMerged, via, "mayor", "refinery", "MERGED x", "b"); err != nil {
		t.Fatalf("fallback send returned %v", err)
	}
	if len(*log) != 1 || (*log)[0].to != "mayor" {
		t.Fatalf("sent %+v, want only the fallback to mayor", *log)
	}
	if len(*evs) != 1 {
		t.Fatalf("events %+v, want one alert_route_invalid", *evs)
	}
	e := (*evs)[0]
	if e.EventType != EventRouteInvalid || e.Details["alert"] != "refinery.merged" || e.Details["route"] != "esc-box" ||
		e.Details["fallback_to"] != "mayor" || e.Details["phase"] != "send" || e.Details["subject"] != "MERGED x" {
		t.Errorf("event %+v", e)
	}

	// The box is created: the next send uses it, and later sends do not
	// probe again.
	known = true
	_ = Mail(RefineryMerged, via, "mayor", "refinery", "s2", "b")
	_ = Mail(RefineryMerged, via, "mayor", "refinery", "s3", "b")
	if (*log)[1].to != "esc-box" || (*log)[2].to != "esc-box" {
		t.Errorf("sent %+v after the box appeared", *log)
	}
	if probes != 2 {
		t.Errorf("probed %d times, want 2 (once unknown, once found)", probes)
	}
}

// A routed send that fails is delivered to the fallback, and the fallback's
// result is what Mail returns.
func TestMailFailedRoutedSendFallsBack(t *testing.T) {
	evs := withRoutes(t, map[Name]Route{RefineryMerged: {To: "esc-box", Match: "mayor", Fallback: "mayor", Source: "k"}}, nil)
	via, log := recorder(map[string]error{"esc-box": errors.New("no_such_mailbox")})
	if err := Mail(RefineryMerged, via, "mayor", "refinery", "s", "b"); err != nil {
		t.Errorf("Mail = %v, want the fallback's nil", err)
	}
	if len(*log) != 2 || (*log)[0].to != "esc-box" || (*log)[1].to != "mayor" {
		t.Errorf("sent %+v", *log)
	}
	if len(*evs) != 1 || (*evs)[0].EventType != EventRouteInvalid {
		t.Errorf("events %+v", *evs)
	}
	// Both failing: the fallback's error is returned, so the caller still
	// records an undelivered notice.
	via2, _ := recorder(map[string]error{"esc-box": errors.New("a"), "mayor": errSentinel{}})
	if err := Mail(RefineryMerged, via2, "mayor", "refinery", "s", "b"); err != (errSentinel{}) {
		t.Errorf("Mail = %v, want the fallback's error", err)
	}
}

// A notify_to alias routes to the box the watcher already addresses: it
// changes nothing while that box exists, and falls back when it does not.
func TestMailAliasRouteOnlyAddsTheFallback(t *testing.T) {
	exists := true
	withRoutes(t, map[Name]Route{AckWatchFinding: {To: "pm-pogo", Match: "pm-pogo", Fallback: "mayor", Source: "[ack_watch] notify_to"}},
		func(string) bool { return exists })
	via, log := recorder(nil)
	_ = Mail(AckWatchFinding, via, "pm-pogo", "ack-watch", "s", "b")
	_ = Mail(AckWatchFinding, via, "human", "ack-watch", "s", "b") // escalation copy
	if (*log)[0].to != "pm-pogo" || (*log)[1].to != "human" {
		t.Errorf("sent %+v", *log)
	}
	knownBoxes = sync.Map{}
	exists = false
	_ = Mail(AckWatchFinding, via, "pm-pogo", "ack-watch", "s", "b")
	if (*log)[2].to != "mayor" {
		t.Errorf("unknown notify_to box: sent %+v, want the coordinator", *log)
	}
}

// Mute wins over route: a muted alert is withheld wherever it is routed.
func TestMailMuteWinsOverRoute(t *testing.T) {
	withRoutes(t, map[Name]Route{RefineryMerged: {To: "esc-box", Source: "k"}}, nil)
	evs := withMutes(t, map[Name]string{RefineryMerged: "m"})
	via, log := recorder(nil)
	_ = Mail(RefineryMerged, via, "mayor", "refinery", "s", "b")
	if len(*log) != 0 || len(*evs) != 1 || (*evs)[0].EventType != EventMuted {
		t.Errorf("sent %+v events %+v", *log, *evs)
	}
}

func TestMailOutputRoutesAndFallsBack(t *testing.T) {
	evs := withRoutes(t, map[Name]Route{StrandedWorkBranch: {To: "esc-box", Match: "mayor", Fallback: "mayor", Source: "k"}}, nil)
	var argv [][]string
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		argv = append(argv, args)
		if args[2] == "esc-box" {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	defer func() { execCommand = old }()

	if _, err := MailOutput(StrandedWorkBranch, "mayor", "pogod", "s", "b"); err != nil {
		t.Errorf("MailOutput = %v", err)
	}
	if len(argv) != 2 || argv[0][2] != "esc-box" || argv[1][2] != "mayor" {
		t.Errorf("ran %v, want the route then the fallback", argv)
	}
	if len(*evs) != 1 || (*evs)[0].EventType != EventRouteInvalid {
		t.Errorf("events %+v", *evs)
	}
}

func TestPrimaryFollowsTheDefaultRule(t *testing.T) {
	cases := map[Name]string{
		AckWatchFinding:      RoleCoordinator,
		RefineryMerged:       RoleCoordinator,
		RefineryMergeFailed:  RoleCoordinator, // the author's copy is not the primary
		StrandedWorkBranch:   RoleCoordinator,
		CredExpiryWarning:    RoleEscalationBox,
		BlindWatchOutOfBand:  RoleEscalationBox,
		FilerNotifyCompleted: "",
		CrewResetNotice:      "",
		ConditionNotice:      "",
	}
	for n, want := range cases {
		d, _ := Lookup(n)
		if got := Primary(d); got != want {
			t.Errorf("Primary(%s) = %q, want %q", n, got, want)
		}
	}
}

// The listing shows the routed recipient, the key that set it, and an unusable
// route's fallback.
func TestRowsShowRoutes(t *testing.T) {
	s := Settings{
		Coordinator: "mayor", EscalationBox: "human",
		Routes: map[Name]Route{
			RefineryMerged:     {To: "human", Match: "mayor", Fallback: "mayor", Source: "R"},
			StrandedWorkBranch: {To: "ghost", Match: "mayor", Fallback: "mayor", Source: "S"},
			CrewResetNotice:    {To: "pm-pogo", Source: "C"},
		},
		RouteInvalid: map[Name]string{StrandedWorkBranch: "no mailbox named ghost"},
	}
	by := map[Name]Row{}
	for _, r := range Rows(s, false) {
		by[r.Name] = r
	}
	if r := by[RefineryMerged]; r.Effective != "human" || r.Route != "human" || r.Source != "R" {
		t.Errorf("refinery.merged: %+v", r)
	}
	if r := by[StrandedWorkBranch]; r.RouteInvalid == "" || r.Effective != "mayor (route to ghost is unusable: no mailbox named ghost)" {
		t.Errorf("stranded_work.branch: %+v", r)
	}
	if r := by[CrewResetNotice]; r.Effective != "pm-pogo (instead of the crew agent)" {
		t.Errorf("crew_reset.notice: %+v", r)
	}
	if r := by[AckWatchFinding]; r.Source != "default" || r.Route != "" {
		t.Errorf("control: unrouted row %+v", r)
	}
}
