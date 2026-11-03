package enthvcs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// TestEveryOperationIsUnsupported walks the still-inert group of
// vcs.Backend (Refs, until slice 12) by reflection, so a method added to them
// later is covered without editing this test: each one, called with zero
// arguments, must return an error wrapping vcs.ErrUnsupported. Kind must say
// enth. Workspaces has a body since slice 5 (mg-cc5cd), Work since slice 6
// (mg-30da0), Lander since slice 7 (mg-1fe2b), and each has its own tests.
func TestEveryOperationIsUnsupported(t *testing.T) {
	var b vcs.Backend = Backend{Repo: vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}}
	if b.Kind() != vcs.Enth {
		t.Fatalf("Kind() = %q, want enth", b.Kind())
	}
	// Kind, and every Workspaces (slice 5), Work (slice 6) and Lander
	// (slice 7) method, are skipped.
	total := map[string]bool{"Kind": true}
	for _, g := range []reflect.Type{reflect.TypeOf((*vcs.Workspaces)(nil)).Elem(), reflect.TypeOf((*vcs.Work)(nil)).Elem(), reflect.TypeOf((*vcs.Lander)(nil)).Elem()} {
		for i := 0; i < g.NumMethod(); i++ {
			total[g.Method(i).Name] = true
		}
	}
	iface := reflect.TypeOf((*vcs.Backend)(nil)).Elem()
	v := reflect.ValueOf(b)
	errType := reflect.TypeOf((*error)(nil)).Elem()
	checked := 0
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if total[m.Name] {
			continue
		}
		fn := v.MethodByName(m.Name)
		args := make([]reflect.Value, fn.Type().NumIn())
		for j := range args {
			args[j] = reflect.Zero(fn.Type().In(j))
		}
		args[0] = reflect.ValueOf(context.Background())
		out := fn.Call(args)
		last := out[len(out)-1]
		if !last.Type().Implements(errType) {
			t.Fatalf("%s: last result is not an error", m.Name)
		}
		err, _ := last.Interface().(error)
		if !errors.Is(err, vcs.ErrUnsupported) {
			t.Errorf("%s returned %v, want an error wrapping vcs.ErrUnsupported", m.Name, err)
		}
		checked++
	}
	// Positive control: the walk must actually have visited the groups.
	if checked < 3 {
		t.Errorf("checked only %d methods; the reflection walk is not seeing vcs.Backend", checked)
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
