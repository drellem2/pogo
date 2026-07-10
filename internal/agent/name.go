package agent

import (
	"errors"
	"fmt"

	"github.com/drellem2/pogo/internal/config"
)

// ErrInvalidAgentName wraps every reason a name cannot be spawned. The API
// handlers test for it with errors.Is and answer 400 rather than 409/500.
var ErrInvalidAgentName = errors.New("invalid agent name")

// ValidateAgentName rejects names pogod cannot fully serve.
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
// the surprise this check exists to remove. Spawn also treats a permanent bind
// failure as fatal, so a name that passes here and still cannot bind — should
// the reservation arithmetic ever drift from sun_path — fails the spawn instead
// of silently losing attach (mg-ef80).
//
// The comparison is on bytes, not runes: sun_path is a byte budget.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidAgentName)
	}
	if len(name) > config.MaxAgentNameLen {
		return fmt.Errorf("%w: %q is %d bytes, over the %d-byte limit; the agent's attach socket path must fit the AF_UNIX sun_path limit",
			ErrInvalidAgentName, name, len(name), config.MaxAgentNameLen)
	}
	return nil
}
