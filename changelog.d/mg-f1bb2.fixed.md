- **`pogo check-staleness` no longer calls an org template's current prompts
  "superseded", and `pogo doctor --check` no longer calls a rewritten prompt
  "up-to-date".** The prompt reference is now repo + ref + subtree: set it with
  `--repo`/`--ref`/`--subtree`, or with a new `[lineage]` block in config.toml
  (`prompt_repo`, `prompt_ref`, `prompt_subtree`). pogod's `[prompt_stale]` sweep
  reads the same block. The report now says prompts "differ from the reference"
  and names that reference. Line counts read "N more/fewer lines than ref". The
  "Every agent reading these is running a superseded prompt" line is gone.
  If no lineage is declared and the installed tree has files the reference does
  not ship, the verdict is hedged ("the reference may not be this corpus's
  upstream"), no install is suggested, and the sweep logs the result instead of
  mailing it. Upgrade note: a host that tracks drellem2/pogo and keeps local
  prompt files will stop getting staleness mail until it declares
  `[lineage] prompt_subtree = "internal/agent/prompts"`. Doctor's prompt check
  now hashes the body even when the stamp's embed hash matches, and reports a
  mismatch as a hand-edited prompt (drellem2/pogo#125, mg-f1bb2).
