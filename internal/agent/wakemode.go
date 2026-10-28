package agent

import (
	"fmt"
	"strings"
	"sync"
)

// Declared wake mode (drellem2/pogo#232, #236).
//
// An agent is woken to read its mail in one of two ways: a mail-check-<name>
// schedule fires into its PTY on a timer, or wakewatch sends it a pointer nudge
// when mail lands (mg-5496). diagnose, deafwatch and check-mailloops judge the
// first — "has no mail-check schedule" is health=no_mail_loop — and until this
// file they had no way to know an agent was meant to be woken the second way.
// An operator who took a seat off its timer on purpose got a permanent RED, a
// "Restore it:" recipe, and a deafwatch page every six hours.
//
// The answer is a DECLARATION, not an inference. The removal event
// (schedule_removed reason=explicit_rm) was the obvious key and the wrong one:
// `pogo agent park` emits it too, any `schedule rm` does, and it ages out of the
// rotated events.log — a suppressor that fails quiet. A declaration is durable,
// auditable, and visible: a declared agent is still LISTED by every reader,
// under not-judged with ExclusionPointerWake, so it cannot hide a real fault
// behind a green.
//
// One key, two places, one switch:
//
//	prompt frontmatter   wake = "pointer" | "mail-check"   (per agent)
//	config.toml          [agents] wake = "pointer" | "mail-check"   (install default)
//
// Frontmatter overrides the config default in BOTH directions. This is the
// single key wakewatch phase 3 (mg-a2ff) flips for crew; do not add a second.
//
// What a pointer-mode agent IS judged on — unconsumed work (wake_unconsumed) —
// is mg-a2ff's job. Until then diagnose shows the last wake_unconsumed as
// context only, never as a Health input.

// WakeMode is how an agent is woken to read its mail.
type WakeMode string

const (
	// WakeMailCheck: a mail-check-<name> schedule wakes the agent. The
	// default, and the mode diagnose/deafwatch/check-mailloops judge.
	WakeMailCheck WakeMode = "mail-check"
	// WakePointer: wakewatch's pointer nudges wake the agent; it is owed no
	// mail-check schedule, and is not judged on having one.
	WakePointer WakeMode = "pointer"
)

// ParseWakeMode validates a declared value. Whitespace is trimmed; anything
// else must match exactly.
func ParseWakeMode(s string) (WakeMode, bool) {
	switch WakeMode(strings.TrimSpace(s)) {
	case WakeMailCheck:
		return WakeMailCheck, true
	case WakePointer:
		return WakePointer, true
	}
	return "", false
}

// WakeModeHow is the sentence an operator reads to learn how to declare
// pointer wake. One copy, so diagnose and the reports cannot drift.
const WakeModeHow = `wake = "pointer" in its prompt frontmatter, or [agents] wake = "pointer" in config.toml for the whole install`

// Source labels for WakeDeclaration.Source's tiers.
const (
	wakeSourceConfig  = "config.toml [agents] wake"
	wakeSourceDefault = "default"
)

// WakeDeclaration is an agent's effective wake mode and WHERE it came from.
// Source is what diagnose prints in parentheses — the declaration has to be
// traceable to the line that made it, or it is a mute button nobody can find.
type WakeDeclaration struct {
	Mode   WakeMode `json:"mode"`
	Source string   `json:"source"`
}

var (
	defaultWakeMu  sync.RWMutex
	defaultWakeRaw string
)

// SetDefaultWakeMode sets the process-wide install default from config
// ([agents] wake). Empty means "not set": the built-in mail-check applies. An
// unrecognised value is KEPT rather than dropped, so ResolveWakeMode can name
// it in the source instead of reading as an unset default; the returned error
// is for the caller to log at startup.
func SetDefaultWakeMode(raw string) error {
	defaultWakeMu.Lock()
	defaultWakeRaw = strings.TrimSpace(raw)
	defaultWakeMu.Unlock()
	if raw == "" {
		return nil
	}
	if _, ok := ParseWakeMode(raw); !ok {
		return fmt.Errorf("[agents] wake = %q is not %q or %q; agents without a frontmatter declaration are judged as %s",
			raw, WakeMailCheck, WakePointer, WakeMailCheck)
	}
	return nil
}

