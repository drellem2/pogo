- **A ticket body is the contract only up to the spawn, and the three prompts
  whose agents edit other people's tickets now say so (mg-9ccc).** `spawn-polecat`
  takes the body as a `--body-file` snapshot and renders it into the worker's
  prompt file, so from that instant the worker holds a copy and never reads the
  item again. A body edit *before* dispatch is the durable way to change what the
  worker is told; a body edit *after* dispatch changes what a **human** reads and
  nothing about what the worker does. Nothing distinguishes the two at the call
  site: the edit exits 0, `mg show` renders the new text, and the worker proceeds
  on the old.

  pm-onethird found it on mg-409a — re-scoped at 14:05, dispatched moments
  earlier when its dependency cleared — and caught it only because it happened to
  notice the item was already `claimed` as it wrote. It mailed p409a directly,
  which was correct and required already believing the window exists. Both the
  coordinator and the PMs had spent the day writing orders, scope boundaries and
  do-not-do constraints into bodies on the assumption that they bind. They do,
  right up until the spawn.

  So the remedy is documentary, because the defect is a **false belief and not a
  missing mechanism** — the mail channel works and is the intended path. `mayor.md`,
  `pm/pm-template.md` and `crew/doctor.md` gain the same block, at the end of the
  `mg edit` body-write bullet mg-4bb9 installed (it is that bullet's expiry date).
  It names the dispatch tell — `claimed`, which is a fact rather than a guess only
  because mg-7254 made pogod claim at spawn — and the one-liner that turns an id
  into the agent name to mail, since `mg mail send` refuses a name it does not know
  (mg-d639) and a correction that says "mail the worker" without saying how to find
  it leaves the reader inventing one:

  ```bash
  pogo agent list --json | jq -r '.[] | select(.work_item_id=="<id>") | .name'
  ```

  Both of those one-liners can answer with an empty line for two different
  reasons, and the block says which is which — because "nobody to tell" read out
  of a failed lookup is this same defect one level down. `mg show` errors to
  **stderr** and exits 3, so `jq -r .status` prints nothing and still exits 0
  (`jq`'s status, not `mg`'s); `pogo agent list --json` errors to **stdout**, so
  the `jq` exits 5 instead of printing a name. Empty *and quiet* means what it
  says; empty *next to an error* means you never asked.

  After dispatch you do **both**: append to the body and mail that name. The mail
  is the only channel that reaches the worker; the body is the only record the
  next reader gets, and an unmailed body section reads later as though the worker
  had acted on it. `claimed` with no name is your own claim or a departed worker —
  nobody to tell, and the append is for the human record alone.

  **The mechanical option was declined on the ticket's own instruction, and the
  ruled-out one is pinned.** A warning at `mg edit` time is `mg`'s to add, not
  pogo's, and it would have to be a warning rather than a refusal — appending a
  coordinator ruling to a claimed item is legitimate and common. What is pinned
  here is the direction the ticket ruled *out*:
  `TestPromptsSayABodyEditAfterDispatchReachesNobody` fails if any `templates/`
  worker prompt starts reading
  its own item's body, which would make the body a live instruction channel and
  reintroduce the mid-flight scope change that stood a worker down. The pin is a
  rule, not a spelling list — a line that names the worker's own id via `mg show`
  *and* reaches for a body flag or the `.body` field — so a status grep and
  polecat-triage.md confirming its own append both stay legal. All three halves
  are mutation-tested, and the templates walk fails rather than passes if the
  directory reads empty.

  **This fix is subject to the defect it describes, one level up.** A crew prompt
  is also a snapshot: the embedded corpus reaches `$POGO_HOME/agents/` only when
  something runs `InstallPrompts` (a pogod restart does, from mg-342d), and a
  *running* mayor, doctor or PM keeps the prompt it started with. So the merge
  binds nobody currently alive. The install half is annunciated —
  `pogo check-staleness` reports a prompt corpus behind the repo — and the running
  half has the same answer this block gives: it went to mayor and pm-onethird by
  mail.
