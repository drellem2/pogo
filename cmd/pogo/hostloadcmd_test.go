package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestRepoOccupancyRendersEveryState. The reader of this command is deciding
// whether to dispatch, so silence reads as permission — every state the count
// can be in has to be said out loud, including the ones that are not refusals
// and the ones where the count may be wrong.
func TestRepoOccupancyRendersEveryState(t *testing.T) {
	for _, tc := range []struct {
		name string
		occ  *agent.RepoOccupancy
		// hostHeavy is the host gate's answer, which this renderer is told
		// rather than deriving: the two must not be able to disagree.
		hostHeavy bool
		want      []string
		deny      []string
	}{
		{
			name: "not asked renders nothing",
			occ:  nil,
			deny: []string{"Repo:", "Cap:"},
		},
		{
			name: "under the cap says so without refusing",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 1, Polecats: []string{"a-cat"},
				Cap: 3, ConfiguredCap: 3, RefineryKnown: true,
			},
			want: []string{"Repo:       /dev/pogo", "Cap:        3", "Workers:    1 — a-cat"},
			deny: []string{"would currently be refused"},
		},
		{
			name: "full repo names the refusal and its scope",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 3, Polecats: []string{"a-cat", "b-cat", "c-cat"},
				Cap: 3, ConfiguredCap: 3, RefineryKnown: true, WouldRefuse: true,
			},
			want: []string{"would currently be refused with 503", "not refused by THIS cap"},
			deny: []string{"rerouting will NOT help"},
		},
		{
			// The 2026-08-13 measurement (mg-eb47): a per-repo slot freed by a
			// merge was unusable because the host gate was refusing every spawn
			// regardless of repo. "Dispatch elsewhere" is the wrong advice in
			// that state and the renderer has to withdraw it, or a coordinator
			// spends its retries proving it.
			name: "a full repo on a full host does not offer rerouting",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 3, Polecats: []string{"a-cat", "b-cat", "c-cat"},
				Cap: 3, ConfiguredCap: 3, RefineryKnown: true, WouldRefuse: true,
			},
			hostHeavy: true,
			want: []string{
				"rerouting will NOT help",
				"regardless of repo",
				"not capacity",
				"not on\na worker exiting",
			},
		},
		{
			name: "reserved slot says the refinery is why",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 2, Polecats: []string{"a-cat", "b-cat"},
				Cap: 2, ConfiguredCap: 3, RefineryReserved: 1,
				RefineryHasWork: true, RefineryKnown: true, WouldRefuse: true,
			},
			want: []string{"Cap:        2 of 3 (1 reserved for the refinery"},
		},
		{
			name: "disarmed cap is not silence",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 9, Polecats: []string{"a-cat"},
				Cap: 0, ConfiguredCap: 0, RefineryKnown: true,
			},
			want: []string{"DISARMED", "refuses nothing"},
			deny: []string{"would currently be refused"},
		},
		{
			name: "an unasked refinery is not an idle one",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Cap: 3, ConfiguredCap: 3, RefineryKnown: false,
			},
			want: []string{"NOT ASKED", "not an idle refinery"},
		},
		{
			name: "a count that may be low says so",
			occ: &agent.RepoOccupancy{
				Repo: "/dev/pogo", Count: 1, Polecats: []string{"a-cat"},
				Cap: 3, ConfiguredCap: 3, RefineryKnown: true,
				WitnessErr:   "version 9999 is newer than this pogod",
				Unattributed: []string{"x-mystery"},
			},
			want: []string{"UNREADABLE", "may therefore be low", "Unattributed: 1", "x-mystery"},
		},
		{
			// mg-cd4a. `Workers: 0` under a cap of 3 is this command making the
			// mistake its own doc comment warns about: the caller is deciding
			// whether to dispatch, and a fabricated zero reads as permission
			// exactly as loudly as silence would. The count was never taken.
			name: "an occupancy that was never counted does not print a zero",
			occ: &agent.RepoOccupancy{
				Repo: "pogo", Cap: 3, ConfiguredCap: 3, RefineryKnown: true,
				Unresolvable: `"pogo" is a repository NAME, not a path, and it matches no single repository known to this host`,
			},
			want: []string{
				"NOT COUNTED", "NAME, not a path",
				"NOT an empty repository", "never looked for",
				"lsp", "FAILS OPEN",
			},
			// The two numbers a skimming reader would keep, and both would be
			// wrong: a worker count nobody took, and a refusal nobody made.
			deny: []string{"Workers:    0", "would currently be refused"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printRepoOccupancy(&buf, tc.occ, tc.hostHeavy)
			got := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q; got:\n%s", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Errorf("must not contain %q; got:\n%s", d, got)
				}
			}
		})
	}
}

// TestWorkerBudgetRendersBothStatesAndItsOwnLimits. The budget is the only
// number this command prints that is NOT a measurement, and it is the one a
// reader is most likely to mistake for one. It has to say what it is derived
// from and that nothing enforces it — a share the reader believes is enforced is
// worse than no share at all, because it reads as a reason to stop looking at
// the host gate.
func TestWorkerBudgetRendersBothStatesAndItsOwnLimits(t *testing.T) {
	var derived bytes.Buffer
	printWorkerBudget(&derived, agent.WorkerBudget{
		Cores: 3, HostCores: 10, Basis: "10 cores divided by the per-repo dispatch cap of 3 workers",
	})
	for _, want := range []string{
		"3 of 10 cores per worker",
		"per-repo dispatch cap",
		"Advisory",
		"$POGO_WORKER_CORES",
		"Nothing enforces it",
	} {
		if !strings.Contains(derived.String(), want) {
			t.Errorf("missing %q; got:\n%s", want, derived.String())
		}
	}

	var unknown bytes.Buffer
	printWorkerBudget(&unknown, agent.WorkerBudget{Basis: "host core count unknown"})
	if !strings.Contains(unknown.String(), "NOT DERIVED") {
		t.Errorf("an underived budget must not render as a share of nothing; got:\n%s", unknown.String())
	}
	if strings.Contains(unknown.String(), "0 of 0") {
		t.Errorf("rendered a zero budget as a number; got:\n%s", unknown.String())
	}
}
