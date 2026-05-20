package watch

import "testing"

func TestOpHas(t *testing.T) {
	op := Create | Write
	if !op.Has(Create) {
		t.Errorf("expected Create bit set")
	}
	if !op.Has(Write) {
		t.Errorf("expected Write bit set")
	}
	if op.Has(Remove) {
		t.Errorf("did not expect Remove bit set")
	}
}

func TestOpString(t *testing.T) {
	if got := (Create | Remove).String(); got != "CREATE|REMOVE" {
		t.Errorf("expected CREATE|REMOVE, got %s", got)
	}
	if got := Op(0).String(); got != "NONE" {
		t.Errorf("expected NONE, got %s", got)
	}
}

func TestEventHas(t *testing.T) {
	e := Event{Path: "/tmp/x", Op: Write}
	if !e.Has(Write) {
		t.Errorf("expected event to have Write")
	}
	if e.Has(Create) {
		t.Errorf("did not expect event to have Create")
	}
}

func TestIsExcludedDir(t *testing.T) {
	excluded := []string{".git", ".pogo", "node_modules", "vendor", "build", "dist", "MyApp.app"}
	for _, name := range excluded {
		if !IsExcludedDir(name) {
			t.Errorf("expected %q to be excluded", name)
		}
	}
	included := []string{"src", "internal", "cmd", "lib", "app"}
	for _, name := range included {
		if IsExcludedDir(name) {
			t.Errorf("did not expect %q to be excluded", name)
		}
	}
}

// TestNewWatcher verifies the platform backend constructs and closes cleanly.
func TestNewWatcher(t *testing.T) {
	w, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if w.Events() == nil || w.Errors() == nil {
		t.Errorf("expected non-nil event/error channels")
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}
