- **Upgrade note: on a host with no `[lineage]` block, pogod's prompt-staleness
  sweep may stop mailing, and `pogo doctor --check` warns on edited prompts.**
  When prompts differ from the reference *and* `~/.pogo/agents` holds files the
  reference does not ship (for example the `crew/pm-*.md` stubs), the sweep now
  logs the result and sends no mail, because the reference may not be the
  corpus's upstream. To keep the mail on a host that tracks drellem2/pogo, add
  `[lineage]` with `prompt_subtree = "internal/agent/prompts"` to config.toml.
  An org template should name its own `prompt_repo`/`prompt_subtree` instead.
  Separately, `pogo doctor --check` now shows a WARN row, "agent prompts
  up-to-date (body differs from stamp)", for a prompt whose body was edited in
  place under an unchanged install stamp. It used to report these files as
  up-to-date. The row is a warning and does not change doctor's exit status
  (drellem2/pogo#125, mg-f1bb2).
