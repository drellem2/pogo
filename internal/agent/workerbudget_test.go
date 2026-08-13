package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// TestWorkerBudgetDividesTheBoxByTheEnforcedCap is the shape of the fix: on the
// host the incident happened on, one worker is told it may have a THIRD of the
// box rather than all of it.
//
// The 10/3 row is the measured case (mg-eb47): three polecats were live, the
// per-repo cap read one-of-three, and one of them held 9.0 of 10 cores. Under
// this budget that worker is told 3, which leaves the seven cores the fourteen
// undispatchable items needed.
func TestWorkerBudgetDividesTheBoxByTheEnforcedCap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hostCores int
		cap       config.DispatchCapConfig
		want      int
		wantBasis string
	}{
		{
			name:      "the fleet's host under the shipped cap",
			hostCores: 10,
			cap:       config.DefaultDispatchCapConfig(),
			want:      3,
			wantBasis: "per-repo dispatch cap",
		},
		{
			// A cap of 1 means one worker per repo, so that worker may have the
			// whole box: there is nothing for it to contend with.
			name:      "a cap of one hands over the whole host",
			hostCores: 10,
			cap:       config.DispatchCapConfig{MaxPolecatsPerRepo: 1},
			want:      10,
		},
		{
			// Integer division floors, and the floor is 1 rather than 0: a
			// budget of zero is an instruction to do nothing, not a share.
			name:      "more workers than cores floors at one core",
			hostCores: 2,
			cap:       config.DispatchCapConfig{MaxPolecatsPerRepo: 8},
			want:      1,
			wantBasis: "floored at 1 core",
		},
		{
			// With no count enforced, hostload.FleetHeavyAt (0.50) is the only
			// remaining control, so the share comes off that threshold. It buys
			// "at most half the box", not "the gate will not fire" — the fleet
			// also contains pogod and every other agent.
			name:      "a disarmed cap still bounds one worker at half the host",
			hostCores: 10,
			cap:       config.DispatchCapConfig{},
			want:      5,
			wantBasis: "no per-repo worker cap is armed",
		},
		{
			// The reserve withholds a dispatch SLOT; it does not shrink what a
			// running worker may use, and subtracting it would shrink every
			// worker's share for a slot nobody occupies.
			name:      "the refinery reserve does not shrink the share",
			hostCores: 10,
			cap:       config.DispatchCapConfig{MaxPolecatsPerRepo: 3, RefineryReserve: 1},
			want:      3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := WorkerBudgetFor(tc.hostCores, tc.cap)
			if b.Cores != tc.want {
				t.Errorf("Cores = %d, want %d (basis: %s)", b.Cores, tc.want, b.Basis)
			}
			if b.HostCores != tc.hostCores {
				t.Errorf("HostCores = %d, want %d", b.HostCores, tc.hostCores)
			}
			if !b.Known() {
				t.Errorf("Known() = false for a derived budget: %+v", b)
			}
			if tc.wantBasis != "" && !strings.Contains(b.Basis, tc.wantBasis) {
				t.Errorf("Basis = %q, want it to contain %q", b.Basis, tc.wantBasis)
			}
			if b.Basis == "" {
				t.Error("Basis is empty: a number a reader cannot argue with is a number they cannot check")
			}
		})
	}
}

// TestWorkerBudgetEnvIsAbsentRatherThanZero. An unset variable says "nobody told
// me" and a zero one says "use no cores". They are different instructions and a
// worker acting on the second would do nothing, so a budget that could not be
// derived must emit no assignment at all.
func TestWorkerBudgetEnvIsAbsentRatherThanZero(t *testing.T) {
	// hostCores <= 0 falls back to runtime.NumCPU(), which is always >= 1 on a
	// machine running this test — so the undermined case is constructed
	// directly rather than by passing 0.
	unknown := WorkerBudget{Basis: "host core count unknown — no budget could be derived"}
	if unknown.Known() {
		t.Fatal("a budget with no cores reported itself as known")
	}
	if env := unknown.Env(); env != nil {
		t.Errorf("Env() = %v for an underived budget, want nil", env)
	}
	if s := unknown.String(); !strings.Contains(s, "NOT DERIVED") {
		t.Errorf("String() = %q, want it to name the underived state", s)
	}

	b := WorkerBudgetFor(10, config.DefaultDispatchCapConfig())
	env := b.Env()
	want := []string{"POGO_WORKER_CORES=3", "POGO_HOST_CORES=10"}
	if len(env) != len(want) {
		t.Fatalf("Env() = %v, want %v", env, want)
	}
	for i, w := range want {
		if env[i] != w {
			t.Errorf("Env()[%d] = %q, want %q", i, env[i], w)
		}
	}
}

