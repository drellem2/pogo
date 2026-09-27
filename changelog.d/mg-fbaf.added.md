- **`pogo agent env` lists every environment variable pogod injects into a
  polecat, in injection order, and which shipped prompts read each (mg-fbaf).**
  On one night `POGO_WORKER_CORES` was believed not to exist. Then a second
  reader, who had just found that it does, believed nothing consumed it. That
  reader was quoting the `pogo host load` line that named the consumer ("…and
  prompt prose") while asserting the opposite. The answer was spread across
  help text, doc comments and six templates, and no command could be asked.
  Now each variable (`POGO_AGENT_NAME`, `POGO_AGENT_TYPE`, `POGO_PROCESS_NAME`,
  `POGO_AGENT_PROMPT`, `POGO_SUBMIT_RECEIPT`, `POGO_WORKER_CORES`,
  `POGO_HOST_CORES`, `POGO_ROLE`) prints with what it is for, when it is set,
  and, on a separate `but:` line, what it does not buy. `--json` carries the
  same data plus a `not_covered` list.
  - **One list, not two.** `internal/agent/workerenv.go` is a single catalogue.
    The spawn path and the restart path now assemble their env through it, so a
    variable cannot be injected without being reported.
  - **"read by" is computed, not stated.** It is a scan of the prompt corpus
    embedded in the binary, so it cannot go stale against the templates. An
    empty result prints as "no shipped prompt mentions it (this searched
    shipped prompts only)", never "nothing".
  - **The bounds print with the output.** The list does not cover what the worker
    inherits from pogod's own environment, a dispatcher's
    `spawn-polecat --env`, or consumers in Go code, in user-edited prompts or in
    other repos. The two core-budget values come from the running daemon. With
    no daemon they read UNKNOWN, and the rest of the report still answers.
  - **`pogo host load`'s worker-budget footer was split up.** "Advisory, nothing
    enforces it", "it reaches a worker as `$POGO_WORKER_CORES`" and "which
    prompts read it: `pogo agent env`" are now three separate lines. A reader who
    takes only the emphatic line still learns the variable exists.

  This landed as a rescue commit (mg-51bf) with no changelog entry. The entry
  was added afterwards under mg-f34e, which built the merged code and ran it
  against the live daemon before writing this.
