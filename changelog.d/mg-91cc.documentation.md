- **The PM sweep gets a specified open-PR pass, so a PR nothing is carrying
  stops reading as a healthy one (mg-91cc).** New
  [docs/pm-open-pr-pass.md](docs/pm-open-pr-pass.md) specifies a per-repo,
  per-sweep pass over open pull requests, enabled per PM with `"open-prs"` in
  the `sources` array of `~/.pogo/agents/pm/<name>.toml`. For each open PR it
  asks two questions the baseline never asked — is its head an ancestor of
  `origin/main`, and does a work item track it in **any** of the six statuses —
  and routes the answer to one of four dispositions: in flight, landed-but-open
  (close, reap the branch), stranded (file a carrier), superseded (close naming
  what landed). Never merge or close on a guess, and closing routes through the
  coordinator because it is outward-facing.

  **The listing half was never the gap.** The baseline's per-repo GitHub loop
  has run `gh pr list` since the template shipped (mg-1897, firmed by mg-b1e7),
  and that defaults to `--state open`. What was absent is the *disposition*: from
  a listing alone, an in-flight PR, one whose work already landed, one nothing
  will ever pick up, and one a competing PR superseded are indistinguishable,
  and the two cross-checks that tell them apart are run by no baseline source —
  `gh issue list` does not return PRs, and `mg list` has no row for a PR no work
  item tracks. The doc says so explicitly so the stale "open PRs are unlisted"
  framing does not get repeated.

  Both motivating instances were live on 2026-07-29. **gh#93** was MERGEABLE
  with green CI, its head `c48b055` not an ancestor of `origin/main`, and its
  `mg-36e3` absent from every status — the PR was the only place that work
  existed, and since the refinery merges from work items, nothing was ever going
  to pick it up. **gh#95** was the gh#94 implementation that did not land, left
  open, and it **misled the gh#97 reporter into walking back a correct finding**
  against `ownedByLive` / `polecatDirOwner`, symbols that do not exist on `main`.
  That is the generalisable cost: an open PR reads as the state of the world to
  someone outside the fleet, so an unlanded one left open is a false claim about
  the product, published.

  **Config only; the baseline is untouched and still carries the gap.**
  `internal/agent/prompts/pm/pm-template.md` is the home that would fix every PM
  including ones not yet instantiated, and it is a `.protected-paths` red line
  the refinery refuses with no bypass (mg-6c4b, incident mg-2a50) — it lands by
  hand-push from the repo owner. So a PM scaffolded later inherits the original
  blind spot unless whoever scaffolds it copies the `sources` entry. The doc
  states that asymmetry rather than letting the fix read as complete.
