package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/hostload"
)

// fakeLoadGate serves a known host. The two states this change is about — a
// host the fleet has filled and a host it has not — are neither of them states
// a test can create on the machine it runs on.
type fakeLoadGate struct {
	sample hostload.Sample
	ok     bool
	calls  int
}

func (f *fakeLoadGate) DispatchLoad() (hostload.Sample, bool) {
	f.calls++
	return f.sample, f.ok
}

// oneHeavyWorker is the live instance of mg-1b8c, measured 2026-07-30: ONE
// polecat self-parallelised into three compute processes, holding ~5.7 of 10
// cores, with other fleet processes making ~6.2 in total.
func oneHeavyWorker() hostload.Sample {
	return hostload.Sample{
		Cores: 10, FleetCores: 6.2, ExternalCores: 1.3, FleetProcs: 9,
		LoadAvg1: 75, Attributed: true, Window: time.Second,
	}
}

// fiveIdleWorkers is the ordinary case: the shipped concurrency cap fully
// spent, and the host barely touched. Nothing here may add latency.
func fiveIdleWorkers() hostload.Sample {
	return hostload.Sample{
		Cores: 10, FleetCores: 0.4, ExternalCores: 1.2, FleetProcs: 14,
		LoadAvg1: 3.5, Attributed: true, Window: time.Second,
	}
}

// TestSpawnRefusedWhenTheFleetHasFilledTheHost is the arm that changes the
// outcome. The failure this closes: two compute-heavy workers dispatched into
// the same window because the only thing dispatch looked at was whether a slot
// was free. A slot was free here too — five workers are running and the cap is
// five — and the host is not.
func TestSpawnRefusedWhenTheFleetHasFilledTheHost(t *testing.T) {
	reg := newDrainTestRegistry(t)
	gate := &fakeLoadGate{sample: oneHeavyWorker(), ok: true}
	reg.SetLoadGate(gate)

	rr := spawnPolecatFor(t, reg, "mg-heavy")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — the load gate did not refuse a full host", rr.Code)
	}
	body := rr.Body.String()
	// The refusal has to be arguable: numbers, not an adjective.
	for _, want := range []string{"6.2 of 10 cores", "9 processes", "HOLD"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal must carry the measurement, missing %q; got: %s", want, body)
		}
	}
	// And it has to say it is a "later". A caller that reads this as "this item
	// cannot be dispatched" abandons work that is perfectly fine.
	for _, want := range []string{"LATER", "re-check", "nothing about the work item is wrong"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal must read as retryable, missing %q; got: %s", want, body)
		}
	}
	// Nothing left behind — the gate sits above every side effect.
	if a := reg.Get("cat-gate"); a != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
}

// TestSpawnNotDelayedWhenTheHostIsFree is the latency arm the acceptance asks
// for explicitly. Fourteen fleet processes across five workers, and 0.4 cores
// between them: the guard must be invisible here, or it has replaced one bad
// dispatch rule with another.
func TestSpawnNotDelayedWhenTheHostIsFree(t *testing.T) {
	reg := newDrainTestRegistry(t)
	gate := &fakeLoadGate{sample: fiveIdleWorkers(), ok: true}
	reg.SetLoadGate(gate)

	start := time.Now()
	rr := spawnPolecatFor(t, reg, "mg-ordinary")
	elapsed := time.Since(start)

	if rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("the load gate refused an idle host: %s", rr.Body.String())
	}
	if gate.calls != 1 {
		t.Errorf("the gate was consulted %d times, want exactly 1 per spawn", gate.calls)
	}
	// The gate adds one sample and nothing else. It cannot be a source of
	// dispatch latency when there is nothing to wait for.
	if elapsed > 2*time.Second {
		t.Errorf("spawn on an idle host took %s; the load gate must not add latency", elapsed)
	}
}

// TestManyWorkersDoNotTripTheGate is the same claim stated the other way, and
// it is the whole point of counting the resource. The shipped rule is "3-5
// concurrent workers"; a host with fourteen fleet processes holding almost no
// CPU is not a host under pressure, and treating it as one would starve the
// queue exactly as counting workers does.
func TestManyWorkersDoNotTripTheGate(t *testing.T) {
	crowded := fiveIdleWorkers()
	crowded.FleetProcs = 60
	reg := newDrainTestRegistry(t)
	reg.SetLoadGate(&fakeLoadGate{sample: crowded, ok: true})

	if rr := spawnPolecatFor(t, reg, "mg-crowded"); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("60 idle fleet processes tripped a guard that is supposed to measure CPU: %s",
			rr.Body.String())
	}
}

