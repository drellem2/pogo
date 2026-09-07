- **The merge gate now records the PID OF THE PROCESS THAT SENT the signal that
  kills its test step — the reading mg-3bd1 recorded as unrecoverable on darwin,
  which it is not (mg-cbc3).**

  Three merge gates on this host have been ended by a SIGTERM the refinery did
  not send (2026-08-19 at 178s, 2026-09-07 at 85s and at 264s). mg-3bd1 measured
  a great deal about them and closed with the sender unidentified, recording the
  reason as a dead end: darwin exposes a sending pid only to a handler installed
  with `SA_SIGINFO`, "which a shell cannot install and Go's `os/signal` does not
  expose", and there is no root here for dtrace.

  **Every clause of that is true and the conclusion does not follow.** A shell
  cannot install `SA_SIGINFO` and Go does not expose `siginfo_t` — but a
  compiled helper installs it in one call, needs no privilege, and `/usr/bin/cc`
  is on this box. Measured with the sender's pid known in advance and read out
  of the *sending* process rather than assumed: a signal from the calling shell
  (pid 52601) recorded `si_pid=52601`, and one from a distinct subshell that was
  neither the target's parent nor the caller (pid 52656) recorded `si_pid=52656`.
  The second is the control that matters — in the natural arrangement the sender
  IS the target's parent, so a program that printed `getppid()` passes every
  other test and fails that one. The sending pid was recoverable for all three
  occurrences and nothing was recording it.

  **Shipped:** `scripts/signal-sender.c` with the front-end
  `scripts/signal-sender.sh`, on the Go-test row **inside**
  `scripts/tmpdir-leak-guard.sh`. The position is the point and is why this is a
  second instrument rather than an edit to `signal-witness.sh`: the measured
  delivery shape bounds the signalled set above by the guard, and the witness
  sits outside it, so on all three occurrences the witness would have taken its
  AMBIGUOUS arm and learned nothing about a sender. The witness still owns the
  ancestor reading, which can only be taken from outside the signalled region.

  It compiles itself once per revision of its source, cached under the pogo
  state root; if it cannot build — no `cc`, a failed compile, a cached binary
  that fails its own no-argument self-check — it `exec`s the wrapped command,
  leaving the process chain byte-for-byte what it was before the instrument
  existed, and says so on stderr. An instrument must not be able to turn the
  gate red, and one that is off quietly has been off for months by the time
  anybody asks. Its stderr block caps each resolved `ps` line at 200 columns
  while the record file keeps them whole: an agent's argv here runs past a
  kilobyte and the refinery persists 8 KB of gate output, so nine ancestors
  printed in full would evict the failure text the block is attached to.

  **Four more candidates eliminated, each with its measurement** (in
  `docs/investigations/gate-sigterm-variable-elapsed-2026-09-07.md`): the
  nightly deploy's `kill_tree` — the shape match mg-3bd1 named — is ruled out
  for these three **on timing**, the deploy log showing activity only at 02:00
  and 05:30 on 2026-09-07 and none in the 17:00–20:30Z band on either day, with
  a positive control that the same grep does return runs; pogod did nothing
  fleet-shaped within ±90s of any of the three, with a positive control that an
  `agent_stopped` 87.9s earlier IS visible to the same instrument; the macOS
  unified log carries no `kill(2)` record at all, stated as a null instrument
  rather than as a negative finding; and `internal/agent`'s own SIGTERMs — the
  lead appended to the archived predecessor — are **tested and not supported**,
  because every signal in that package goes through a live `*os.Process` handle
  for an unreaped child, `Registry.StopWithCause` sends SIGINT rather than
  SIGTERM, and the recorded victims include `go test`'s own parents, which a
  test's signal to a process it spawned cannot reach.

  **New, and it changes what kind of sender to look for: this box recycles the
  entire PID space in minutes.** Read out of `launchd`'s spawn records for the
  minutes around the 20:08Z kill: 86,263 at 21:06:33 BST, 95,537 at 21:07:04,
  then **9,174** at 21:07:33 — wrapped past PID_MAX — and 19,024 at 21:08:13.
  292 to 469 pids per second; the whole ~100,000-pid space turned over once
  inside the 85 seconds the killed run lasted. (Measured again on a quiet box at
  23:32Z: 56/s, still a full wrap every ~30 minutes.) It dates the guard
  independently — pid 86690 places its creation at ≈20:06:40Z against the 85s
  the refinery recorded — and it means any construction that holds a pid and
  signals it later can produce exactly the recorded shape with no intent toward
  the gate at all. **That is a hypothesis with a mechanism and a supporting
  measurement, not an identification, and it is written down as such.**

  **The sender is still not identified.** What has changed is that the next
  occurrence names it instead of leaving it to be reconstructed.
