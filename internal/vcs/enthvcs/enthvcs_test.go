package enthvcs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// TestEveryOperationIsUnsupported walks vcs.Backend by reflection, so a
// method added to the interface later is covered without editing this test:
// each one, called with zero arguments, must return an error wrapping
// vcs.ErrUnsupported. Kind is the one exception and must say enth.
func TestEveryOperationIsUnsupported(t *testing.T) {
	var b vcs.Backend = Backend{Repo: vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}}
	if b.Kind() != vcs.Enth {
		t.Fatalf("Kind() = %q, want enth", b.Kind())
	}
	// Methods whose contract has no error result; checked by hand below.
	total := map[string]bool{"Kind": true, "ResolveBase": true, "BranchExists": true, "BranchSpent": true, "Refresh": true}
	ctx := context.Background()
	r := vcs.Repo{Path: "/x"}
	if b.ResolveBase(ctx, r, "main") != "" || b.BranchExists(ctx, r, "b") || b.BranchSpent(ctx, r, "b", "main") {
		t.Error("an inert total method answered as if the repo had something")
	}
	if got := b.Refresh(ctx, "/x"); got.Status != vcs.RefreshFailed {
		t.Errorf("Refresh status = %q, want failed (unknown freshness, never current)", got.Status)
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
	if checked < 15 {
		t.Errorf("checked only %d methods; the reflection walk is not seeing vcs.Backend", checked)
	}
}
