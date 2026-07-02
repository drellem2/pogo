package providers

import "testing"

func TestResolveClaude(t *testing.T) {
	p, ok := Resolve("claude")
	if !ok {
		t.Fatal("Resolve(\"claude\") returned ok=false")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"claude\") = %+v, want provider with ID=claude", p)
	}
	if p.Binary != "claude" {
		t.Errorf("claude provider Binary = %q, want %q", p.Binary, "claude")
	}
}

func TestResolveCodex(t *testing.T) {
	p, ok := Resolve("codex")
	if !ok {
		t.Fatal("Resolve(\"codex\") returned ok=false")
	}
	if p == nil || p.ID != "codex" {
		t.Fatalf("Resolve(\"codex\") = %+v, want provider with ID=codex", p)
	}
	if p.Binary != "codex" {
		t.Errorf("codex provider Binary = %q, want %q", p.Binary, "codex")
	}
}

func TestResolvePi(t *testing.T) {
	p, ok := Resolve("pi")
	if !ok {
		t.Fatal("Resolve(\"pi\") returned ok=false")
	}
	if p == nil || p.ID != "pi" {
		t.Fatalf("Resolve(\"pi\") = %+v, want provider with ID=pi", p)
	}
	if p.Binary != "pi" {
		t.Errorf("pi provider Binary = %q, want %q", p.Binary, "pi")
	}
}

func TestAllContainsEveryProvider(t *testing.T) {
	all := All()
	want := []string{"claude", "codex", "pi"}
	if len(all) != len(want) {
		t.Fatalf("All() returned %d providers, want %d", len(all), len(want))
	}
	for i, id := range want {
		if all[i] == nil || all[i].ID != id {
			t.Errorf("All()[%d].ID = %v, want %q", i, all[i], id)
		}
	}
}

func TestResolveEmptyDefaultsToClaude(t *testing.T) {
	p, ok := Resolve("")
	if !ok {
		t.Fatal("Resolve(\"\") returned ok=false; empty id should default to Claude")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"\") = %+v, want the Claude default", p)
	}
}

func TestResolveUnknownFallsBackToClaude(t *testing.T) {
	p, ok := Resolve("nonesuch")
	if ok {
		t.Error("Resolve(\"nonesuch\") returned ok=true for an unknown provider")
	}
	if p == nil || p.ID != "claude" {
		t.Fatalf("Resolve(\"nonesuch\") = %+v, want the Claude fallback so startup never wedges", p)
	}
}
