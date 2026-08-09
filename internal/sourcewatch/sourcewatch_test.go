package sourcewatch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC)

const window = 6 * time.Hour

func consumer(label, key, dir string) Consumer {
	return Consumer{
		Label:     label,
		Program:   "/Users/daniel/.pogo/pogo-reminders/bin/poll-mail.sh",
		SourceKey: key,
		Source:    dir,
		PlistPath: "/Users/daniel/Library/LaunchAgents/" + label + ".plist",
	}
}

// fixture builds a SampleFunc/PeerFunc pair from a map of dir -> activity, so a
// verdict's phrasing is exercised without a filesystem that has the fault.
func fixture(dirs map[string]Activity, peers map[string][]string) (SampleFunc, PeerFunc) {
	sample := func(dir string) Activity {
		if a, ok := dirs[dir]; ok {
			a.Dir = dir
			return a
		}
		return Activity{Dir: dir}
	}
	peer := func(c Consumer) []string { return peers[c.Label] }
	return sample, peer
}

func live(count int, ago time.Duration) Activity {
	return Activity{Exists: true, Count: count, Last: now.Add(-ago)}
}

func quiet(ago time.Duration) Activity {
	return Activity{Exists: true, Last: now.Add(-ago)}
}

func verdictFor(t *testing.T, rep Report, label string) Verdict {
	t.Helper()
	for _, v := range rep.Verdicts {
		if v.Consumer.Label == label {
			return v
		}
	}
	t.Fatalf("no verdict for %s in %+v", label, rep.Verdicts)
	return Verdict{}
}

// TestStarvedConsumerIsReported is the ticket, in the arrangement that produced
// it: two jobs running one program, one pointed at the box the fleet writes and
// one pointed at a box nothing writes to. The mis-pointed job is loaded,
// healthy and polling; the only thing that distinguishes it is where the data
// actually is.
func TestStarvedConsumerIsReported(t *testing.T) {
	const daniel = "/m/daniel/new"
	const human = "/m/human/new"
	sample, peers := fixture(
		map[string]Activity{
			daniel: quiet(40 * time.Hour),
			human:  live(9, 5*time.Minute),
		},
		map[string][]string{
			"com.pogo.notify":  {human},
			"com.pogo.deadman": {daniel},
		})

	rep := Evaluate([]Consumer{
		consumer("com.pogo.notify", "MAIL_DIR", daniel),
		consumer("com.pogo.deadman", "MAIL_DIR", human),
	}, sample, peers, now, window)

	notify := verdictFor(t, rep, "com.pogo.notify")
	if notify.Status != StatusStarved {
		t.Fatalf("notify status = %q, want %q; detail = %q", notify.Status, StatusStarved, notify.Detail)
	}
	for _, want := range []string{"MAIL_DIR", daniel, "NOTHING HAS ARRIVED THERE", human} {
		if !strings.Contains(notify.Detail, want) {
			t.Errorf("detail does not mention %q; detail = %q", want, notify.Detail)
		}
	}
	// The finding has to say that the job's own instruments will keep reading
	// green, because that is the whole reason it went unnoticed: every reader of
	// this line has already checked launchctl and seen "healthy".
	if !strings.Contains(notify.Detail, "green") {
		t.Errorf("detail = %q, want it to say the job-level instruments stay green", notify.Detail)
	}

	if dm := verdictFor(t, rep, "com.pogo.deadman"); dm.Status != StatusLive {
		t.Errorf("deadman status = %q, want %q (it is the one carrying the traffic); detail = %q", dm.Status, StatusLive, dm.Detail)
	}
	if len(rep.Findings()) != 1 {
		t.Errorf("Findings() = %d, want exactly the starved consumer", len(rep.Findings()))
	}
}

