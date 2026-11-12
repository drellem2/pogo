package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/events"
)

// promptSynthFor returns the path respawn should re-synthesize a crew prompt
// into: the PromptFile startCrewAgent launched with, when that file is a
// synthesized copy of source. It returns "" when there is no source (not a
// crew start) or when PromptFile IS the source — re-synthesizing "into" the
// source would overwrite the operator's file.
func promptSynthFor(source, promptFile string) string {
	if source == "" || promptFile == "" || promptFile == source {
		return ""
	}
	return promptFile
}

// refreshRespawnPrompt rebuilds a crew agent's launch prompt from its current
// source before a respawn re-execs it (drellem2/pogo#306), and returns the
// command and prompt path to launch with.
//
// Before this, respawn re-ran the original argv, which names the prompt by
// path, against a synthesized-prompt.md nothing had rewritten since the
// agent's first start. A source edit made after that start was therefore
// invisible across every crash restart and crew-reset in between.
//
//   - No source (polecats, generic POST /agents): unchanged.
//   - Synthesis succeeds: the freshly written synthesized copy.
//   - Synthesis fails: logged loudly and evented, and the agent launches on
//     the SOURCE file — never on the stale synthesized copy.
//   - Synthesis fails and the source itself is gone: the respawn is refused.
//     A deleted prompt is a retirement, and relaunching on the old copy is
//     exactly the defect this exists to stop.
func refreshRespawnPrompt(name, source, synth, prev string, command []string) ([]string, string, error) {
	if source == "" {
		return command, prev, nil
	}
	next := source
	if synth != "" {
		out, err := SynthesizeExtendsPrompt(source, synth)
		switch {
		case err != nil:
			log.Printf("ERROR: agent %s: could not regenerate %s from %s before respawn: %v — "+
				"launching on the source prompt instead of the stale synthesized copy", name, synth, source, err)
			events.Emit(context.Background(), events.Event{
				EventType: "crew_prompt_regen_failed",
				Agent:     "crew-" + name,
				Details: map[string]any{
					"source":      source,
					"synthesized": synth,
					"error":       err.Error(),
				},
			})
			if _, serr := os.Stat(source); serr != nil {
				return nil, "", fmt.Errorf("agent %q: prompt source %s unusable (%v) and synthesis failed (%v); "+
					"refusing to respawn on the stale synthesized copy", name, source, serr, err)
			}
		case out != "":
			next = out
			log.Printf("agent %s: regenerated %s from %s before respawn", name, out, source)
		}
	}
	if next == prev {
		return command, next, nil
	}
	rewritten := make([]string, len(command))
	for i, arg := range command {
		rewritten[i] = strings.ReplaceAll(arg, prev, next)
	}
	return rewritten, next, nil
}
