package agent

import (
	"fmt"
	"log"
	"strings"
)

// Model selection: which MODEL an agent runs on, as distinct from which
// HARNESS runs it (that is Provider — see provider.go). The two are separate
// axes and this file is careful never to conflate them: `--provider codex`
// picks the harness binary, `--model gpt-5.5` picks what that binary talks to.
//
// # There is deliberately no default, and no config tier
//
// The resolution chain here is exactly two tiers deep — a --model flag, then a
// model: key in the agent prompt's frontmatter — and when neither is set pogo
// passes NO model argument at all, leaving the harness on whatever the user's
// own harness configuration selects. That floor is not an omission to be filled
// in later. It is the fix for a measured outage.
//
// On 2026-07-06 a model pinned in pogo's config was exhausted mid-day. Claude
// Code does NOT degrade to another model when its pinned one is unavailable: it
// wedges at a "keep using this model or switch models" prompt, which looks
// exactly like a busy agent from the outside. Every crew agent and every polecat
// on the machine sat silent for ~5.5 hours, because every agent spawns through
// the same override, so the blast radius of one bad value is the whole fleet
// rather than one worker. Recovery needed a human running /model by hand.
// Daniel's directive after that incident was that pogo hardcodes no model, and
// the reason it took five weeks to reach this file is that the reasoning lived
// only in comments inside ~/.config/pogo/config.toml — a file this change would
// otherwise never have made anyone open (mg-e7f5).
//
// So: adding a per-spawn model SELECTION is compatible with that directive, and
// is the whole point of this file. Adding a default, a fallback constant, or a
// config-level pin is not, and would re-open the hole. If a later change wants a
// [agents] model tier, it has to answer for what happens when that value stops
// working while nobody is watching.
//
// # Validation is syntactic only
//
// ValidateModel checks that a value is a plausible model identifier and that it
// cannot be smuggled into argv as something other than a model name. It does NOT
// check that the model exists, that the account can reach it, or that it has
// credit left — pogo keeps no allowed-model list, because a list here would go
// stale on the harness vendor's release schedule and would refuse working models
// as confidently as broken ones. The only check that actually proves a value is
// usable is running the harness against it, e.g.
//
//	claude --model "<value>" -p "ok"     # exit 0 means the value is usable
//
// which is a thing to do BEFORE pointing a fleet at a new model, not a thing to
// do inside a spawn handler.

// maxModelLen bounds a model identifier. Generous next to any real name
// ("claude-opus-5", "us.anthropic.claude-sonnet-4-20250514-v1:0",
// "anthropic/claude-opus-5"), tight enough that a pasted paragraph is refused
// rather than handed to a harness as a flag value.
const maxModelLen = 128

// ValidateModel reports whether model is syntactically usable as a harness model
// argument. It is a syntax check and nothing more — see the package comment
// above for why pogo keeps no list of known-good models.
//
// The rules exist to keep a model value from becoming something other than a
// model value:
//
//   - A leading "-" is refused: the value is passed as its own argv element
//     immediately after the provider's model flag, so "-foo" would be read by
//     the harness as another flag rather than as the flag's argument.
//   - Whitespace is refused: command templates are split with strings.Fields,
//     so a value containing a space cannot survive a template round-trip intact,
//     and a value that means one thing on one path and another elsewhere is
//     worse than a refusal.
//   - The character set is a conservative allowlist covering every model-naming
//     convention pogo's providers use (dotted Bedrock ids, slashed
//     vendor/model ids, colon-suffixed versions).
//
// An empty model is refused here; callers treat "" as "no model requested" and
// must not call this.
func ValidateModel(model string) error {
	if model == "" {
		return fmt.Errorf("model is empty")
	}
	if model != strings.TrimSpace(model) {
		return fmt.Errorf("model %q has leading or trailing whitespace", model)
	}
	if len(model) > maxModelLen {
		return fmt.Errorf("model is %d bytes, over the %d-byte limit", len(model), maxModelLen)
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("model %q starts with '-'; it would be read as a flag, not as a model name", model)
	}
	for _, r := range model {
		if !isModelRune(r) {
			return fmt.Errorf("model %q contains disallowed character %q (allowed: letters, digits, and . _ - : / @ +)", model, r)
		}
	}
	return nil
}

// isModelRune reports whether r may appear in a model identifier.
func isModelRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '.', '_', '-', ':', '/', '@', '+':
		return true
	}
	return false
}

// Model resolution tier labels, returned by ResolveModel so callers can log
// which tier supplied the answer — the same shape resolveProviderLocked uses.
const (
	ModelTierFlag        = "--model flag"
	ModelTierFrontmatter = "model: frontmatter"
	// ModelTierUnset is the floor: no model argument is passed and the harness
	// uses the user's own configuration. See the package comment — this is the
	// deliberate absence of a default, not a missing tier.
	ModelTierUnset = "unset (harness default)"
)

// ResolveModel walks the per-spawn model precedence chain:
//
//  1. flagModel        — explicit --model override (spawn-polecat)
//  2. frontmatterModel — model: key in the agent prompt's frontmatter
//  3. nothing          — no model argument at all; the harness decides
//
// It returns the selected model ("" for tier 3), the tier that supplied it, and
// an error if the selected value fails ValidateModel. It is a pure function
// rather than a Registry method precisely because there is no config tier to
// consult: see the package comment for why that floor is load-bearing.
func ResolveModel(flagModel, frontmatterModel string) (model, tier string, err error) {
	model, tier = flagModel, ModelTierFlag
	if model == "" {
		model, tier = frontmatterModel, ModelTierFrontmatter
	}
	if model == "" {
		return "", ModelTierUnset, nil
	}
	if err := ValidateModel(model); err != nil {
		return "", tier, fmt.Errorf("%s: %w", tier, err)
	}
	return model, tier, nil
}

// ModelArgs returns the argv elements that ask this provider's harness to run on
// model, or nil when model is empty (no selection — the harness's own default
// applies).
//
// A provider that declares no ModelFlag cannot express a model selection, and
// this returns an error rather than nil: silently dropping the request would
// spawn a worker on a model nobody chose while every log line said otherwise.
// That is the failure the caller must surface, so the spawn fails instead.
func (p *Provider) ModelArgs(model string) ([]string, error) {
	if model == "" {
		return nil, nil
	}
	if err := ValidateModel(model); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("cannot select model %q: no harness provider resolved for this spawn", model)
	}
	if p.ModelFlag == "" {
		return nil, fmt.Errorf("provider %q cannot express a model selection (it declares no model flag); "+
			"remove --model / the model: frontmatter key, or select a provider that supports one", p.ID)
	}
	return []string{p.ModelFlag, model}, nil
}

// applyModel appends provider p's model-selection argv to command.
//
// It appends rather than rewriting, and appends LAST among the flags, so that a
// harness with last-flag-wins parsing honors the requested model over any
// --model already baked into an operator's [agents] command override. That
// override is not silently obeyed either: a template that already carries the
// flag is warned about by name, because two model flags on one command line is
// an ambiguity pogo cannot resolve on the operator's behalf.
//
// The command slice is copied, never appended in place, so a caller's slice —
// and in particular a registry-wide template expansion — is never mutated.
func applyModel(command []string, p *Provider, model string) ([]string, error) {
	args, err := p.ModelArgs(model)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return command, nil
	}
	for _, arg := range command {
		if arg == p.ModelFlag {
			log.Printf("WARNING: command template already carries %s; appending the requested model %q last, "+
				"which wins only under last-flag-wins parsing — check the [agents] command override",
				p.ModelFlag, model)
			break
		}
	}
	return append(append([]string(nil), command...), args...), nil
}
