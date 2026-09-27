package agent

import "golang.org/x/sys/unix"

// ioctlInputPending reads a tty's unread input byte count (FIONREAD on
// Linux, which x/sys spells TIOCINQ).
const ioctlInputPending = unix.TIOCINQ