// TestQuietEverywhereIsNotAPass is the test the ticket demanded this fix pass
// against itself:
//
//	What would this instrument report if the thing it NAMES stopped entirely?
//	If the answer is green, it is measuring its own execution.
//
// The thing this package names is data arriving into the sources consumers
// read. If ALL of it stops, every source is quiet, and the naive predicate
// ("someone has zero while someone else has more") finds nothing — which would
// reproduce the defect inside the fix for it. So a fleet-wide silence must be
// undetermined, and must say so in words.
func TestQuietEverywhereIsNotAPass(t *testing.T) {
	const daniel = "/m/daniel/new"
	const human = "/m/human/new"
	sample, peers := fixture(
		map[string]Activity{
			daniel: quiet(40 * time.Hour),
			human:  quiet(30 * time.Hour),
		},
		map[string][]string{
			"com.pogo.notify":  {human},
			"com.pogo.deadman": {daniel},
		})

	rep := Evaluate([]Consumer{
		consumer("com.pogo.notify", "MAIL_DIR", daniel),
		consumer("com.pogo.deadman", "MAIL_DIR", human),
	}, sample, peers, now, window)

	if len(rep.Findings()) != 0 {
		t.Fatalf("Findings() = %+v, want none: with nothing arriving anywhere there is no comparison that convicts anyone", rep.Findings())
	}
	for _, v := range rep.Verdicts {
		if v.Status != StatusUndetermined {
			t.Errorf("%s status = %q, want %q — a machine where nothing is being written must not read as a clean bill", v.Consumer.Label, v.Status, StatusUndetermined)
		}
		if !strings.Contains(v.Detail, "NOT CHECKED") || !strings.Contains(v.Detail, "not a pass") {
			t.Errorf("%s detail = %q, want it to disclaim rather than read as a pass", v.Consumer.Label, v.Detail)
		}
	}
}

// TestNoPeersIsUndetermined. A consumer with nothing to compare against has not
// been cleared; it has not been examined. Same branch as fleet-wide silence and
// deliberately so — both mean "no comparison was available".
func TestNoPeersIsUndetermined(t *testing.T) {
	const only = "/m/only/new"
	sample, peers := fixture(
		map[string]Activity{only: quiet(40 * time.Hour)},
		map[string][]string{"com.pogo.notify": nil})

	rep := Evaluate([]Consumer{consumer("com.pogo.notify", "MAIL_DIR", only)}, sample, peers, now, window)
	v := verdictFor(t, rep, "com.pogo.notify")
	if v.Status != StatusUndetermined || !strings.Contains(v.Detail, "NOT CHECKED") {
		t.Errorf("status/detail = %q/%q, want undetermined + NOT CHECKED", v.Status, v.Detail)
	}
}

// TestDrainedSourceIsNotStarved. A source a working consumer empties as fast as
// it fills holds no backlog, and is not for that reason dead. Activity.Last
// carries the directory's own mtime — which moves on unlink as well as create —
// precisely so a drained box and an abandoned one are different readings. Get
// this wrong and the check fires on the healthy consumer, which is how a
// detector gets switched off.
func TestDrainedSourceIsNotStarved(t *testing.T) {
	const drained = "/m/human/new"
	const busy = "/m/daniel/new"
	sample, peers := fixture(
		map[string]Activity{
			drained: quiet(2 * time.Minute), // emptied two minutes ago
			busy:    live(4, time.Minute),
		},
		map[string][]string{"com.pogo.relay": {busy}})

	rep := Evaluate([]Consumer{consumer("com.pogo.relay", "MAIL_DIR", drained)}, sample, peers, now, window)
	v := verdictFor(t, rep, "com.pogo.relay")
	if v.Status != StatusLive {
		t.Fatalf("status = %q, want %q for a box with no backlog but recent writes; detail = %q", v.Status, StatusLive, v.Detail)
	}
	if !strings.Contains(v.Detail, "draining") {
		t.Errorf("detail = %q, want it to say why an empty box still counts as live", v.Detail)
	}
}

