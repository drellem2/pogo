synthwatch, refusal-watch and turn-watch can now be switched off:
`[synth_watch]`, `[refusal_watch]` and `[turn_watch]` each take an `enabled` key,
default `true`, so a config that does not mention them behaves as before
(drellem2/pogo#185). The three were armed unconditionally and were not referenced
from `internal/config` at all, while every sibling watcher had a switch.

`[synth_watch] enabled = false` is **page-only**: it stops the scan that pages
`human` and leaves the respawn gate in place, so an agent failing every turn is
still not restarted — dropping the gate would re-open mg-18d0's restart loop.
Each disabled detector logs a `NOT armed (config: [x] enabled = false)` line at
startup, and synthwatch's says the respawn gate is still active.
