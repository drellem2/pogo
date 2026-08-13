package agent

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// modelProvider builds a minimal Provider that can (modelFlag != "") or cannot
// (modelFlag == "") express a model selection.
func modelProvider(id, modelFlag string) *Provider {
	return &Provider{
		ID:        id,
		Binary:    id,
		ModelFlag: modelFlag,
		Nudge:     NudgeProfile{SubmitTerminator: "\r", IdleThreshold: time.Second},
	}
}

// TestValidateModelAcceptsRealModelShapes pins the syntax allowlist against the
// naming conventions pogo's four harnesses actually use. It is a regression
// guard in the direction that matters most: a validator tightened without
// checking these would start refusing working models, and the failure would
// look like "pogo says my model is invalid" rather than like a pogo bug.
func TestValidateModelAcceptsRealModelShapes(t *testing.T) {
	for _, m := range []string{
		"fable",
		"claude-opus-5",
		"claude-haiku-4-5-20251001",
		"gpt-5.5",
		"anthropic/claude-opus-5",           // pi / openrouter style
		"us.anthropic.claude-sonnet-4-v1:0", // Bedrock inference-profile style
		"auto",                              // Cursor's default
		"some_model+variant@2026",           // the remaining allowed punctuation
		strings.Repeat("m", maxModelLen),    // exactly at the cap
	} {
		if err := ValidateModel(m); err != nil {
			t.Errorf("ValidateModel(%q) = %v, want nil", m, err)
		}
	}
}

// TestValidateModelRefusesUnusableValues covers each refusal rule, and in
// particular the two that are load-bearing rather than cosmetic: a leading '-'
// would be parsed by the harness as another flag, and whitespace cannot survive
// the strings.Fields split that command templates go through.
func TestValidateModelRefusesUnusableValues(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  string // substring the error must name
	}{
		{"empty", "", "empty"},
		{"leading dash reads as a flag", "-p", "read as a flag"},
		{"leading double dash", "--dangerously-skip-permissions", "read as a flag"},
		{"embedded space", "claude opus", "disallowed character"},
		{"tab", "claude\topus", "disallowed character"},
		{"leading whitespace", " fable", "whitespace"},
		{"trailing whitespace", "fable ", "whitespace"},
		{"shell metacharacter", "fable;rm -rf /", "disallowed character"},
		{"command substitution", "$(whoami)", "disallowed character"},
		{"newline", "fable\nmore", "disallowed character"},
		{"over the length cap", strings.Repeat("m", maxModelLen+1), "over the"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModel(tc.model)
			if err == nil {
				t.Fatalf("ValidateModel(%q) = nil, want an error", tc.model)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateModel(%q) error = %q, want it to mention %q", tc.model, err, tc.want)
			}
		})
	}
}

// TestResolveModelPrecedence is the acceptance bar the architect named: the
// flag beats template frontmatter, frontmatter is the floor below it, and with
// neither set the answer is NO MODEL — not a default.
func TestResolveModelPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		flag      string
		fm        string
		wantModel string
		wantTier  string
	}{
		{"flag beats frontmatter", "fable", "claude-opus-5", "fable", ModelTierFlag},
		{"flag alone", "fable", "", "fable", ModelTierFlag},
		{"frontmatter alone", "", "claude-opus-5", "claude-opus-5", ModelTierFrontmatter},
		{"neither pins nothing", "", "", "", ModelTierUnset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, tier, err := ResolveModel(tc.flag, tc.fm)
			if err != nil {
				t.Fatalf("ResolveModel(%q, %q): %v", tc.flag, tc.fm, err)
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
			if tier != tc.wantTier {
				t.Errorf("tier = %q, want %q", tier, tc.wantTier)
			}
		})
	}
}

