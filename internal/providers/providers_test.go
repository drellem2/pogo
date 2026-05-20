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
