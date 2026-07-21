- **The `commit-msg` hook's degrade path no longer promises a backstop that is itself deploy-gated
  (mg-42ed).** When no route to `pogo check-commit-body` exists, the hook warns and passes — and it
  used to close that warning with *"The refinery runs the same detector on merge and still gates it
  there."* That sentence was stated unconditionally, and it is not unconditionally true: the gate
  (`internal/refinery/closingref_gate.go`) landed in `4866a26` but **executes inside pogod**, so it
  gates nothing until the running pogod is redeployed past that commit.

  The irony is the reason to fix it rather than shrug: the warning fires *because* the deploy state
  is stale, and then reassures the reader with a guarantee that is stale *for the same reason*. A
  path that correctly says "nothing was checked" and then adds "but you're covered" is worse than
  one that stops after the first half.

  The reassurance is now **conditional, not deleted** — the gate is real code and will be the
  guarantee once deployed, so the text names the condition (`only once the RUNNING pogod is past
  4866a26`) and reads as true in both deploy states. It also points out that the probe failures
  printed directly above are themselves evidence the deploy is stale, so the later catch should be
  treated as unproven. The same unconditional claim in the file's header comment was rewritten the
  same way.

  Exercised, not just reasoned about: the degrade path is near-unreachable normally, because the
  capability probe falls through to `go run ./cmd/pogo` and needs only the Go toolchain. It was
  reached by running the hook under a `PATH` containing `git` and nothing else, with the exit code
  read directly rather than through a pipe — `cmd | head` reports `head`'s status, and an absence
  reads as a pass. Both working arms were re-measured unchanged: a benign `Refs drellem2/pogo#89`
  body exits 0 with `commit-msg: ok`, and the real `e83f394` body exits 1 still naming
  `line 7→8 … (WRAPPED)`.