// TestWorkerBudgetFallsBackToThisHostsCoreCount. A caller that does not know the
// core count gets this host's rather than a zero budget, because the daemon
// deciding the share and the host it is dividing are the same machine.
func TestWorkerBudgetFallsBackToThisHostsCoreCount(t *testing.T) {
	b := WorkerBudgetFor(0, config.DefaultDispatchCapConfig())
	if !b.Known() {
		t.Fatalf("no budget derived from a zero core count: %+v", b)
	}
	if b.HostCores < 1 || b.Cores < 1 {
		t.Errorf("nonsensical budget from the fallback: %+v", b)
	}
}

// TestPolecatSpawnEnvPrecedence pins both directions of the ordering. A
// dispatcher that has measured an item's cost must be able to override the
// static division — a control with no override is a wedge — and must NOT be
// able to move POGO_ROLE, which is a frozen cross-tool identifier.
func TestPolecatSpawnEnvPrecedence(t *testing.T) {
	budget := WorkerBudgetFor(10, config.DefaultDispatchCapConfig())

	plain := polecatSpawnEnv(budget, nil)
	if got := indexOfAssignment(plain, "POGO_WORKER_CORES"); got < 0 {
		t.Fatalf("no budget in %v", plain)
	}
	if plain[len(plain)-1] != "POGO_ROLE=polecat" {
		t.Errorf("last entry = %q, want POGO_ROLE=polecat", plain[len(plain)-1])
	}

	// exec.Cmd keeps the LAST assignment of a duplicated key, so "wins" here
	// means "appears after".
	overridden := polecatSpawnEnv(budget, []string{"POGO_WORKER_CORES=8", "POGO_ROLE=impostor"})
	budgetAt, overrideAt := indexOfAssignment(overridden, "POGO_WORKER_CORES"), lastIndexOf(overridden, "POGO_WORKER_CORES=8")
	if overrideAt <= budgetAt {
		t.Errorf("the dispatcher's POGO_WORKER_CORES does not win: %v", overridden)
	}
	if overridden[len(overridden)-1] != "POGO_ROLE=polecat" {
		t.Errorf("a dispatcher moved POGO_ROLE: %v", overridden)
	}
}

func indexOfAssignment(env []string, key string) int {
	for i, e := range env {
		if strings.HasPrefix(e, key+"=") {
			return i
		}
	}
	return -1
}

func lastIndexOf(env []string, want string) int {
	for i := len(env) - 1; i >= 0; i-- {
		if env[i] == want {
			return i
		}
	}
	return -1
}

// TestRegistryWorkerBudgetTracksTheInstalledCap is the drift guard the
// WouldRefuseDispatch precedent exists for: what /agents/hostload reports and
// what the spawn path injects must come from one derivation. A registry whose
// cap is reconfigured must report the new share, not a cached one.
func TestRegistryWorkerBudgetTracksTheInstalledCap(t *testing.T) {
	reg := newDrainTestRegistry(t)

	reg.SetDispatchCap(config.DispatchCapConfig{MaxPolecatsPerRepo: 1})
	whole := reg.WorkerBudget()
	if whole.Cores != whole.HostCores {
		t.Errorf("cap of 1: Cores = %d, want the whole host (%d)", whole.Cores, whole.HostCores)
	}

	reg.SetDispatchCap(config.DispatchCapConfig{MaxPolecatsPerRepo: 2})
	half := reg.WorkerBudget()
	if want := whole.HostCores / 2; half.Cores != want && whole.HostCores > 1 {
		t.Errorf("cap of 2: Cores = %d, want %d", half.Cores, want)
	}
	if half.HostCores != whole.HostCores {
		t.Errorf("the host changed size between reads: %d then %d", whole.HostCores, half.HostCores)
	}
}

// TestHostLoadEndpointServesTheInjectedBudget. The endpoint exists so a
// coordinator can read the resource rather than count workers, and the budget is
// only useful there if it is the SAME number the spawn path hands out. This is
// the WouldRefuseDispatch drift guard applied to the new field: an advisory
// figure that could differ from the injected one lets a coordinator plan against
// a fleet pogod is configuring differently.
func TestHostLoadEndpointServesTheInjectedBudget(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DispatchCapConfig{MaxPolecatsPerRepo: 2})
	// A sampler that cannot measure, so the assertion is about the budget alone
	// and never about what this machine happens to be doing.
	reg.SetLoadGate(&fakeLoadGate{ok: false})

	rr := httptest.NewRecorder()
	reg.handleHostLoad(rr, httptest.NewRequest("GET", "/agents/hostload", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp HostLoadResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	want := reg.WorkerBudget()
	if resp.WorkerBudget != want {
		t.Errorf("endpoint served %+v, the spawn path would inject %+v", resp.WorkerBudget, want)
	}
	// It is a policy division, not a measurement — so it answers even when
	// nothing could be sampled. An unmeasurable host is exactly when a
	// coordinator most needs to know how much one worker plans to take.
	if resp.Measured {
		t.Fatal("test setup: the sample was supposed to fail")
	}
	if !resp.WorkerBudget.Known() {
		t.Errorf("the budget went unknown because the SAMPLE failed: %+v", resp.WorkerBudget)
	}
}