// TestExternalLoadDoesNotVetoDispatch holds the measurement that disqualified
// the obvious implementation. On the measured host a VPN extension held ~0.9
// cores and the system indexer ~0.3, none of it the fleet's. A guard keyed on
// total host CPU lets an unrelated process stop the fleet working, and pausing
// the fleet would not give those cores back.
func TestExternalLoadDoesNotVetoDispatch(t *testing.T) {
	notOurs := hostload.Sample{
		Cores: 10, FleetCores: 0.6, ExternalCores: 8.9, FleetProcs: 5,
		LoadAvg1: 214, Attributed: true, Window: time.Second,
	}
	reg := newDrainTestRegistry(t)
	reg.SetLoadGate(&fakeLoadGate{sample: notOurs, ok: true})

	if rr := spawnPolecatFor(t, reg, "mg-external"); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("dispatch was vetoed by load that is not the fleet's: %s", rr.Body.String())
	}
}

// TestGateFailsOpenOnAnUnreadableHost: refusing work on missing information
// stalls the queue for a reason nobody downstream can check or clear.
func TestGateFailsOpenOnAnUnreadableHost(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetLoadGate(&fakeLoadGate{ok: false})

	if rr := spawnPolecatFor(t, reg, "mg-unreadable"); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("a failed measurement refused dispatch: %s", rr.Body.String())
	}
}

// TestGateFailsOpenWithoutAttribution: an unattributable sample is missing
// information too, and must not read as a busy fleet.
func TestGateFailsOpenWithoutAttribution(t *testing.T) {
	unattributed := hostload.Sample{Cores: 10, ExternalCores: 9.8, Attributed: false}
	reg := newDrainTestRegistry(t)
	reg.SetLoadGate(&fakeLoadGate{sample: unattributed, ok: true})

	if rr := spawnPolecatFor(t, reg, "mg-unattributed"); rr.Code == http.StatusServiceUnavailable {
		t.Errorf("an unattributable sample refused dispatch: %s", rr.Body.String())
	}
}

