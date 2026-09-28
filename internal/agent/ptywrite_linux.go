package agent

import (
	"time"

	"golang.org/x/sys/unix"
)

// ioctlInputPending reads a tty's unread input byte count (FIONREAD on
// Linux, which x/sys spells TIOCINQ).
const ioctlInputPending = unix.TIOCINQ

// inputLandingWindow is how long a Linux master write may sit in the pty's
// flip buffer before a kworker pushes it where FIONREAD counts it. Until the
// queue is seen non-empty, or this passes, a zero FIONREAD is not evidence the
// harness read anything (see waitInputDrained). The push is normally
// sub-millisecond; the window is the margin for a loaded box, and it costs a
// piece at most this much when the harness reads faster than the 2ms poll can
// see the bytes land.
const inputLandingWindow = 50 * time.Millisecond
