- **A new reader of Go's automatic VCS stamp is now a build failure.**
  `internal/version/provenanceguard_test.go` enumerates the four places pogo is
  allowed to read `vcs.revision` — pogod's `GET /version`, `internal/version`'s
  `source=buildinfo` fallback, driftwatch's in-process self-report, and
  selfdrift's on-disk `debug/buildinfo` read — plus the one shell script that
  scrapes `go version -m`. Anything else fails the suite with file:line. It is
  an AST scan, so an ALIASED call (`dbg.ReadBuildInfo`) is caught where a name
  grep is not, and so documenting the trap does not count as falling into it;
  shell comments are stripped for the same reason. Stale allowlist entries fail
  too — an entry for a site that no longer reads the stamp is a standing licence
  for the next reader added there.

- **It is an enumeration, not macguffin's prohibition, and that was measured
  rather than assumed.** macguffin's mg-b7fe guard bans the toolchain stamp
  outright because mg consumes none of it. Run verbatim against this tree it
  fails on all four pogo sites, each of which is deliberate and documented where
  it sits — selfdrift's read is the FOREIGN STAMP detector's own input, so a
  prohibition would ban the detector along with the defect. Its shell half also
  reported PASS on pogo while the repo's only real scrape sat at
  `scripts/pogo-self-deploy:727`: that file has no `.sh` extension, and the
  ported walk keys on the extension. This guard identifies shell scripts by
  shebang as well.

- **The trap is now measured on pogod itself, closing the inference mg-b7fe
  left open.** From a clean pogo worktree at `359ff1a1`, `go build ./cmd/pogod`
  stamped `vcs.revision=d6d179f2` with `vcs.modified=true` — the HEAD of the
  enclosing `~/.pogo` repo, which had 13 dirty files. `d6d179f2` is not an
  object in pogo at all. The built CLI reported
  `pogo 0.10.0 (d6d179f-dirty, branch=unknown, source=buildinfo)`.

- **The FOREIGN STAMP runtime gate is not made redundant by this, and was not
  touched.** Identical Go sources — every `.go` file byte-identical, verified by
  diff — built in the nested worktree and in a standalone repo produced
  `d6d179f-dirty` and `c378c17`: one foreign, one correct, from the same bytes.
  The build LOCATION is the variable, so no source scan can separate them, and
  `source=buildinfo` flags where a revision came from rather than whether it is
  true. Only asking git whether the SHA exists in this repo turns it into a
  refusal. The guard bounds who may read the stamp; the gate classifies stamps
  that already exist, including in binaries already installed on a box.

- **The guard has been observed prohibiting.** Eight injections into the live
  tree — aliased call, blank `debug/buildinfo` import, dot-import, a second
  reader inside an already-licensed file, an extensionless shell scraper, an
  extra scrape inside the licensed script, and a stale allowlist entry on each
  side — each failed with file:line and was then removed. Two comment-only
  cases correctly did not trip. `TestTheProvenanceGuardCanFail` keeps that
  control in the suite against a fixture, because a manual injection proves the
  guard worked on the afternoon someone ran it; breaking alias handling or
  reverting to the `.sh`-only walk makes it fail.
