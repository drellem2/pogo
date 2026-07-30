- **The shipped prompts stop asserting that `mg edit` has no append subcommand,
  so agents stop being routed onto the wholesale body rewrite that
  `--append-body-file` exists to prevent (mg-4bb9).**
  `internal/agent/prompts/mayor.md`, `prompts/pm/pm-template.md` and
  `prompts/crew/doctor.md` all carried the same sentence, verbatim, in their
  ticket-lifecycle sections: *"`mg edit <id> --body="<new body>"` replaces the
  body wholesale — there is no append/comment subcommand. To leave a note for a
  future actor without rewriting the body, mail them."*

  Both halves were wrong, and each was wrong in a way that had already caused
  damage.

  `--append-body` and `--append-body-file` exist, and `mg edit --help` opens
  with a banner naming the second as the instrument for adding to a body:
  **ADDING TO A BODY? USE `--append-body-file`, NOT `--body-file`.** Nobody read
  that banner, because the prompt had already answered the question. That is the
  reason this matters more than a stale flag list: **a confident false claim in
  a prompt is worse than silence, because silence sends you to `--help` and a
  wrong answer stops you looking.** The prompts now say so in as many words.

  The recommended alternative was the second failure. Putting a note for a
  future actor in *mail* rather than the body is a diagnosed defect in its own
  right, and two items needed exactly that repair in one night — both of which
  now carry the repair in their own bodies. mg-8a12's real scope went by mail,
  leaving a body that carried a prohibition and no positive scope until one was
  appended under a "POSITIVE SCOPE" heading. mg-ddf4's strongest evidence went
  by mail and was appended afterwards under a heading that states the diagnosis
  outright: *"This was in mail and not in the ticket, which is the same defect
  the ticket is about."* A polecat reads the ticket, not the coordinator's
  outbox.

  The replacement text gives the append with a quoted heredoc as the default
  form, and pins the three properties that make it the safer operation — each
  one a distinct rewrite failure it does not have:

  - It **composes against the body on disk at write time**, so it cannot destroy
    a section another writer stored between your read and your write. mg-f326 is
    three agents doing exactly that to each other in two hours, every write
    exiting 0.
  - It **lands below the prose, so it can never author the body's leading `# `
    heading** — and that heading *is* the title, the only place a title is
    stored. An append therefore cannot rename an item, which is the mg-bac6
    hazard in both of its directions.
  - It is **exempt from the workflow-tag refusal** on an item that already
    carries the tag, where a full rewrite is not.

  `--body-file` is reserved rather than forbidden: when a full rewrite genuinely
  is the shape, the prompts now show it naming the version it read
  (`mg show <id> --body-hash` into `mg edit --if-unchanged=`), which refuses with
  exit 4 instead of overwriting a change the author never saw. They also record
  that `mg show <id> --body` does **not** exist — mg-9fc8 is the incident where
  that flag's usage error was captured into a file and stored *as* the body — and
  give `mg show <id> --json | jq -r .body` as the read that works. Mail keeps the
  job it is still right for: a note that does not belong in the body at all.

  Verified by use before the text was written, not by reading `--help`:
  `mg edit mg-4bb9 --append-body-file -` grew that item's body from 109 to 131
  lines with its `# ` heading count still 1, its title and assignee untouched,
  exit 0, and no body backup written (an append overwrites nothing, so there is
  nothing to back up).

  One claim from the ticket was **dropped rather than shipped**: that the
  read-modify-writes produced a duplicate `# ` heading on mg-78c0 / mg-78d2 /
  mg-a3d4 when `--body-file` re-prepended the stored title. It does not
  reproduce against the current binary — a rewrite with a matching leading
  heading, with `--title`, and with no heading at all each yield exactly one
  `# ` heading, and all three items presently have one. It may well have
  happened; it is not a mechanism this change can demonstrate, so it is not
  asserted in three shipped prompts. The rest of the recorded cost (roughly a
  dozen full-body rewrites in one night, each with a hand-written guard) is
  attributed to the count on mg-4bb9 rather than restated as measurement.

  `prompt_test.go` pins the affordance and all three properties in the three
  prompts, pins the *absence* of both halves of the old claim, and pins the
  scope: no `templates/` worker prompt mentions `mg edit` at all, so a worker
  gaining one means this change's boundary — and mg-61f4's, which measured the
  same boundary — needs revisiting deliberately. Verified by falsification: with
  the prompt edits stashed, the test reports 57 failures.

  No behaviour change. `mg` is a separate tool and is untouched; this is prompt
  text and its regression test.