// TestResolveModelHasNoDefault is the guard on the constraint that this whole
// feature had to be built around: pogo pins NO model. It is separate from the
// precedence table above, and phrased as its own named test, because the
// precedence table would still pass if someone added a fallback constant to the
// bottom of the chain — the "neither" row would just start reporting it. The
// 2026-07-06 outage (an exhausted pinned model wedged every agent on the box for
// 5.5 hours, because Claude Code does not degrade to another model) is the
// reason this is a test rather than a comment. See model.go.
func TestResolveModelHasNoDefault(t *testing.T) {
	model, tier, err := ResolveModel("", "")
	if err != nil {
		t.Fatalf("ResolveModel with nothing set should not error: %v", err)
	}
	if model != "" {
		t.Fatalf("ResolveModel(\"\", \"\") = %q; pogo must pin NO model by default — "+
			"a pinned model that runs out of credit wedges the harness fleet-wide "+
			"rather than degrading (see internal/agent/model.go)", model)
	}
	if tier != ModelTierUnset {
		t.Errorf("tier = %q, want %q", tier, ModelTierUnset)
	}

	// And the argv consequence of that, which is the part a reader can check
	// against a running process: no model flag reaches the command line.
	p := modelProvider("claude", "--model")
	got, err := applyModel([]string{"claude", "--dangerously-skip-permissions"}, p, model)
	if err != nil {
		t.Fatalf("applyModel: %v", err)
	}
	for _, arg := range got {
		if arg == "--model" {
			t.Fatalf("argv %v carries --model with no model selected", got)
		}
	}
}

// TestResolveModelRejectsBadValuesAtEachTier verifies a malformed value is
// refused whichever tier supplied it, and that the error names that tier — the
// error is the only thing a dispatcher sees, so it has to say where to look.
func TestResolveModelRejectsBadValuesAtEachTier(t *testing.T) {
	if _, tier, err := ResolveModel("-p", ""); err == nil {
		t.Errorf("a flag value of %q should be refused", "-p")
	} else if !strings.Contains(err.Error(), ModelTierFlag) {
		t.Errorf("error %q should name the %s tier (got tier %q)", err, ModelTierFlag, tier)
	}
	if _, _, err := ResolveModel("", "bad model"); err == nil {
		t.Errorf("a frontmatter value of %q should be refused", "bad model")
	} else if !strings.Contains(err.Error(), ModelTierFrontmatter) {
		t.Errorf("error %q should name the %s tier", err, ModelTierFrontmatter)
	}
}

// TestModelArgsTranslatesThroughTheProvider verifies the Provider boundary holds:
// pogo asks for a model in provider-neutral terms and the descriptor supplies the
// argv. A provider that cannot express one FAILS rather than dropping the request.
func TestModelArgsTranslatesThroughTheProvider(t *testing.T) {
	t.Run("provider with a flag", func(t *testing.T) {
		args, err := modelProvider("claude", "--model").ModelArgs("fable")
		if err != nil {
			t.Fatalf("ModelArgs: %v", err)
		}
		if want := []string{"--model", "fable"}; !reflect.DeepEqual(args, want) {
			t.Errorf("ModelArgs = %v, want %v", args, want)
		}
	})

	t.Run("no model requested yields no args", func(t *testing.T) {
		args, err := modelProvider("claude", "--model").ModelArgs("")
		if err != nil {
			t.Fatalf("ModelArgs(\"\"): %v", err)
		}
		if len(args) != 0 {
			t.Errorf("ModelArgs(\"\") = %v, want none", args)
		}
	})

	t.Run("provider that cannot express a model fails loudly", func(t *testing.T) {
		_, err := modelProvider("mystery", "").ModelArgs("fable")
		if err == nil {
			t.Fatal("a provider with no ModelFlag must refuse a model request, not ignore it")
		}
		if !strings.Contains(err.Error(), "mystery") {
			t.Errorf("error %q should name the provider that cannot express the model", err)
		}
	})

	t.Run("nil provider fails loudly", func(t *testing.T) {
		var p *Provider
		if _, err := p.ModelArgs("fable"); err == nil {
			t.Fatal("a nil provider must refuse a model request, not ignore it")
		}
		// ...but a nil provider with no model requested is the ordinary
		// bare-registry spawn and must stay silent.
		if args, err := p.ModelArgs(""); err != nil || args != nil {
			t.Errorf("nil provider with no model = (%v, %v), want (nil, nil)", args, err)
		}
	})
}

