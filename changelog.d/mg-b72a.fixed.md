- **The nightly deploy job resolves `git` by running it, like every other tool it uses (mg-b72a).**
  `scripts/launchd/pogo-deploy.sh` proves `mg` and `pogo` by executing the candidate, but
  pinned `GIT=/usr/bin/git` on the reasoning that git ships in `/usr/bin` on every macOS.
  It does — but that path is the Xcode Command Line Tools *shim*, and a damaged install
  behind it makes it fail every invocation, `git --version` included, with
  `unable to locate xcodebuild` and exit 71, while staying executable and on `PATH`. `-x`
  and `command -v` cannot tell it from a healthy git. `git` is now resolved the way `mg`
  already was: candidates in order, each required to print `git version` before it is
  accepted, preferring a real Homebrew/local git while keeping the shim as a fallback so a
  CLT-only box still deploys. An operator-pinned `$GIT` is still honoured, and now
  health-checked too. **This is a consistency change; no such breakage has been reproduced.**
  On a host whose only git is a healthy `/usr/bin/git` the candidate list collapses to that
  one path, and the only behavioural difference is that a broken shim would abort once, with
  an alert, instead of failing separately inside every clone/fetch/rev-parse in `sync_src`.

- **`scripts/pogo-deploy_test.sh` stops depending on the health of the host's Xcode install (mg-b72a).**
  The suite hardcoded `GIT=/usr/bin/git` and called a bare `git` off `PATH` for its repo
  fixtures, so its verdict rested on exactly the assumption the runner now refuses to make.
  It resolves one git and uses it throughout, and the new `resolve_git` assertions run
  against **fakes**: an executable binary that reproduces the shim's exit 71 with empty
  stdout, and one that exits 0 while printing nothing. Substituting the candidate list is
  what makes them mean something — on a host with a working git the real list can never
  produce a rejection, so a check exercised only against the real list would report green
  whether or not it existed.

- **`--help` stops truncating the runner's own documentation (mg-b72a).**
  It printed a hardcoded `sed -n '2,80p'` line range, which had already drifted and was
  cutting the ENV list off mid-list — doc rot of the quiet kind, since the output still
  looks complete. The header is now bounded by the `set -u` sentinel that ends it.
