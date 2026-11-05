package enthvcs

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// `pogo presubmit --print-stack` prints PrintStackLine: the ids bottom first,
// on ONE line, single-space-separated. That is a contract with two consumers
// (mg-88ece) — self_land splits it on whitespace, and a re-make writes it
// whole into Pogo-Supersedes — so it is pinned here rather than left to
// whatever strings.Join happened to be called with.
func TestPrintStackLineIsOneSpaceSeparatedLine(t *testing.T) {
	for _, tc := range []struct {
		ids  []string
		want string
	}{
		{[]string{"7dc1aaaa"}, "7dc1aaaa"},
		{[]string{"7dc1aaaa", "7db1bbbb"}, "7dc1aaaa 7db1bbbb"},
		{[]string{"c1", "c2", "c3"}, "c1 c2 c3"},
	} {
		got := PrintStackLine(tc.ids)
		if got != tc.want {
			t.Fatalf("PrintStackLine(%q) = %q, want %q", tc.ids, got, tc.want)
		}
		if strings.ContainsAny(got, "\n\r\t") {
			t.Fatalf("PrintStackLine(%q) = %q: a trailer value must stay on one line", tc.ids, got)
		}
		if f := strings.Fields(got); strings.Join(f, ",") != strings.Join(tc.ids, ",") {
			t.Fatalf("PrintStackLine(%q) does not split back into its ids: %q", tc.ids, f)
		}
	}
}

// The second consumer: a re-made Change carries Pogo-Supersedes="$OLD", where
// OLD is the --print-stack line. A 2-Change stack must retire BOTH old
// Changes, not one and not a phantom "id1 id2".
func TestSupersedesOfAMultiChangeStackRetiresEveryChange(t *testing.T) {
	item := []vcs.Trailer{{Key: vcs.TrailerItem, Value: "mg-1"}}
	old1 := vcs.Change{ID: "7dc1aaaa", Trailers: item}
	old2 := vcs.Change{ID: "7db1bbbb", Base: "7dc1aaaa", Trailers: item}
	remade := vcs.Change{ID: "9e9ecccc", Trailers: append(append([]vcs.Trailer{}, item...),
		vcs.Trailer{Key: vcs.TrailerSupersedes, Value: PrintStackLine([]string{old1.ID, old2.ID})})}
	got := vcs.DropSuperseded([]vcs.Change{old1, old2, remade})
	if len(got) != 1 || got[0].ID != remade.ID {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		t.Fatalf("after a re-make superseding a 2-Change stack, want only %s left, got %q", remade.ID, ids)
	}
}
