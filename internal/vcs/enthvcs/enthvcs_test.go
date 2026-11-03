package enthvcs

import (
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// TestKindIsEnth: the backend says what it is. Every group of vcs.Backend
// now has a body — Workspaces since slice 5 (mg-cc5cd), Work since slice 6
// (mg-30da0), Lander since slice 7 (refusing since mg-5b904), and Refs since
// slice 12 (mg-ae4ac, refs_test.go) — so the reflection walk that checked the
// inert groups answered ErrUnsupported has nothing left to walk, and each
// group has its own tests.
func TestKindIsEnth(t *testing.T) {
	var b vcs.Backend = Backend{Repo: vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}}
	if b.Kind() != vcs.Enth {
		t.Fatalf("Kind() = %q, want enth", b.Kind())
	}
}

func TestParseChanges(t *testing.T) {
	out := []byte(`{"type":"change","id":"c1","entry":3,"author":{"name":"ann","email":{"hex":"616e6e40"}},"created":1700000500,"base":"r","trailers":[{"key":"Pogo-Item","value":"mg-1"}],"landed":null}
{"type":"change","id":"c2","entry":4,"author":{"name":"ann","email":"a@x"},"created":1700000600,"base":null,"trailers":[],"landed":"L2"}
`)
	cs, err := parseChanges(out)
	if err != nil || len(cs) != 2 {
		t.Fatalf("parseChanges = %v, %v", cs, err)
	}
	if cs[0].AuthorEmail != "ann@" || cs[0].Trailer("pogo-item") != "mg-1" || cs[0].Landed != "" || cs[0].Base != "r" || cs[0].Created.Unix() != 1700000500 {
		t.Errorf("first = %+v", cs[0])
	}
	if cs[1].Landed != "L2" || cs[1].Base != "" {
		t.Errorf("second = %+v", cs[1])
	}
	// An unreadable line is an error, never a dropped Change.
	if _, err := parseChanges([]byte("{\"type\":\"change\",\"id\":\"c1\"}\nnot json\n")); err == nil {
		t.Error("an unreadable line was skipped")
	}
}
