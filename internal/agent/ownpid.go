package agent

import "os/exec"

// OwnPIDEnv is the agent process's OWN pid, exported to it at exec (mg-4a941).
//
// mg reads it: `mg claim` / `mg reclaim` / `mg unshelve --claim` record
// $POGO_PID as the claim's owner, falling back to mg's own pid. Before this
// variable was set, a polecat's step-1 `mg reclaim` stamped the pid of the
// short-lived mg process itself, so every running polecat's claim was under a
// pid that was dead within a second: no claim pid named its worker, the
// orphaned_claim stall-watch's "claim pid dead" half was a constant, and a
// human reading claimed/*.md.<pid> was misled.
//
// mg also reads it as "running under pogo automation": with it set, `mg new`
// declines to auto-detect --repo from the cwd (the guard that keeps a polecat
// worktree path out of a filed item). That guard had never fired for an agent
// either, for the same reason.
const OwnPIDEnv = "POGO_PID"

// ownPIDSpec is the catalogue entry for POGO_PID. It is not in
// agentIdentitySpecs because its value is not known when cmd.Env is built —
// no process has been forked yet — so envAssignments cannot carry it.
// exportOwnPID assigns it instead, and PolecatEnv reports it.
var ownPIDSpec = envSpec{
	Name:        OwnPIDEnv,
	Placeholder: "<the agent process's own pid>",
	Why:         "the pid `mg claim`/`mg reclaim` record as the claim's owner, so a claim pid names its live worker; also tells `mg new` it runs under automation",
	Note:        "assigned by a /bin/sh exec trampoline after every other layer, so no --env or frontmatter can override it; it is the harness's pid, not the pid of a tool shell the harness starts",
}

// ownPIDTrampoline is the /bin/sh script exportOwnPID runs. $$ is the shell's
// pid, and `exec` replaces the shell with the harness in the SAME process, so
// the value is the harness's own pid — the pid pogod records as Agent.PID.
const ownPIDTrampoline = `POGO_PID=$$; export POGO_PID; exec "$@"`

// exportOwnPID rewrites cmd so the process it starts sees its own pid as
// $POGO_PID. Call it after cmd.Env is final and before the process starts.
//
// The program is the path exec.Command already resolved (cmd.Path), so the
// lookup still uses pogod's PATH exactly as before rather than the shell's.
// When the program is not runnable, cmd is left untouched: wrapping it would
// turn a spawn error into a started shell that exits 126/127, i.e. a spawn
// that "succeeds" and an agent that immediately crashes. Two checks, because
// exec.Command sets cmd.Err only for a bare name it could not find on PATH —
// an absolute path that does not exist (or is not executable) leaves cmd.Err
// nil and would otherwise fail only at exec, inside the shell.
func exportOwnPID(cmd *exec.Cmd) {
	if cmd.Err != nil {
		return
	}
	if _, err := exec.LookPath(cmd.Path); err != nil {
		return
	}
	args := append([]string{"sh", "-c", ownPIDTrampoline, "sh", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
	cmd.Args = args
}