// TestMissingSourceIsItsOwnVerdict. A consumer polling a path that does not
// exist wants a different remedy than one polling a real box that has gone
// quiet, and folding them together loses that.
func TestMissingSourceIsItsOwnVerdict(t *testing.T) {
	sample, peers := fixture(
		map[string]Activity{"/m/live/new": live(3, time.Minute)},
		map[string][]string{"com.pogo.ghost": {"/m/live/new"}})

	rep := Evaluate([]Consumer{consumer("com.pogo.ghost", "MAIL_DIR", "/m/gone/new")}, sample, peers, now, window)
	v := verdictFor(t, rep, "com.pogo.ghost")
	if v.Status != StatusMissing {
		t.Fatalf("status = %q, want %q; detail = %q", v.Status, StatusMissing, v.Detail)
	}
	if !strings.Contains(v.Detail, "not a directory") || !strings.Contains(v.Detail, "reports no error") {
		t.Errorf("detail = %q, want it to name the absence and say the job stays quiet about it", v.Detail)
	}
}

// TestUnreadableSourceIsNotQuiet. "I could not look" and "nothing arrived" are
// the two readings this package exists to keep apart; a stat error must not
// collapse into the quiet one.
func TestUnreadableSourceIsNotQuiet(t *testing.T) {
	sample, peers := fixture(
		map[string]Activity{
			"/m/denied/new": {Exists: true, Err: errors.New("permission denied")},
			"/m/live/new":   live(3, time.Minute),
		},
		map[string][]string{"com.pogo.blind": {"/m/live/new"}})

	rep := Evaluate([]Consumer{consumer("com.pogo.blind", "MAIL_DIR", "/m/denied/new")}, sample, peers, now, window)
	v := verdictFor(t, rep, "com.pogo.blind")
	if v.Status != StatusUndetermined {
		t.Fatalf("status = %q, want %q; detail = %q", v.Status, StatusUndetermined, v.Detail)
	}
	if !strings.Contains(v.Detail, "not a report that the source is quiet") {
		t.Errorf("detail = %q, want it to disclaim explicitly", v.Detail)
	}
}

// TestCutoverCompletionClearsTheFinding. The expected resolution of the routing
// half of this ticket is that the relay is activated and the primary's box
// starts receiving. When that happens this row must go quiet on its own — a
// control that still fires after the condition is fixed is one somebody deletes.
func TestCutoverCompletionClearsTheFinding(t *testing.T) {
	const daniel = "/m/daniel/new"
	const human = "/m/human/new"
	sample, peers := fixture(
		map[string]Activity{
			daniel: live(7, 3*time.Minute), // the relay is now writing here
			human:  live(9, time.Minute),
		},
		map[string][]string{
			"com.pogo.notify":  {human},
			"com.pogo.deadman": {daniel},
		})

	rep := Evaluate([]Consumer{
		consumer("com.pogo.notify", "MAIL_DIR", daniel),
		consumer("com.pogo.deadman", "MAIL_DIR", human),
	}, sample, peers, now, window)

	if len(rep.Findings()) != 0 {
		t.Fatalf("Findings() = %+v, want none once both sources receive", rep.Findings())
	}
	if rep.Count(StatusLive) != 2 {
		t.Errorf("live count = %d, want 2", rep.Count(StatusLive))
	}
}

// TestNextRepointIsCaughtWithoutEditingThisPackage. The reason the predicate is
// stated generally rather than as "notify must not read daniel/". Here the
// cutover has completed, both jobs read `daniel`, and somebody later re-points
// the DEADMAN at a third box that has gone quiet. Nothing in this package names
// any of these directories, and the finding still lands.
func TestNextRepointIsCaughtWithoutEditingThisPackage(t *testing.T) {
	const daniel = "/m/daniel/new"
	const archive = "/m/archive/new"
	sample, peers := fixture(
		map[string]Activity{
			daniel:  live(11, time.Minute),
			archive: quiet(72 * time.Hour),
		},
		map[string][]string{
			"com.pogo.notify":  {archive},
			"com.pogo.deadman": {daniel},
		})

	rep := Evaluate([]Consumer{
		consumer("com.pogo.notify", "MAIL_DIR", daniel),
		consumer("com.pogo.deadman", "MAIL_DIR", archive),
	}, sample, peers, now, window)

	f := rep.Findings()
	if len(f) != 1 || f[0].Consumer.Label != "com.pogo.deadman" {
		t.Fatalf("Findings() = %+v, want the re-pointed deadman", f)
	}
	if !strings.Contains(f[0].Detail, archive) {
		t.Errorf("detail = %q, want it to name the box that stopped receiving", f[0].Detail)
	}
}

