package orphanwatch

import "testing"

// TestOwnerFromCwd covers the two path shapes this fleet actually produces and,
// more importantly, the cases that must NOT attribute. A rule that over-attributes
// convicts unrelated programs; one that under-attributes reports the defect it
// was built for as unattributable.
func TestOwnerFromCwd(t *testing.T) {
	const root = "/Users/daniel/.pogo/polecats"

	cases := []struct {
		name   string
		cwd    string
		want   string
		wantOK bool
	}{{
		// The reproduction built for mg-4518: `nohup` run from the polecat's
		// own worktree, so cwd is the root entry with nothing after it.
		name: "worktree root exactly", cwd: root + "/q4518", want: "q4518", wantOK: true,
	}, {
		// The first confirmed instance: pid 81021, owner p131e, running out of
		// its instrument directory 44 minutes after the branch had merged.
		name: "instrument dir under worktree",
		cwd:  root + "/p131e/code/dual_certificate_131e", want: "p131e", wantOK: true,
	}, {
		// The near-miss: p00a1's four live workers, whose cwd was the harness
		// scratchpad rather than anywhere under the polecats root.
		name: "harness scratchpad slug",
		cwd:  "/private/tmp/claude-501/-Users-daniel--pogo-polecats-p00a1/1c9f/scratchpad",
		want: "p00a1", wantOK: true,
	}, {
		name: "trailing slash", cwd: root + "/a41b7/", want: "a41b7", wantOK: true,
	}, {
		name: "the root itself is not a polecat", cwd: root, wantOK: false,
	}, {
		name: "a sibling of the root", cwd: "/Users/daniel/.pogo/agents/mayor", wantOK: false,
	}, {
		// A prefix that merely starts the same must not match: the boundary is
		// a path component, not a string prefix.
		name: "root-prefixed sibling directory", cwd: root + "-old/q4518", wantOK: false,
	}, {
		name: "escapes upward", cwd: root + "/../agents", wantOK: false,
	}, {
		name: "unrelated program", cwd: "/Users/daniel/dev/pogo", wantOK: false,
	}, {
		name: "empty cwd", cwd: "", wantOK: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := OwnerFromCwd(root, tc.cwd)
			if ok != tc.wantOK {
				t.Fatalf("OwnerFromCwd(%q, %q) ok = %v, want %v (got owner %q)", root, tc.cwd, ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("OwnerFromCwd(%q, %q) = %q, want %q", root, tc.cwd, got, tc.want)
			}
		})
	}
}

// TestOwnerFromCwdEmptyRootAttributesNothing pins the fail-closed direction for
// an unresolvable root. Attributing against "" would make every absolute path
// look like it lives under the root, which is the difference between a detector
// and a machine for convicting the whole box.
func TestOwnerFromCwdEmptyRootAttributesNothing(t *testing.T) {
	if owner, ok := OwnerFromCwd("", "/Users/daniel/.pogo/polecats/q4518"); ok {
		t.Errorf("OwnerFromCwd with empty root attributed %q; must attribute nothing", owner)
	}
}

// TestSlugifyMatchesHarnessConvention pins the derivation of the scratchpad
// component against the observed literal. If the harness ever changes its
// naming this test is where that is discovered, rather than in a silently
// empty report.
func TestSlugifyMatchesHarnessConvention(t *testing.T) {
	const observed = "-Users-daniel--pogo-polecats-p00a1"
	got := slugify("/Users/daniel/.pogo/polecats") + "-p00a1"
	if got != observed {
		t.Errorf("slugify-derived component = %q, want the observed %q", got, observed)
	}
}
