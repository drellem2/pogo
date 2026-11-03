package vcs

import (
	"reflect"
	"testing"
)

// A re-made Change retires the one it names (mg-5b904), within its own item
// only; a listing with no such trailer is returned as it is.
func TestDropSuperseded(t *testing.T) {
	ch := func(id, item string, tr ...Trailer) Change {
		ts := []Trailer{{Key: TrailerItem, Value: item}}
		return Change{ID: id, Trailers: append(ts, tr...)}
	}
	sup := func(v string) Trailer { return Trailer{Key: "pogo-supersedes", Value: v} }
	ids := func(cs []Change) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	plain := []Change{ch("a", "mg-1"), ch("b", "mg-1")}
	if got := DropSuperseded(plain); !reflect.DeepEqual(ids(got), []string{"a", "b"}) {
		t.Errorf("no trailer: %v", ids(got))
	}
	cs := []Change{ch("a", "mg-1"), ch("b", "mg-1"), ch("c", "mg-1", sup("a, b")), ch("d", "mg-2", sup("c")), ch("e", "", sup("d"))}
	if got := DropSuperseded(cs); !reflect.DeepEqual(ids(got), []string{"c", "d", "e"}) {
		t.Errorf("DropSuperseded = %v; want c, d, e (a and b retired by c; another item's or no item's trailer retires nothing)", ids(got))
	}
	self := []Change{ch("a", "mg-1", sup("a"))}
	if got := DropSuperseded(self); len(got) != 1 {
		t.Errorf("a Change retired itself: %v", ids(got))
	}
}
