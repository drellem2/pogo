- **A work item can now declare where an agent may edit, and be believed
  (mg-f1d5).** The polecat prompt has always said "Stay scoped. Only work on your
  assigned task." Nothing enforced it. Every agent runs `claude
  --dangerously-skip-permissions` by design (`docs/polecat-permissions.md`), so
  an edit anywhere on the machine is one tool call away and a polecat that
  wanders is caught at review time or not at all.

  Added `scripts/mg-scope-guard.sh`: a `scope:` carrier line in a work item's
  body — beside the existing `workflow:`/`stage:`/`gh:` block — names the
  editable surface, and the script, wired as a Claude `PreToolUse` hook, refuses
  `Edit`, `Write`, `MultiEdit` and `NotebookEdit` anywhere else. It also answers
  directly (`mg-scope-guard.sh PATH...`) in macguffin's shared exit vocabulary —
  `0` in scope, `9` out, `11` escape — so another tool can ask without holding a
  copy of the matching rules. Install instructions in `docs/mg-scope.md`.

  **It is inert until somebody asks for it, and loud afterwards.** No item in
  force, or an item declaring no scope, and every path is allowed — an agent that
  never opted in is never blocked, which is the property that decides whether a
  guard like this survives contact with a fleet. Past the opt-in the rule
  inverts: a missing `mg`, a missing `jq`, an unreadable item or no worktree root
  all *refuse*, naming the fix. An opted-in agent that is silently unenforced is
  the exact failure this removes, and it is invisible; a refusal is not.

  **`**` crosses directory separators and `*` does not**, which is the one piece
  of real logic here and the one a `[[ str == pat ]]` shortcut gets wrong: bash's
  `*` swallows slashes, so `docs/*.md` there would match `docs/sub/a.md` and
  every narrow scope would silently be a wide one. The pattern is compiled to a
  regex instead, and the case is pinned. Paths normalise **lexically** rather
  than through `realpath`, because a `Write` names a file that does not exist yet
  and `realpath -e` on it fails — which past the opt-in would turn every
  new-file creation into a refusal. An escape is kept at `11` rather than folded
  into `9`: a break-out and a typo want different responses.

  **The uncovered half is stated, not implied.** Writes through `Bash` are out of
  reach — a hook sees the command line, not what the command will touch — and the
  doc says so. This stops the accident, not the determined agent. A guard
  believed to cover more than it does is worse than no guard.

  43 cases in `scripts/mg-scope-guard_test.sh`, run by `./test.sh`, all against a
  stub `mg` and a fixture worktree in a temp dir so the suite never reads the
  developer's live `~/.macguffin`.

- **A study of Redjive2's Orc, and what pogo should take from it (mg-f1d5).**
  `docs/orc-comparative-study-2026-07-28.md`. Orc turns out not to be one
  orchestrator but a ~148k-line Go monorepo of seven tools — and `macmuffin`,
  which had been looked for as a repository, is a *module* inside it (`muff`), as
  are Mailman, Communiqué, Anno, Dock and Orcprobe. The doc specs each against
  its source rather than its README, diffs it against pogo/macguffin in both
  directions, and ranks reimplementations by value over cost. The scope guard
  above is the first entry on that list; the rest — confirmed nudge delivery,
  seeding Claude's first-run state instead of racing the trust dialog, a packaged
  test sandbox, mail read-receipts — are scoped and costed, not built. Orc's
  permission model is explicitly *not* recommended for adoption yet: pogo's fence
  is the merge queue, which is a different fence in the same field.
