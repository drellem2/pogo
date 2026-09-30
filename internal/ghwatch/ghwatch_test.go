package ghwatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/carrierdrift"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
)

// The precondition table, enumerated (moved from pogod's intakearming_test.go
// with the decision itself).
func TestDecideArming(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		enabled, ghOnPath, needsCred, credOK bool
		want                                 Arming
	}{
		{"disabled wins over everything", false, false, true, false, Disabled},
		{"gh and a credential", true, true, true, true, Armed},
		{"gh but no credential", true, true, true, false, NoCredential},
		{"no gh, credential somehow ok", true, false, true, true, NoGHBinary},
		{"neither", true, false, true, false, NoGHBinary},
		{"teardown needs no credential", true, true, false, false, Armed},
		{"teardown still needs gh", true, false, false, true, NoGHBinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideArming(tc.enabled, tc.ghOnPath, tc.needsCred, tc.credOK); got != tc.want {
				t.Errorf("DecideArming = %q, want %q", got, tc.want)
			}
		})
	}
}

type mailRec struct {
	mu   sync.Mutex
	sent []string // "to|subject"
}

func (m *mailRec) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, to+"|"+subject)
	return nil
}

func (m *mailRec) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type eventRec struct {
	mu  sync.Mutex
	evs []events.Event
}

