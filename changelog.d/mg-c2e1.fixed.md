- **`mayor.md` still told the coordinator to archive completed work with
  `mg archive --days=0` — estate-wide, gate-blind, and obsolete since
  2026-07-21.** The mass form existed for exactly one reason: the single-id
  `mg archive <id>` used to silently no-op. That was fixed and verified live on
  2026-07-21 (`mg archive mg-e0ca` and `mg archive mg-b399` each took exactly
  their own item, with all three live `gh-issue` carriers untouched before and
  after), so the justification was spent a month ago and only the blast radius
  was left. The cleanup step now runs `mg archive <id>`, gated on the
  {{.Worker}} having exited (`pogo agent list | grep "work-item=<id>"` empty)
  and the item reading `done`.

- **"Unfiltered" means the whole estate, and the coordinator's own items are
  the ones LEAST likely to be in it.** `mg archive --help` says the sweep
  "archives ALL done items, including any you are keeping deliberately."
  Measured 2026-08-19 with `mg archive --days=0 --dry-run`: **4 items would
  have been archived and not one of them was mayor's** — `mg-13e6` and
  `mg-15d4` (architect, `~/.claude`), `mg-872c` (pm-onethird,
  `~/research/onethird_program`), and `mg-d172`, a request Daniel had filed
  that same morning. mayor archives its own items as it closes them, so what
  actually accumulates in `done/` is other agents' work.

- **The sweep cannot see a workflow gate — structurally, not by omission.**
  Carrier state is pogod's parse, not mg's: `mg show --json` has no `workflow`
  key and no `stage` key at all (23 keys, neither among them), while pogod's
  `GET /workitems` reports `mg-3050` as `workflow=gh-issue stage=gated` — both
  measured 2026-08-19. `mg archive` is mg, so the gate is absent from the data
  model the sweep reads and no care at the callsite can restore it. It has
  eaten live `gh-issue` gate carriers twice. Two guards *do* exist and refused
  3 of the 7 done items in that same dry-run (a done `design` with no
  successor; anything tagged `blocked-on-*`) — neither is a gate check, and
  meeting one is what makes the sweep look guarded.

- **Filed rather than left to practice, because the protection was a property
  of the running session.** mayor had been using the per-id form all day —
  from a recalled memory note, not from the prompt, which still said otherwise.
  A fresh session reads `mayor.md` and does what it says, and mayor is
  restarted routinely (06:54Z that morning). This is the second half of the
  pair architect ruled on in mg-e52c; the first half landed as `cad63fe`.

- **Found in the same pass: §Work item archival asserted the opposite, and had
  been false for 146 days.** It said "Once a ticket's code is merged, the
  refinery archives the work item automatically — no action needed from you."
  The refinery's auto-archive call was removed on 2026-03-26 by mg-1f67 —
  deliberately, so completions stay visible long enough for the coordinator to
  act — and that paragraph was written on 2026-03-21 and never updated. It is
  what made the archiving step read as harmless catch-up rather than the only
  thing that ever runs. The section now says the coordinator is the only
  archiver, for merged and unmerged work alike, and names the retracted claim
  so a reader can recognise the belief they are carrying.

- **`client.ArchiveMGDoneItems()` is dead code whose doc comment still claims
  the refinery calls it after every merge — and it wraps `mg archive
  --days=0`.** Zero callers, measured 2026-08-19; the call site was removed by
  mg-1f67 on 2026-03-26. Left in place (out of this ticket's scope, which is
  the shipped prompt) and noted in `mayor.md`, because re-wiring it would put
  the estate-wide sweep back into code where no session's judgement is in the
  path at all.

- **The coupling with `pm/pm-template.md` is now written down in both
  directions**, in the same spirit as `cad63fe`. That template's "Recently
  shipped" query reads `done` **or** `archived` precisely because this step
  moves completed items out of `done` within minutes of the close (mg-e52c);
  neither file said so. The filename is literal and not `{{.Coordinator}}.md`:
  the coordinator's NAME is configurable, its prompt FILE is frozen at
  `mayor.md` (mg-04ce).

- Pinned by `TestMayorArchivesByIdNotByMassSweep`, split the way mg-e52c's
  pin was and for the same reason — the prescription lives in the bash fences
  and the prohibition has to quote the forbidden form in prose to explain it.
  Its first version failed on the fix rather than on the defect, twice: the
  fence extractor missed mayor.md's indented fences, and the retracted-claim
  check scored the retraction as a relapse. Both are fixed and both are
  explained where they sit. Every assertion was checked to fail against the
  pre-change file.

- **The remedy was subject to the defect three times, and each catch is in the
  diff.** The new pin failed on the *fix* rather than the defect twice — its
  fence extractor missed `mayor.md`'s indented fences and swallowed prose into
  the is-a-command-prescribed check, and its retracted-claim check scored the
  retraction as a relapse, because a retraction has to quote what it retracts.
  The measurement named this fleet's roster and was rejected by
  `TestShippedPromptsNameNoPersonalFleetAgent` (mg-f04b); the item ids stay so
  the reading can be re-derived, the names are gone. And
  `TestMayorPromptTellsTheFilerTheItemLanded` bounded the cleanup list with
  `strings.Index(s, "mg archive --days=0")` — the string this change removes. It
  kept passing, but only because the prose that *forbids* the mass form still
  contains the literal 648 chars further down; the bound had stopped meaning
  "start of the cleanup list". Re-anchored to the list item itself.

- **This fix does not reach the running coordinator, and that is measured
  rather than predicted.** `pogo agent prompt install`, run from this branch's
  binary against a sandboxed copy of the live `~/.pogo/agents/mayor.md`, returns
  a **conflict**: it writes `mayor.md.dist` and leaves the canonical file — with
  its `mg archive --days=0` — untouched. The deployed file's body hash is
  `9627ce51…` against a stamp recording `e20f8f69…`, so the install matrix
  classifies it user-edited; it carries two in-place edits on top of the
  `938c4c5` embed. mg-bb99/`0956abd` put both lessons upstream, so no content is
  lost — but the matrix gates on **hashes, not semantics**, so the
  divert-to-`.dist` hazard the ticket believed already resolved is live.
  `pogo agent prompt install --force` reconciles it and writes
  `mayor.md.bak.<timestamp>` first; both were proved in the sandbox and the live
  file was hash-checked untouched before and after. This could not be written
  into `mayor.md` itself — a note about a blocked install cannot be read until
  the install it is about has run.
