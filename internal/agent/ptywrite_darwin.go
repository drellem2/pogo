package agent

import "time"

// ioctlInputPending reads a tty's unread input byte count: darwin's FIONREAD,
// _IOR('f', 127, int). x/sys/unix does not export it for darwin; the value is
// the one <sys/filio.h> defines and Python's termios.FIONREAD reports here.
const ioctlInputPending = 0x4004667f

// inputLandingWindow is zero on darwin: a master write is in the slave's input
// queue by the time write() returns, so FIONREAD == 0 means it has been read.
const inputLandingWindow time.Duration = 0
