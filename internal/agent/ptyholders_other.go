//go:build !linux

package agent

// ptySlaveHolders is a no-op off Linux (mg-c7a0d). There is no /proc to walk,
// and on darwin the case it diagnoses does not arise the same way: the session
// leader's exit revokes the tty, so a straggler that kept the slave cannot keep
// the master's reader blocked. uninspected=-1 says "not looked", so the log
// line cannot be read as "looked and found nobody".
func ptySlaveHolders(slavePath string) (holders []string, uninspected int) {
	return nil, -1
}

const ptyHolderScanSupported = false
