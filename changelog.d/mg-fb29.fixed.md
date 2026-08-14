- **gh-issue intake reports the FAULT rather than N unreadable repos — and
  `gh auth token` joins the credential chain, which is what makes that possible
  (mg-fb29).** Three changes that ride on one idea: a **positive credential
  predicate**, evaluated once, instead of each watched repo inferring the same
  global fact from its own failed lookup.

  **The message was wrong in both directions, and both were measured on this
  host.** `gh-intake-watch` fired *"2 unreadable repo(s): drellem2/macguffin,
  drellem2/pogo"* four times on 2026-08-14 — 01:18Z, 01:48:58Z, 02:31:51Z,
  03:02Z — each listing *"expired or missing gh auth"* **first** among four
  equally-weighted "common causes". The credential was valid with full scopes
  throughout and `~/.zshenv` had not been touched since May 24; the actual cause
  was an intermittent network/DNS failure, corroborated in the same minutes by
  four independent instruments (`server_error — ENOTFOUND` across fleet
  transcripts, 10 consecutive `ssh: connect to host github.com` in the refinery,
  `lookup proxy.golang.org: no such host` in a quality gate, and
  `gh-teardown-watch` reporting *"INSTRUMENT FAILURE — this run measured
  nothing"*). Two of those three sibling instruments already report this class
  correctly. Only gh-intake sent its reader at a wrong cause — and this ticket,
  filed from that framing, then sat marked as a human credential decision for
  **nine days**. The mirror case is the one the title names: a host with
  genuinely no credential gets N repo findings, not one of which says what to
  fix.

  **1. `gh auth token` is the last link in `internal/ghtoken`'s chain**
  (ambient → user shell → `gh auth token`). Its point is less the extra host it
  rescues than what it lets a caller *conclude*: before it, "no token harvested"
  did not mean "gh cannot authenticate", because `gh auth login` writes a
  `hosts.yml` the package could not see — so acting on `SourceNone` would have
  false-alarmed on every host that had ever logged in. After it, `SourceNone` is
  a decidable statement. It is also the one link of the proposed configurable
  chain that writes **no new copy of the secret**, which is why it ships while
  the rest stays reserved on mg-7d62. A non-zero exit is **ordinary, not a
  fault** — a host that never ran `gh auth login` is a normal host — and nothing
  parses gh's stderr to classify anything, because an English-message matcher
  stops working *silently* the first time gh rewords it.

  **The startup log line now names the winning source** (`source=ambient` /
  `source=shell` / `source=gh-auth-token`). That line is load-bearing rather than
  tidy: its *"harvested from the user shell"* form, 163 occurrences deep in
  `pogod.log`, is the evidence that refuted this issue's premise. With more than
  one source, a line that does not say which one won stops being able to answer
  the question it just answered.

  **2. An unreadable repo now names its cause, in three shapes.** Credential
  **missing** → one `NO GITHUB CREDENTIAL` finding with the failed repos listed
  as its consequences and `gh auth login` as the single remedy. Credential
  **present** → *"a credential WAS configured (source=…)"*, with the remaining
  causes **ranked**: network/DNS first, then rate limiting, then a renamed repo,
  then an expired or revoked credential **last but not excluded**. Not
  **evaluated** → says so, and claims nothing either way. The distinction reaches
  the **mail subject**, not just the body, because the subject is the part that
  gets skimmed and forwarded — which is how this ticket got its title. The
  subject states what was *measured* ("a gh credential WAS configured") rather
  than the conclusion ("not an auth fault"), which a startup snapshot cannot
  support; putting that stronger claim on the subject line would have rebuilt
  the defect in the opposite direction. The credential state is part of the
  notification fingerprint, so a credential that goes missing mails at once even
  though the repo list is byte-identical.

  **3. pogod checks it at STARTUP, not per poll.** The intake watcher's only
  arming precondition was `exec.LookPath("gh")`, with the argument written beside
  it: *"A missing gh is a precondition, not a finding."* That argument is
  extended from the binary to the credential — same shape, one global cause
  amplified into N findings — as a second A13 condition,
  `ghintake_no_credential`, with its own id because its remedy (`gh auth login`
  plus a restart) has nothing in common with the PATH case's plist edit, and a
  shared id would let whichever fired first suppress the other. The two are
  **ordered**: a host with no `gh` is told about `gh`, never about a credential
  it could not have obtained anyway — reporting a downstream symptom as the cause
  is the very defect being fixed, and the gate must not commit it one level up.

  **Not built, on purpose.** No classifier over gh's stderr English. A draft
  proposed it; it fails in the mode where the check keeps passing and nobody
  learns anything. The predicate replaces it entirely. The invariant from
  mg-039b holds unchanged: an unclassified gh failure still degrades to
  UNREADABLE, never to "clean".

  **Do not verify this with `ps`.** `ps eww -p <pogod-pid> | grep GH_TOKEN`
  returns nothing **even when the harvest succeeded**, because `ghtoken.Ensure()`
  calls `os.Setenv` *after* exec and macOS `ps` reports the exec-time
  environment. That false negative is the founding diagnostic of two separate
  reports that the daemon was tokenless, and both were wrong. Check a **child's**
  environment, or the startup log line that now names its source.