// TestApplyModelDoesNotMutateCallerSlice guards the shared-backing-array hazard:
// the expanded command template is built once per spawn, and appending in place
// could scribble into a caller's slice.
func TestApplyModelDoesNotMutateCallerSlice(t *testing.T) {
	p := modelProvider("claude", "--model")
	base := make([]string, 2, 8) // spare capacity: append would reuse it
	base[0], base[1] = "claude", "--dangerously-skip-permissions"

	got, err := applyModel(base, p, "fable")
	if err != nil {
		t.Fatalf("applyModel: %v", err)
	}
	if want := []string{"claude", "--dangerously-skip-permissions", "--model", "fable"}; !reflect.DeepEqual(got, want) {
		t.Errorf("applyModel = %v, want %v", got, want)
	}
	if len(base) != 2 {
		t.Errorf("caller slice was mutated: %v", base)
	}
}

// TestSpawnAppliesModelToArgv is the end-to-end acceptance bar: the resolved
// model reaches the spawned process's argv, and is recorded on the agent so
// "which model is this on?" is answerable without parsing a command line.
func TestSpawnAppliesModelToArgv(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	p := modelProvider("claude", "--model")
	reg.RegisterProvider(p)

	// `cat --model fable` is not a real invocation; the assertion is on the
	// recorded argv, and cat ignores its arguments while staying alive on an
	// open PTY, which is what every other spawn test in this package relies on.
	a, err := reg.Spawn(SpawnRequest{
		Name: "m-pinned", Type: TypePolecat, Command: []string{"cat"},
		Provider: p, Model: "fable",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if want := []string{"cat", "--model", "fable"}; !reflect.DeepEqual(a.Command, want) {
		t.Errorf("spawned argv = %v, want %v", a.Command, want)
	}
	if a.Model != "fable" {
		t.Errorf("agent Model = %q, want %q", a.Model, "fable")
	}

	// The unpinned spawn on the SAME registry and provider must carry no model
	// flag: model selection is per-spawn, never sticky.
	b, err := reg.Spawn(SpawnRequest{
		Name: "m-unpinned", Type: TypePolecat, Command: []string{"cat"}, Provider: p,
	})
	if err != nil {
		t.Fatalf("Spawn unpinned: %v", err)
	}
	if want := []string{"cat"}; !reflect.DeepEqual(b.Command, want) {
		t.Errorf("unpinned argv = %v, want %v — a model must never leak across spawns", b.Command, want)
	}
	if b.Model != "" {
		t.Errorf("unpinned agent Model = %q, want empty", b.Model)
	}
}

// TestSpawnAppliesModelBeforeTheArgvPrompt guards the ordering bug this change
// nearly shipped with. Providers that deliver the initial task as a trailing
// positional argv element (pi, cursor) rebuild the command in Spawn; building it
// from the pre-model slice drops the model silently, and ONLY for those two
// providers — which is exactly the shape of defect that reaches production.
func TestSpawnAppliesModelBeforeTheArgvPrompt(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	p := modelProvider("pi", "--model")
	p.InitialPromptViaArgv = true
	reg.RegisterProvider(p)

	a, err := reg.Spawn(SpawnRequest{
		Name: "m-argv", Type: TypePolecat, Command: []string{"cat"},
		Provider: p, Model: "fable", InitialNudge: "do the thing",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	want := []string{"cat", "--model", "fable", "do the thing"}
	if !reflect.DeepEqual(a.Command, want) {
		t.Errorf("spawned argv = %v, want %v — the model flag must precede the "+
			"trailing positional prompt, and must not be dropped by it", a.Command, want)
	}
}

// TestSpawnRefusesAModelItCannotExpress verifies the failure is a refused spawn
// rather than a worker quietly running on a model nobody chose.
func TestSpawnRefusesAModelItCannotExpress(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	p := modelProvider("mystery", "") // declares no model flag
	reg.RegisterProvider(p)

	a, err := reg.Spawn(SpawnRequest{
		Name: "m-refused", Type: TypePolecat, Command: []string{"cat"},
		Provider: p, Model: "fable",
	})
	if err == nil {
		t.Fatalf("Spawn should have failed; got agent %v running on an unexpressed model", a)
	}
	if !strings.Contains(err.Error(), "mystery") {
		t.Errorf("error %q should name the provider that could not express the model", err)
	}
	if reg.Get("m-refused") != nil {
		t.Error("a refused spawn must leave no registered agent behind")
	}
}
