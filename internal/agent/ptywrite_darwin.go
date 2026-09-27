package agent

// ioctlInputPending reads a tty's unread input byte count: darwin's FIONREAD,
// _IOR('f', 127, int). x/sys/unix does not export it for darwin; the value is
// the one <sys/filio.h> defines and Python's termios.FIONREAD reports here.
const ioctlInputPending = 0x4004667f
