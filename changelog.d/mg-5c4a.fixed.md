A deploy killed mid-flight no longer leaves the fleet unable to dispatch. The
drain restore was disarmed on the line *above* `launchctl kickstart -k`, on the
premise that the kickstart kills the daemon whose flag the deploy set — true of
a kickstart that lands, false of one that hangs. On four consecutive nights
(2026-09-04..07) it hung for three and a half hours, the run deadline killed the
deploy, and `draining=true` was left set on a pogod that was up and answering:
no polecat dispatched, fleet-wide, until a human read `/agents/drain` by hand.
The disarm now waits for the kickstart to return.

A killed run also used to file itself as a success. `trap - INT TERM` ran on the
same line, so the shell took an untrapped SIGTERM — bash still runs the EXIT
trap, but hands it `$? = 0`, so all four nights wrote `exit=0` with an empty
`reason=` into their own reason records. Signals now convert to an exit for the
whole run, so the record carries the real code and names the signal.

`pogo server status` reports the dispatch flag (`draining=false`, `draining=TRUE`
with the POST that clears it, or `draining=unreported` for a daemon that predates
the field), and the nightly runner reads it on its way out and writes a
`dispatch:` line for every attempt. Nothing reported it before.

The runner's SIGINT/SIGTERM messages no longer assert that "dispatch was restored
on the way out and nothing was installed" — an outcome they never observed, and
which was false of every one of those four nights.
