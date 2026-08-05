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
		want []string
		deny []string
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
			want: []string{"would currently be refused with 503", "DIFFERENT repo is unaffected"},
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printRepoOccupancy(&buf, tc.occ)
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