// TestSharedSourceIsSampledOnce. Two consumers on one box is a legitimate
// configuration — it is what completing the cutover produces — and they must
// not be able to disagree about whether that box is live.
func TestSharedSourceIsSampledOnce(t *testing.T) {
	const box = "/m/daniel/new"
	calls := 0
	sample := func(dir string) Activity {
		if dir == box {
			calls++
		}
		return Activity{Dir: dir, Exists: true, Count: 2, Last: now.Add(-time.Minute)}
	}
	peers := func(c Consumer) []string { return []string{"/m/human/new"} }

	rep := Evaluate([]Consumer{
		consumer("com.pogo.notify", "MAIL_DIR", box),
		consumer("com.pogo.deadman", "MAIL_DIR", box),
	}, sample, peers, now, window)

	if calls != 1 {
		t.Errorf("sampled %s %d times, want 1", box, calls)
	}
	if rep.Count(StatusLive) != 2 {
		t.Errorf("live count = %d, want both consumers live", rep.Count(StatusLive))
	}
}

// TestFindingNamesBoundedEvidence. Measured against the live machine before
// this cap existed: the sibling family of ~/.macguffin/mail/daniel/new is every
// agent mailbox in the store, and a finding that enumerated the live ones
// rendered a 400KB doctor row. The count must be exact and the naming bounded —
// a finding nobody can read is one nobody reads, which is how a detector for an
// invisible fault becomes invisible itself.
func TestFindingNamesBoundedEvidence(t *testing.T) {
	const starved = "/m/daniel/new"
	dirs := map[string]Activity{starved: quiet(40 * time.Hour)}
	var peerDirs []string
	for i := 0; i < 200; i++ {
		d := "/m/box" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "/new"
		dirs[d] = live(1, time.Duration(i+1)*time.Minute)
		peerDirs = append(peerDirs, d)
	}
	sample, _ := fixture(dirs, nil)
	peers := func(Consumer) []string { return peerDirs }

	rep := Evaluate([]Consumer{consumer("com.pogo.notify", "MAIL_DIR", starved)}, sample, peers, now, window)
	v := verdictFor(t, rep, "com.pogo.notify")
	if v.Status != StatusStarved {
		t.Fatalf("status = %q, want %q", v.Status, StatusStarved)
	}
	if v.PeerCount != 200 || v.LivePeerCount != 200 {
		t.Errorf("PeerCount/LivePeerCount = %d/%d, want 200/200 — the counts are the fact, and must not be capped", v.PeerCount, v.LivePeerCount)
	}
	if len(v.LivePeers) != maxNamedPeers {
		t.Errorf("named %d peers, want at most %d", len(v.LivePeers), maxNamedPeers)
	}
	if !strings.Contains(v.Detail, "and 197 more") {
		t.Errorf("detail = %q, want it to say how much evidence it did not print", v.Detail)
	}
	// Named most-recent-first, so the boxes written while this one was not are
	// the ones an operator can check by hand.
	for i := 1; i < len(v.LivePeers); i++ {
		if v.LivePeers[i].Last.After(v.LivePeers[i-1].Last) {
			t.Errorf("named peers are not most-recent-first: %+v", v.LivePeers)
		}
	}
	if len(v.Detail) > 1000 {
		t.Errorf("detail is %d bytes; a doctor row must stay readable", len(v.Detail))
	}
}

// TestEvaluateNeverReportsFindingsForAnEmptyPopulation. Zero consumers is not
// zero problems; it is nothing examined. The renderer is what turns that into
// NOT CHECKED, and it can only do so if the report does not pretend otherwise.
func TestEvaluateNeverReportsFindingsForAnEmptyPopulation(t *testing.T) {
	rep := Evaluate(nil, func(string) Activity { return Activity{} }, func(Consumer) []string { return nil }, now, window)
	if rep.Scanned != 0 || len(rep.Verdicts) != 0 {
		t.Fatalf("rep = %+v, want an empty sweep", rep)
	}
	if len(rep.Findings()) != 0 {
		t.Errorf("Findings() = %+v on an empty sweep", rep.Findings())
	}
}