// TestHostLoadEndpointAgreesWithTheGate. An advisory number that could drift
// from the enforced one would let a coordinator plan against a host pogod sees
// differently, which is the same class of defect as the prose-vs-code split
// the dispatch gates next door were built to end.
func TestHostLoadEndpointAgreesWithTheGate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sample     hostload.Sample
		ok         bool
		wantRefuse bool
	}{
		{"fleet has filled the host", oneHeavyWorker(), true, true},
		{"host is free", fiveIdleWorkers(), true, false},
		{"load is not ours", hostload.Sample{Cores: 10, FleetCores: 0.6, ExternalCores: 8.9, Attributed: true}, true, false},
		{"unmeasurable", hostload.Sample{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetLoadGate(&fakeLoadGate{sample: tc.sample, ok: tc.ok})

			rr := httptest.NewRecorder()
			reg.handleHostLoad(rr, httptest.NewRequest("GET", "/agents/hostload", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			var resp HostLoadResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if resp.WouldRefuseDispatch != tc.wantRefuse {
				t.Errorf("would_refuse_dispatch = %v, want %v", resp.WouldRefuseDispatch, tc.wantRefuse)
			}
			if resp.Measured != tc.ok {
				t.Errorf("measured = %v, want %v", resp.Measured, tc.ok)
			}
			if resp.Advice == "" {
				t.Error("the endpoint must always render advice, not just numbers")
			}

			// Now put the same sample through the spawn path and require the
			// same answer.
			reg2 := newDrainTestRegistry(t)
			reg2.SetLoadGate(&fakeLoadGate{sample: tc.sample, ok: tc.ok})
			gotRefuse := spawnPolecatFor(t, reg2, "mg-agree").Code == http.StatusServiceUnavailable
			if gotRefuse != tc.wantRefuse {
				t.Errorf("spawn refused = %v but endpoint said %v — the advisory and enforced "+
					"answers disagree", gotRefuse, tc.wantRefuse)
			}
		})
	}
}

func TestHostLoadEndpointRejectsNonGET(t *testing.T) {
	reg := newDrainTestRegistry(t)
	rr := httptest.NewRecorder()
	reg.handleHostLoad(rr, httptest.NewRequest("POST", "/agents/hostload", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestDefaultLoadGateMeasuresThisHost: the gate holds without any wiring. A
// registry that nobody called SetLoadGate on must still take a real sample,
// or the control is one forgotten call away from being absent.
func TestDefaultLoadGateMeasuresThisHost(t *testing.T) {
	reg := newDrainTestRegistry(t)
	s, ok := reg.getLoadGate().DispatchLoad()
	if !ok {
		t.Fatal("the default load gate could not sample this host")
	}
	if s.Cores <= 0 {
		t.Errorf("Cores = %d", s.Cores)
	}
	if !s.Attributed {
		t.Error("the default gate must attribute to this process's own subtree")
	}
}

func TestHostLoadGateHonoursItsTimeout(t *testing.T) {
	g := HostLoadGate{
		Reader:  &hostload.Reader{Roots: []int{1}, Window: time.Minute},
		Timeout: 50 * time.Millisecond,
	}
	start := time.Now()
	if _, ok := g.DispatchLoad(); ok {
		t.Error("a sample that could not finish inside the timeout reported ok")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("DispatchLoad blocked for %s past its 50ms timeout", d)
	}
}

func TestFixedSamplerErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	if _, err := (hostload.FixedSampler{Err: want}).Read(nil); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

// TestLoadGateAndWorkerBudgetReadOneConfiguredValue pins the requirement of
// drellem2/pogo#202 that the gate's mark and the worker budget's share are ONE
// value: before the key existed both read hostload.FleetHeavyAt, and a key that
// moved only the gate would have left the budget quietly on the old number.
func TestLoadGateAndWorkerBudgetReadOneConfiguredValue(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DispatchCapConfig{}) // no cap: the budget follows the gate's mark
	reg.SetDispatchLoadGate(config.DispatchLoadGateConfig{FleetShare: 0.3})
	// 40% fleet share: under the shipped 50% mark, over the configured 30%.
	reg.SetLoadGate(&fakeLoadGate{sample: hostload.Sample{
		Cores: 10, FleetCores: 4, FleetProcs: 5, Attributed: true, Window: time.Second,
	}, ok: true})

	rr := spawnPolecatFor(t, reg, "mg-mark")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — the configured 30%% mark was not enforced", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "load_gate_fleet_share") || !strings.Contains(body, "30% mark") {
		t.Errorf("refusal must name the key and the mark it acted on; got: %s", body)
	}

	rec := httptest.NewRecorder()
	reg.handleHostLoad(rec, httptest.NewRequest("GET", "/agents/hostload", nil))
	var resp HostLoadResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.WouldRefuseDispatch || resp.LoadGateFleetShare != 0.3 || resp.LoadGateDisarmed {
		t.Errorf("endpoint disagrees with the gate: %+v", resp)
	}
	b := WorkerBudgetFor(10, config.DispatchCapConfig{}, reg.loadGatePolicy())
	if want := int(10 * resp.LoadGateFleetShare); b.Cores != want {
		t.Errorf("budget = %d cores, want %d — the budget and the gate read different marks (basis: %s)",
			b.Cores, want, b.Basis)
	}
	if got := reg.WorkerBudget(); got.HostCores > 0 && got.Cores != int(float64(got.HostCores)*0.3) && got.Cores != 1 {
		t.Errorf("Registry.WorkerBudget() = %+v, not derived from the configured mark", got)
	}
}

// TestDisarmedLoadGateRefusesNothingAndSaysSo: `load_gate_fleet_share = 0`
// dispatches onto a full host, never consults the sampler, and the endpoint
// reports the disarm rather than an ordinary PROCEED (drellem2/pogo#202).
func TestDisarmedLoadGateRefusesNothingAndSaysSo(t *testing.T) {
	full := hostload.Sample{Cores: 10, FleetCores: 9.8, FleetProcs: 40, Attributed: true, Window: time.Second}

	reg := newDrainTestRegistry(t)
	reg.SetDispatchLoadGate(config.DispatchLoadGateConfig{FleetShare: 0})
	gate := &fakeLoadGate{sample: full, ok: true}
	reg.SetLoadGate(gate)
	if rr := spawnPolecatFor(t, reg, "mg-disarmed"); rr.Code == http.StatusServiceUnavailable {
		t.Fatalf("a disarmed load gate refused: %s", rr.Body.String())
	}
	if gate.calls != 0 {
		t.Errorf("a disarmed gate sampled the host %d time(s) on the spawn path", gate.calls)
	}

	rec := httptest.NewRecorder()
	reg.handleHostLoad(rec, httptest.NewRequest("GET", "/agents/hostload", nil))
	var resp HostLoadResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.LoadGateDisarmed || resp.WouldRefuseDispatch || resp.FleetHeavy {
		t.Errorf("endpoint for a disarmed gate: %+v", resp)
	}
	if !strings.Contains(resp.Advice, "DISARMED by config") {
		t.Errorf("advice does not say the gate is disarmed: %q", resp.Advice)
	}

	// Positive control: the SAME sample on a registry nobody configured is
	// refused — so the pass above is the disarm, not a sample the gate ignores.
	ctl := newDrainTestRegistry(t)
	ctl.SetLoadGate(&fakeLoadGate{sample: full, ok: true})
	if rr := spawnPolecatFor(t, ctl, "mg-control"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("control: the default gate did not refuse a full host (status %d)", rr.Code)
	}

	// The budget falls back to the shipped mark as advice — not to the whole
	// host, and not to a zero share floored to one core.
	b := WorkerBudgetFor(10, config.DispatchCapConfig{}, config.DispatchLoadGateConfig{})
	if b.Cores != int(10*hostload.FleetHeavyAt) || !strings.Contains(b.Basis, "DISARMED") {
		t.Errorf("budget under a disarmed gate and no cap = %+v", b)
	}
}