func (e *eventRec) emit(ev events.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

func teardownOnly() *config.Config {
	cfg := &config.Config{}
	cfg.GHTeardown.Enabled = true
	return cfg
}

// deps with one done carrier (mg-miss, issue #1) whose issue is OPEN — a
// teardown miss — and one (mg-closed, issue #2) whose issue is CLOSED.
func teardownDeps(m *mailRec, e *eventRec) Deps {
	return Deps{
		CredentialOK: true, Credential: "GH_TOKEN: present (source=ambient)",
		Mail: m.send, Emit: e.emit,
		TeardownSource: func() ([]ghteardown.Carrier, error) {
			return []ghteardown.Carrier{
				{ID: "mg-miss", Status: "done", Repo: "o/r", Number: 1},
				{ID: "mg-closed", Status: "done", Repo: "o/r", Number: 2},
			}, nil
		},
		TeardownLookup: func(repo string, n int) (ghteardown.IssueState, error) {
			if n == 2 {
				return ghteardown.StateClosed, nil
			}
			return ghteardown.StateOpen, nil
		},
	}
}

// The point of persisting the watcher's memory: a per-fire process must not
// turn every fire into the watcher's first. Run 1 mails the miss; a fire inside
// the interval does not sample; a fire after the interval samples the same set
// and stays quiet until renotify_after. The control — the same third fire with
// NO carried state — mails, so the quiet is the persistence and not a broken
// mail path.
func TestRunCarriesWatcherMemoryAcrossRuns(t *testing.T) {
	cfg := teardownOnly()
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	m, e := &mailRec{}, &eventRec{}
	f1 := Run(cfg, File{}, teardownDeps(m, e), t0, false)
	if !f1.Teardown.SampledThisRun || m.count() != 1 {
		t.Fatalf("first run: sampled=%t mails=%d, want sampled and one mail", f1.Teardown.SampledThisRun, m.count())
	}

	f2 := Run(cfg, f1, teardownDeps(m, e), t0.Add(10*time.Minute), false)
	if f2.Teardown.SampledThisRun {
		t.Error("a fire inside the 1h interval sampled — the throttle did not survive the process boundary")
	}
	if !f2.Teardown.LastSampledAt.Equal(t0) {
		t.Errorf("a throttled run lost the last sample's time: %v", f2.Teardown.LastSampledAt)
	}

	f3 := Run(cfg, f2, teardownDeps(m, e), t0.Add(2*time.Hour), false)
	if !f3.Teardown.SampledThisRun {
		t.Fatal("a fire past the interval did not sample")
	}
	if m.count() != 1 {
		t.Errorf("an unchanged finding set was re-mailed inside renotify_after (%d mails) — the fingerprint did not persist", m.count())
	}

	// Control: the same fire with no memory mails.
	ctl := &mailRec{}
	Run(cfg, File{}, teardownDeps(ctl, e), t0.Add(2*time.Hour), false)
	if ctl.count() != 1 {
		t.Fatalf("control: a fresh watcher did not mail the miss (%d) — the quiet above proves nothing", ctl.count())
	}

	// The escalation clock is carried too.
	if got := f3.Teardown.Watcher.FirstSeen["teardown_miss|mg-miss"]; !got.Equal(t0) {
		t.Errorf("first-seen for the miss = %v, want %v (keys %v)", got, t0, f3.Teardown.Watcher.FirstSeen)
	}
}

// --force clears the throttle only.
func TestForceSamplesWithoutReMailing(t *testing.T) {
	cfg := teardownOnly()
	t0 := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	m, e := &mailRec{}, &eventRec{}
	f1 := Run(cfg, File{}, teardownDeps(m, e), t0, false)
	f2 := Run(cfg, f1, teardownDeps(m, e), t0.Add(time.Minute), true)
	if !f2.Teardown.SampledThisRun {
		t.Fatal("--force did not sample inside the interval")
	}
	if m.count() != 1 {
		t.Errorf("--force re-mailed an unchanged set (%d mails)", m.count())
	}
}

// The positive control the ticket asks for: a carrier whose issue is known
// closed reads "closed" in the record, named by carrier — so "no findings" can
// be told apart from "looked at nothing".
func TestRecordNamesEveryIssueStateRead(t *testing.T) {
	m, e := &mailRec{}, &eventRec{}
	f := Run(teardownOnly(), File{}, teardownDeps(m, e), time.Now(), false)
	got := map[string]string{}
	for _, l := range f.Teardown.Lookups {
		got[l.Carrier+" "+l.Issue] = l.State
	}
	if got["mg-closed o/r#2"] != "closed" || got["mg-miss o/r#1"] != "open" {
		t.Errorf("lookups = %+v, want mg-closed closed and mg-miss open", f.Teardown.Lookups)
	}
	if f.Teardown.LastEvent != "gh_teardown_watch_fired" {
		t.Errorf("LastEvent = %q", f.Teardown.LastEvent)
	}
	for _, ev := range e.evs {
		if ev.Agent != Agent {
			t.Errorf("event %s carries agent %q, want %q — the log must say which process sampled", ev.EventType, ev.Agent, Agent)
		}
	}
}

// A clean teardown sample emits no event; the record must still say it ran.
func TestCleanTeardownSampleIsRecordedAsClean(t *testing.T) {
	m, e := &mailRec{}, &eventRec{}
	d := teardownDeps(m, e)
	d.TeardownLookup = func(string, int) (ghteardown.IssueState, error) { return ghteardown.StateClosed, nil }
	f := Run(teardownOnly(), File{}, d, time.Now(), false)
	if !f.Teardown.SampledThisRun || f.Teardown.LastEvent != "clean (no event)" || len(f.Teardown.Lookups) != 2 {
		t.Errorf("clean sample recorded as %+v", f.Teardown)
	}
}

func allEnabled() *config.Config {
	cfg := &config.Config{}
	cfg.GHTeardown.Enabled = true
	cfg.GHIntake.Enabled = true
	cfg.CarrierDrift.Enabled = true
	return cfg
}

func failingSources(d Deps) Deps {
	d.IntakeSource = func() (ghintake.Inventory, error) { return ghintake.Inventory{}, errors.New("store") }
	d.DriftSource = func() ([]carrierdrift.Carrier, int, error) { return nil, 0, errors.New("store") }
	d.DriftSnapshot = func(string, int) (carrierdrift.Snapshot, error) { return carrierdrift.Snapshot{}, nil }
	return d
}

// Without gh nothing arms, nothing samples, and the previous record's last
// sample is kept rather than wiped by one bad fire.
func TestNoGHArmsNothingAndKeepsTheLastSample(t *testing.T) {
	m, e := &mailRec{}, &eventRec{}
	t0 := time.Now()
	f1 := Run(teardownOnly(), File{}, teardownDeps(m, e), t0, false)

	d := failingSources(teardownDeps(m, e))
	d.GHPathErr = errors.New(`exec: "gh": executable file not found in $PATH`)
	f2 := Run(allEnabled(), f1, d, t0.Add(2*time.Hour), false)
	for _, nd := range f2.Detectors() {
		if nd.Arming != NoGHBinary || nd.SampledThisRun || !strings.Contains(nd.ArmingDetail, "not found") {
			t.Errorf("%s: arming=%s sampled=%t detail=%q", nd.Name, nd.Arming, nd.SampledThisRun, nd.ArmingDetail)
		}
	}
	if len(f2.Teardown.Lookups) != 2 || !f2.Teardown.LastSampledAt.Equal(f1.Teardown.LastSampledAt) {
		t.Error("an un-armed run discarded the last real sample")
	}
}

func TestNoCredentialDisarmsOnlyTheCredentialedDetectors(t *testing.T) {
	m, e := &mailRec{}, &eventRec{}
	d := failingSources(teardownDeps(m, e))
	d.CredentialOK = false
	d.Credential = "GH_TOKEN: ABSENT (source=none)"
	f := Run(allEnabled(), File{}, d, time.Now(), false)
	if f.Teardown.Arming != Armed {
		t.Errorf("teardown arming = %s; it never needed the credential", f.Teardown.Arming)
	}
	for _, nd := range []NamedDetector{{"intake", f.Intake}, {"carrier_drift", f.CarrierDrift}} {
		if nd.Arming != NoCredential || nd.ArmingDetail != d.Credential {
			t.Errorf("%s: arming=%s detail=%q", nd.Name, nd.Arming, nd.ArmingDetail)
		}
	}
}

func TestDisabledDetectorsDoNotRun(t *testing.T) {
	m, e := &mailRec{}, &eventRec{}
	f := Run(&config.Config{}, File{}, teardownDeps(m, e), time.Now(), false)
	for _, nd := range f.Detectors() {
		if nd.Arming != Disabled || nd.SampledThisRun {
			t.Errorf("%s: arming=%s sampled=%t", nd.Name, nd.Arming, nd.SampledThisRun)
		}
	}
	if m.count() != 0 {
		t.Error("a disabled detector mailed")
	}
}

func TestStateFileRoundTripAndRefusals(t *testing.T) {
	home := t.TempDir()
	if _, err := Read(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read with no file = %v, want ErrNotExist — a job that never ran is not a clean record", err)
	}
	m, e := &mailRec{}, &eventRec{}
	f := Run(teardownOnly(), File{}, teardownDeps(m, e), time.Now(), false)
	if err := Write(home, f); err != nil {
		t.Fatal(err)
	}
	got, err := Read(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Teardown.Watcher.LastPrint != f.Teardown.Watcher.LastPrint || len(got.Teardown.Lookups) != 2 {
		t.Errorf("round trip lost state: %+v", got.Teardown)
	}

	if err := os.WriteFile(StatePath(home), []byte(`{"schema_version": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(home); err == nil {
		t.Error("Read accepted a schema version it does not know")
	}
}

func TestLockExcludesASecondRun(t *testing.T) {
	home := t.TempDir()
	unlock, err := Lock(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(home); !errors.Is(err, ErrLocked) {
		t.Errorf("second Lock = %v, want ErrLocked", err)
	}
	unlock()
	again, err := Lock(home)
	if err != nil {
		t.Fatalf("Lock after release = %v", err)
	}
	again()
}

// mgStub writes an `mg` that logs its argv and answers list/show from cases.
func mgStub(t *testing.T, cases string) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "mg")
	stub := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\ncase \"$*\" in\n" + cases + "\nesac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logPath
}

func showsSince(t *testing.T, logPath string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(" "+line+" ", " show ") {
			n++
		}
	}
	return n
}

// The mg-scan caches must survive the PROCESS boundary, not just a pass: each
// "run" below reads the caches from disk afresh, as `pogo gh-watch` does, and
// an unchanged store must be answered without a single `mg show`. Without the
// persistence every 15-minute fire forks once per item in the store
// (drellem2/pogo#179). Moved from pogod's intakecarriers_test.go and
// carrierdriftsource_test.go, which pinned the in-process half of the same
// property.
func TestCarrierSourcesUseThePersistedCaches(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	cfg.CarrierDrift.IncludeShelved = true

	inBin, inLog := mgStub(t, `  *"list --status=archived"*) echo '{"id":"mg-a","status":"archived","mtime":"2026-01-01T00:00:00Z"}'
                              echo '{"id":"mg-b","status":"archived","mtime":"2026-01-02T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) echo '{"id":"mg-a","status":"archived","body":"gh: drellem2/pogo#1"}' ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"archived","body":"nothing"}' ;;`)
	cdBin, cdLog := mgStub(t, `  *"list --status=available"*) echo '{"id":"mg-a","status":"available","mtime":"2026-01-01T00:00:00Z"}' ;;
  *"list --status=shelved"*)   echo '{"id":"mg-b","status":"shelved","mtime":"2026-01-02T00:00:00Z"}' ;;
  *list*) ;;
  *"show mg-a"*) printf '%s\n' '{"id":"mg-a","status":"available","body":"workflow: gh-issue\ngh: drellem2/pogo#1"}' ;;
  *"show mg-b"*) echo '{"id":"mg-b","status":"shelved","body":"nothing"}' ;;`)
	store := filepath.Join(t.TempDir(), "store")

	for run, want := range []int{2, 0, 0} {
		caches := ReadCaches(home) // a fresh process's view
		in, cd := CarrierSources(cfg, caches)
		if in.Cache != caches.Intake || cd.Cache != caches.CarrierDrift {
			t.Fatal("CarrierSources did not scan through the persisted caches")
		}
		if !cd.IncludeShelved {
			t.Fatal("CarrierSources dropped [carrier_drift] include_shelved")
		}
		in.Bin, in.Root = inBin, store
		cd.Bin, cd.Root = cdBin, store

		refs, scanned, bad, err := in.Carriers()
		if err != nil || scanned != 2 || len(bad) != 0 || len(refs) != 1 {
			t.Fatalf("run %d intake: refs=%+v scanned=%d bad=%v err=%v", run+1, refs, scanned, bad, err)
		}
		cs, n, err := cd.Carriers()
		if err != nil || n != 2 || len(cs) != 1 {
			t.Fatalf("run %d re-read: carriers=%+v scanned=%d err=%v", run+1, cs, n, err)
		}
		if got := showsSince(t, inLog); got != want {
			t.Errorf("run %d intake forked %d `mg show`s, want %d", run+1, got, want)
		}
		if got := showsSince(t, cdLog); got != want {
			t.Errorf("run %d re-read forked %d `mg show`s, want %d", run+1, got, want)
		}
		if err := WriteCaches(home, caches); err != nil {
			t.Fatal(err)
		}
	}

	// Control: with the cache file gone, a fresh process forks again — so the
	// zeros above are the persistence, not a stub that never answers `show`.
	if err := os.Remove(filepath.Join(Dir(home), CacheFileName)); err != nil {
		t.Fatal(err)
	}
	in, _ := CarrierSources(cfg, ReadCaches(home))
	in.Bin, in.Root = inBin, store
	if _, _, _, err := in.Carriers(); err != nil {
		t.Fatal(err)
	}
	if got := showsSince(t, inLog); got != 2 {
		t.Errorf("control: a cold cache forked %d `mg show`s, want 2", got)
	}
}

// CarrierSources is only worth pinning if Production is what uses it, and
// scans the sources it returns. Production binds real `gh` and `mg`, so the
// wiring is asserted against the source — the technique pogod's main.go tests
// use for the same reason.
func TestProductionScansThroughCarrierSources(t *testing.T) {
	raw, err := os.ReadFile("production.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	i := strings.Index(body, "func Production(")
	j := strings.Index(body, "func CarrierSources(")
	if i < 0 || j < 0 {
		t.Fatal("production.go no longer has Production and CarrierSources")
	}
	prod := body[i:j]
	for _, want := range []string{
		"inSrc, cdSrc := CarrierSources(cfg, caches)",
		"inSrc.Carriers",
		"DriftSource:   cdSrc.Carriers",
		"ghintake.CredentialFor(",
		"ghintake.Reverify(",
	} {
		if !strings.Contains(prod, want) {
			t.Errorf("Production does not contain %q", want)
		}
	}
	if strings.Count(prod, "MGSource{") != 1 { // teardown's own, uncached by design
		t.Errorf("Production builds %d MGSource literals; intake and re-read must come from CarrierSources", strings.Count(prod, "MGSource{"))
	}
}
