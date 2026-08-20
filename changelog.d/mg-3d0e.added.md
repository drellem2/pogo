- **`pogo in-effect <commit>` answers "is this commit EXECUTING?" per artifact
  class, per carrier, on this box — the question `merged` and `done` were both
  being read as (mg-3d0e).** A merge puts bytes on a branch. Whether those bytes
  are running depends on what the commit touched, and the carriers move
  independently: compiled Go needs a rebuild *and* a restart of each binary that
  imports it; the agent-prompt corpus is embedded in a binary *and* installed
  under `~/.pogo/agents` *and* read once at spawn by each running agent; scripts
  and plists reach a runtime through an installed copy or through a checkout that
  executes them in place; docs and tests reach one never. Until now the only way
  to know which rule applied was to know which files the commit touched and
  reason it out per fix — and on 2026-08-19 that went wrong twice in one
  afternoon, in both directions.

  **Measured on this box, and both statements are true about the same day.**
  `pogo in-effect 3a3302f` (a compiled change merged that evening) reports the
  running pogod, and all four installed binaries, INERT — `1ebf2dc` does not
  contain it. `pogo in-effect 980048f` (the nightly-runner change) reports the
  installed copy at `~/.pogo/bin/pogo-deploy.sh` LIVE, because it holds exactly
  that commit's bytes. One evening, two artifact classes, two opposite answers.

  **It found a carrier no existing surface named.** `~/.pogo/deploy-src` is a
  second checkout that executes repo scripts in place. On the day this shipped it
  was pinned at `f83e956` — four days behind main — so the same commit was live
  in one checkout and inert in another, and nothing on the box said so. That is
  the report's third verdict word: `half-live`, which the existing {merged, not
  merged} vocabulary has to pick one half to be wrong about.

  **THREE verdicts per carrier, never two.** `live` and `inert` are readings;
  `unknown` is the absence of one, and it is never folded into `inert`. They owe
  different actions — redeploy versus investigate — and a check that goes green
  because it measured nothing is the failure this command exists to remove.
  Exit status follows: `0` in effect (or nothing with a runtime carrier), `1`
  inert or half-live, `3` not established.

  **Every row names where it was measured**, so it can be re-run by hand: a
  revision carrier is `git merge-base --is-ancestor <commit> <observed>`, and an
  installed copy is dated by finding the newest revision of that path whose bytes
  it holds.

  **Carriers are PROBED, not declared, and the classification claims are pinned
  by tests.** A table saying which script gets installed where is the kind of
  claim that rots silently — that is this ticket's own defect one layer down — so
  the class says only what kind of carrier to look for and the box says which
  exist. The programs are read out of `cmd/`; the binaries that carry a package
  come from `go list -deps`, not a prefix table; the constant naming the package
  that embeds the prompt corpus is checked against the real `//go:embed`
  directive; and a path no rule matches renders as its own UNKNOWN row rather
  than falling into the documentation bucket where it would sit under a verdict
  that reads as clean.

  **The command is subject to its own condition, and says so on every run.** It
  ships inside the `pogo` CLI, so it is an artifact of the `compiled` class it
  reports on: an installed `pogo` predating this change does not have it, and one
  predating a later change to the classifier answers with that build's rules. So
  every report ends with the vcs revision of the binary that produced it, and the
  command is registered top-level — where an old binary fails loudly — rather
  than under `pogo service`, where cobra answers an unknown subcommand with exit
  0.

  **What it does not claim to see, stated on every prompt finding:** the text a
  RUNNING agent is holding. An agent reads its prompt at spawn and keeps no copy
  anything can compare, so a current installed corpus is still not in effect for
  an agent started before it — that residual is mg-385f, and prompt rows carry
  the caveat rather than rendering as a pass. LaunchAgent plists are likewise not
  judged here: the installer renders them rather than copying them, so they match
  no git blob, and `pogo doctor --check`'s activation audit already asks the
  installer's own question about them.
