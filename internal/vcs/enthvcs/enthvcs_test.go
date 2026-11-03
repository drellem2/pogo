package enthvcs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// TestEveryOperationIsUnsupported walks the still-inert groups of
// vcs.Backend (Work, Lander, Refs) by reflection, so a method added to them
// later is covered without editing this test: each one, called with zero
// arguments, must return an error wrapping vcs.ErrUnsupported. Kind must say
// enth. Workspaces has a body since slice 5 (mg-cc5cd) and its own tests.
func TestEveryOperationIsUnsupported(t *testing.T) {
	var b vcs.Backend = Backend{Repo: vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}}
	if b.Kind() != vcs.Enth {
		t.Fatalf("Kind() = %q, want enth", b.Kind())
	}
	// Kind, and every Workspaces method (built in slice 5), are skipped.
	total := map[string]bool{"Kind": true}
	ws := reflect.TypeOf((*vcs.Workspaces)(nil)).Elem()
	for i := 0; i < ws.NumMethod(); i++ {
		total[ws.Method(i).Name] = true
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
	if checked < 10 {
		t.Errorf("checked only %d methods; the reflection walk is not seeing vcs.Backend", checked)
	}
}
