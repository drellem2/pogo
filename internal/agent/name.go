package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/config"
)

// ErrInvalidAgentName wraps every reason a name cannot be spawned. The API
// handlers test for it with errors.Is and answer 400 rather than 409/500.
var ErrInvalidAgentName = errors.New("invalid agent name")

// validNameComponent reports whether name is safe to path-join as a single
// directory or file-name component.
//
// An agent name is joined straight onto three different roots — the attach
// socket at "<socket dir>/<name>.sock", the agent's state directory at
// "<prompt dir>/<name>", and a polecat's worktree at "<polecats dir>/<name>" —
// and filepath.Join resolves "..", so an unchecked name of "../x" places the
// socket, the state dir and the worktree outside the root that was meant to
// contain them. The bare ".." is equally unsafe: it resolves the state dir to
// the prompt dir's own parent.
//
// A component must therefore be non-empty, must not be a directory-traversal
// token, and must contain no separator — either flavour, so a Windows-style
// "..\\" cannot slip through a unix build's checks.
//
// Control characters are rejected for a second, non-path reason: a name is not
// only a path component, it is also a field value in pogod's logs and in the
// JSON the API echoes back, and a name carrying a newline forges a log record.
// Every C0 control (NUL, CR and LF among them) and DEL is invalid. This mirrors
// macguffin's mail path-component gate (mg-ea5a, mg-21a6), which refuses the
// same token set for the same two reasons.
func validNameComponent(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidateAgentName rejects names pogod cannot fully serve.
//
// A name must be a single safe path component: no separators, no "." or "..",
// no control characters. See validNameComponent for why each root that the name
// is joined onto depends on that (mg-edb2).
//
// The length ceiling is config.MaxAgentNameLen. An agent's attach socket lives
// at "<socket dir>/<name>.sock", and AgentSocketDir chooses that directory by
// reserving exactly MaxAgentNameLen bytes for the name against the AF_UNIX
// sun_path limit. A longer name overruns the reservation, bind fails, and the
// agent runs with `pogo agent attach` permanently unavailable against it.
//
// Rejecting the name here rather than at bind time makes the ceiling a property
// of the name alone: a name either works under every POGO_HOME or fails under
// all of them. Enforcing it only where it actually overflows would make spawn
// succeed or fail based on how deep the operator's root happens to be, which is
// the surprise this check exists to remove.
//
// That equivalence rests on config.AgentSocketDir always reserving
// MaxAgentNameLen bytes for the name, whatever the root or TMPDIR. Spawn also
// treats a permanent bind failure as fatal, so a name that passes here and still
// cannot bind — should the reservation arithmetic ever drift from sun_path —
// fails the spawn instead of silently losing attach (mg-ef80).
//
// The comparison is on bytes, not runes: sun_path is a byte budget.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidAgentName)
	}
	if !validNameComponent(name) {
		return fmt.Errorf("%w: %q must be a single path component with no separators, control characters or %q",
			ErrInvalidAgentName, name, "..")
	}
	if len(name) > config.MaxAgentNameLen {
		return fmt.Errorf("%w: %q is %d bytes, over the %d-byte limit; the agent's attach socket path must fit the AF_UNIX sun_path limit",
			ErrInvalidAgentName, name, len(name), config.MaxAgentNameLen)
	}
	return nil
}
