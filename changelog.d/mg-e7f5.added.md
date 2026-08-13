- **A polecat's MODEL can now be chosen at dispatch, on an axis separate from
  its harness — and pogo still pins no model of its own (mg-e7f5).** `--provider`
  picked the harness (`claude`, `codex`, `pi`, `cursor`) and template frontmatter
  carried only `worktree` and `nudge_on_start`, so there was no way to express
  "run this worker on a different model". That was blocking a real request: the
  onethird arc is mathematics — lemma derivation, spectral arguments, auditing
  proofs — while the rest of the fleet is software engineering, and there is no
  reason those should share a model.

  **Two tiers, and they stop.** `pogo agent spawn-polecat --model <name>` is the
  per-dispatch override; `model = "..."` in a prompt's TOML frontmatter is the
  role default beneath it, so `polecat-architect` can ask for a reasoning model
  without every dispatch typing it. Crew prompts read the same key (they have no
  flag, exactly as they have no `--provider`). `pogo agent list --json` reports
  the selection as `model`.

  **There is deliberately no third tier and no built-in default**, and that floor
  is the feature, not an unfinished edge. With neither set, pogo passes the
  harness **no model argument at all** and the harness runs on the user's own
  configuration — for Claude Code, `~/.claude/settings.json`, steerable at
  runtime with `/model`. An `[agents] model` config key was explicitly considered
  and left out.

  **Why, measured.** On 2026-07-06 a model pinned in pogo's config ran out of
  credit mid-day. Claude Code does **not** fall back when its pinned model is
  unavailable — it wedges at a "keep using this model or switch models" prompt,
  which from outside is indistinguishable from a busy agent. Every crew agent and
  every polecat on the machine went silent for **~5.5 hours**, because every
  agent spawns through the same override: the blast radius of one bad value is
  the whole fleet, not one worker. Recovery took a human running `/model` by
  hand. The fix then was to remove the pin, and the reasoning has survived since
  only as comments inside `~/.config/pogo/config.toml` — a file nobody
  implementing a `--model` flag in the binary would have any reason to open. It
  is now in the code, the tests, the help text, and here.

  **The provider boundary holds.** Model names are not providers. Each harness
  descriptor declares how to express a model (`Provider.ModelFlag`), and the
  spawn path asks for one in provider-neutral terms. A provider that cannot
  express a model selection **fails the spawn** rather than dropping the request:
  a worker running on a model nobody chose, while the dispatch record says
  otherwise, is worse than a dispatch that stops and says why. The refusal lands
  before the git worktree exists, so a bad `--model` costs no orphaned branch.

  **Validation is syntactic only.** A value must be a plausible model identifier
  and must not be smuggleable into argv as something else — a leading `-` would
  be parsed as another flag, whitespace cannot survive the template split. pogo
  keeps **no allowed-model list**: such a list goes stale on the vendor's release
  schedule and would refuse working models as confidently as broken ones. The
  only check that proves a value usable is running the harness against it
  (`claude --model <value> -p "ok"`), and the `--model` help text says so, because
  the flag is where someone deciding to pin a model will be standing.

  Deciding **which** work runs on **which** model remains a human call; this only
  makes the choice expressible.
