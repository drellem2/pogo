- **The anchored-`pkill` example stops naming a binary nothing is running, and
  stops recommending a form that cannot reach its own example target
  (mg-ce2c).**
  `internal/agent/prompts/mayor.md` closed its unanchored-`pkill` warning with
  the how-to-do-it-properly example:

      If you must kill a process directly, kill by PID (`kill "$PID"`) or anchor
      the pattern to the binary's full path (`pkill -f "^/usr/local/bin/pogod"`).

  `internal/agent/prompts/crew/doctor.md` carried the same sentence verbatim.
  Measured on 2026-07-30, that pattern matched nothing and exited 1 — for **two
  independent reasons**, and the second is why a corrected literal would not
  have been a fix.

  **1. The path was stale, and it fails the obvious check by passing it.**

      /usr/local/bin/pogod        EXISTS, mtime Mar 20 22:50   <- stale build
      which pogod                 /Users/daniel/go/bin/pogod
      running daemon (pid 57196)  /Users/daniel/go/bin/pogod

  This is worse than an ordinary dead path. A reader who verifies with `ls` gets
  a real executable with the right name, so the cheapest available check
  *confirms the wrong answer*. Only `which pogod` or reading the live process
  disagrees. Same shape as the defect mg-f766 was filed for — an instruction
  whose failure is indistinguishable from success at the point of use — except
  here the confirming evidence actively misleads.

  **2. pogod is unmatchable by `pkill` from any agent, at any path.** From `man
  pgrep`:

      -a   Include process ancestors in the match list.  By default, the current
           pgrep or pkill process and all of its ancestors are excluded.

  pogod spawns every crew agent and every polecat, so pogod is *always* an
  ancestor of the shell running the `pkill`. Measured: `pgrep -f .` — match
  anything — enumerated 889 of 907 processes and omitted exactly the caller's
  ancestor chain (this shell, its `claude` process, pogod at 57196, and
  `launchd`), while `pgrep -a -x pogod` returned `57196`. So the example was not
  merely mis-pathed: **no pattern whatsoever can express it**, and simply
  swapping in `~/go/bin/pogod` would have shipped an example that still silently
  does nothing. The audience for this instruction is precisely the set of
  processes that cannot execute it.

  **The failure mode.** `pkill` exits 1 on no match, but the instruction never
  said to read it, and an anchored `pkill` that matches nothing is
  indistinguishable from a daemon that was already down. An agent following this
  mid-incident believes it has killed pogod.

  **The obvious de-hardcoding fix is worse than the bug, so the guard is part of
  the change.** `pkill -f "^$(ps -o comm= -p "$PID")"` looks like the right
  answer — derive the path, don't assert it. But `ps` prints nothing for a pid
  that has already exited, the pattern collapses to `"^"`, and `"^"` matches
  everything: measured at **894 of 907 processes**, i.e. the whole fleet plus the
  user's session. A stale literal fails safe and kills nothing; the naive
  derivation fails catastrophically, and it does so precisely in the common case
  — chasing a process that is already gone. That is the exact disaster the
  surrounding paragraph exists to prevent, so both prompts now ship the guarded
  form:

      BIN=$(ps -o comm= -p "$PID")   # macOS: full path of a LIVE pid; empty once it exits.
                                     # Linux: readlink /proc/"$PID"/exe
      if [ -n "$BIN" ]; then
        pkill -f "^$BIN" || echo "matched nothing: already dead, or an ancestor of this shell"
      else
        echo "pid $PID is gone; there is nothing to pattern-match"
      fi

  Linux is answered rather than assumed, for the same reason mg-f766 answered it:
  `ps -o comm=` there prints the short command name, not a path, so the macOS
  recipe aimed at Linux would silently widen the anchor instead of narrowing it.

  `kill "$PID"` is now stated as the form to reach for — it has no pattern to get
  wrong, and against an ancestor like pogod it is the only form that works at
  all. doctor.md additionally gets the inverted reading it is most exposed to as
  the diagnostic agent: **an empty `pgrep -f pogod` is not evidence that pogod is
  down**; use `pgrep -a -f pogod` or ask pogod for the pid.

  The load-bearing half of the paragraph is unchanged and pinned: an unanchored
  `pkill -f "sleep 600"` kills the fleet's watchdog and mail pollers, which idle
  in exactly that command, and `pogo agent stop <name>` remains the way to stop
  an agent.

  `prompt_test.go` pins all three halves across **every** shipped prompt, so a
  copy-paste cannot reintroduce any of them: `/usr/local/bin/pogod` is struck
  outright; the defect *class* is struck via `pkill -f "^/` (the legitimate forms
  expand a variable first — `^{{.WorktreeDir}}/…` in the polecat templates, or a
  run-time-derived `^$BIN` — so neither trips it); and any prompt shipping
  `pkill -f "^$BIN"` must ship the `[ -n "$BIN" ]` guard with it. With the prompt
  edits reverted the test reports 18 failures. `./build.sh` is green, and
  `pogo check-prompts` exits 0.

  No behaviour change: prompt text and its regression test.
