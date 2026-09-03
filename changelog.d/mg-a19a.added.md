- **pogod now says when it is not writing its own log, and `pogo service log`
  answers "is this file a record of the running daemon?" before you grep it
  (mg-a19a).** Measured on the reference box 2026-09-03 20:53Z: pogod pid 6610,
  up 57h45m — holding the lock, serving `:10000`, delivering scheduled fires,
  running a refinery gate — with its parent Emacs rather than launchd, its fd 2
  on `/dev/ttys007`, no descriptor on `pogod.log`, and **zero lines ever written
  there.** Every diagnostic on the box that grepped `pogod.log` was reading a
  file some other process wrote, and nothing reported it for 44 hours.

  **The file did not look broken.** It was 8.9 MB of richly detailed,
  correctly-formatted daemon output. `launchctl print` reported `runs = 4639`
  for `com.pogo.daemon`: 4,639 spawns, each of which correctly refused the lock
  held by 6610 and exited 1, and the log's last 9,278 lines are those refusals.
  So the whole tail was written by processes that lived milliseconds. The
  ticket's framing — a second daemon at 01:05 whose failed start may have
  silenced 6610 — does not survive the reading: **there is no transition to
  explain.** 6610 never wrote a startup banner to that file, and 01:05:57 is
  simply when launchd stopped retrying.

  **Freshness is the wrong instrument and would have read green.** For the
  thirteen hours of that respawn loop the file the live daemon never touched had
  an mtime under ten seconds old, continuously. An instrument that returns the
  same answer under two different world-states is not evidence about either, so
  `internal/logliveness` compares **identity** — the descriptor the daemon's
  stderr actually points at, against the path the installed plist names — and
  carries mtime, size and start time as context printed under a line saying what
  they do not prove. That mtime is not a verdict input is pinned by test.

  **What ships.** `pogo service log` (exit 0 LIVE / 1 DETACHED / 3 UNKNOWN, and
  UNKNOWN is not a pass) for anyone about to grep; a `pogod_log_not_written`
  condition raised at startup **and on every heartbeat tick**, because a
  startup-only detector would have been silent for the whole 57-hour episode —
  that daemon was detached from its first instant and never restarted;
  `service.InstalledLogPath`, which reads the redirect out of the plist on disk
  rather than deriving it from this build's template; and the
  existence-is-not-liveness paragraph in the mayor prompt, next to the one that
  already covered a missing file.

  **Why the alarm is mail and an event rather than a log line.** Every other
  pogod condition logs correctly to a file nobody reads. This one would log
  correctly to a file that is not a record at all — the notice that the log is
  not being written, written to the log that is not being written. It goes to
  the coordinator's mailbox and onto the durable event spine, both of which
  survive the fault being reported.

  **The measured cost of having none of this.** On 2026-09-03, mid-incident, an
  architect grepped that file for a fleet-stop window, found 51
  `cause=modal_wedge` lines and 1,875 `ANIMATING BUT NOT WORKING` lines, and
  built a specific, mechanically plausible root cause on them. Every one of
  those lines predated the window by days; the real cause was an entitlement
  refusal (mg-6616). A frozen log does not look empty — it silently answers
  questions about periods it does not cover, and renders "the record is absent"
  identically to "the record is negative".

  Related: `pogo service supervision` (mg-fa79) already reported `UNSUPERVISED`
  on this box throughout, and nobody ran it. Same displacement, different
  question — nobody about to run `grep refinery: "$log"` has a reason to first
  ask launchd about supervision, which is why this is a separate instrument
  rather than a note on that one.