func defaultWakeMode() string {
	defaultWakeMu.RLock()
	defer defaultWakeMu.RUnlock()
	return defaultWakeRaw
}

// ResolveWakeMode returns name's effective wake mode. Precedence, first
// declaration wins:
//
//  1. the agent's own prompt frontmatter (for an `extends` stub, the stub —
//     the operator-editable file — and then its template, the same order
//     ResolveRestartOnCrashWithStub uses);
//  2. config.toml [agents] wake;
//  3. mail-check.
//
// An UNRECOGNISED value at any tier stops resolution there and returns
// mail-check, with the source naming the bad value. That is the loud
// direction: a typo'd "pointer" leaves the agent judged, so diagnose says
// no_mail_loop and the source line says why the declaration did not take —
// rather than a typo'd "mail-check" silently falling through to a pointer
// default.
//
// A prompt tree or frontmatter that cannot be read is returned as an error
// alongside the config/default answer. Callers that judge (mailLoopExclusionFor)
// treat that answer as authoritative only when it says mail-check, which is
// the side that still judges.
func ResolveWakeMode(name string) (WakeDeclaration, error) {
	path, err := configuredPromptPath(name)
	if err == nil && path != "" {
		decl, declared, ferr := frontmatterWake(path)
		if ferr != nil {
			err = ferr
		} else if declared {
			return decl, nil
		}
	}
	return configWake(), err
}

// configWake is tiers 2 and 3.
func configWake() WakeDeclaration {
	raw := defaultWakeMode()
	if raw == "" {
		return WakeDeclaration{Mode: WakeMailCheck, Source: wakeSourceDefault}
	}
	if m, ok := ParseWakeMode(raw); ok {
		return WakeDeclaration{Mode: m, Source: wakeSourceConfig}
	}
	return WakeDeclaration{Mode: WakeMailCheck,
		Source: fmt.Sprintf("%s = %q is unrecognised; judged as %s", wakeSourceConfig, raw, WakeMailCheck)}
}

// frontmatterWake reads tier 1 from path, following one `extends` hop.
func frontmatterWake(path string) (WakeDeclaration, bool, error) {
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		return WakeDeclaration{}, false, err
	}
	if meta.HasField("wake") {
		return declFromFrontmatter(meta.Wake, path), true, nil
	}
	m := extendsDirectiveRE.FindStringSubmatch(body)
	if m == nil {
		return WakeDeclaration{}, false, nil
	}
	tmplPath, _ := extendsPaths(m[1], m[2])
	tmeta, _, err := ParsePromptFrontmatter(tmplPath)
	if err != nil {
		return WakeDeclaration{}, false, err
	}
	if tmeta.HasField("wake") {
		return declFromFrontmatter(tmeta.Wake, tmplPath+", via extends"), true, nil
	}
	return WakeDeclaration{}, false, nil
}

func declFromFrontmatter(raw, where string) WakeDeclaration {
	if m, ok := ParseWakeMode(raw); ok {
		return WakeDeclaration{Mode: m, Source: "prompt frontmatter " + where}
	}
	return WakeDeclaration{Mode: WakeMailCheck,
		Source: fmt.Sprintf("prompt frontmatter %s: wake = %q is unrecognised; judged as %s", where, raw, WakeMailCheck)}
}

// configuredPromptPath finds the crew/mayor prompt for identity by the same
// matching ConfiguredStateFor uses. "" with a nil error means no prompt.
func configuredPromptPath(identity string) (string, error) {
	if identity == "" {
		return "", nil
	}
	cands, err := autoStartCandidates()
	if err != nil {
		return "", err
	}
	bare := strings.TrimPrefix(identity, "crew-")
	for _, c := range cands {
		if c.name == identity || c.name == bare {
			return c.path, nil
		}
	}
	return "", nil
}

// pointerWakeExclusion is the pointer-mode arm of mailLoopExclusionFor. A read
// error means the declaration could not be seen, and the agent stays judged:
// an unreadable declaration must not silence a RED.
func pointerWakeExclusion(name string) MailLoopExclusionReason {
	decl, err := ResolveWakeMode(name)
	if err == nil && decl.Mode == WakePointer {
		return ExclusionPointerWake
	}
	return ""
}
