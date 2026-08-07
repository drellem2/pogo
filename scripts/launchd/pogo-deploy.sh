#!/bin/bash
# pogo-deploy.sh — the unattended nightly redeploy TRIGGER (mg-42ac / mg-b7d0).
#
# WHAT THIS IS NOT
# ----------------
# It is not a deployer. Every gate that matters — the ancestry guard, the drain,
# the build, do_prove, the kickstart, verify_running, the mail-check post-check —
# already lives in scripts/pogo-self-deploy, which is deliberately thin and
# rarely touched. This file adds the ONE thing that script cannot supply for
# itself: a caller outside pogod's process tree that fires on a clock. Anything
# here that starts to look like deploy logic belongs in pogo-self-deploy instead.
#
# It is also NOT com.pogo.recovery. Recovery is the tier-3 safety net that
# bounces a wedged pogod; folding a deploy into it would mean every recovery
# restart also became a rebuild from main, which is the opposite of a safety net
# (mg-cf48 declined exactly that conflation). Two jobs, two labels, two logs.
#
# WHY IT IS A LAUNCHD JOB AND NOT A pogod SCHEDULE
# ------------------------------------------------
# pogo-self-deploy's first line is assert_out_of_band (mg-1bbf): it refuses any
# caller inside pogod's tree, because the kickstart -k it ends with kills that
# tree — including the caller, mid-deploy, with nothing left running to report
# what happened. Every crew agent and every polecat is such a descendant. A
# LaunchAgent is parented by launchd, so it passes the guard by construction.
#
# THE THREE FAILURE MODES THIS FILE IS SHAPED AROUND
# --------------------------------------------------
#   (a) it silently no-ops forever — the window is too narrow, or the mac slept
#       through 03:00 and the catch-up fire lands at 14:00 mid-workday;
#   (b) it clobbers Daniel's dev tree — a fetch/checkout/merge in a working
#       directory he may be mid-edit in;
#   (c) it forces a bad deploy — a do_prove RED papered over with --force.
#
# Against (a): the window guard is a RANGE, not an instant, and every skip is
# logged with its reason so a job that never deploys is distinguishable from a
# job that never ran. Against (b): the deploy builds from a DEDICATED checkout
# ($POGO_DEPLOY_SRC, default ~/.pogo/deploy-src) that nothing else writes to,
# and even there a dirty tree aborts rather than resets. Against (c): --force is
# not passed, not plumbed, and not overridable by env — a do_prove RED (exit 9)
# is a loud alert and a preserved running pogod, which is the correct outcome.
#
# THE DRAIN BUDGET IS DERIVED FROM THE WINDOW, NOT A CONSTANT (mg-8f7e)
# -----------------------------------------------------------------------
# pogo-self-deploy's own default is 1800s, and on 2026-07-31 that budget was
# shorter than a routine polecat lifetime: the drain gave up with three still
# working, whose uptimes were 1h33m, 1h19m and 38m. Two of the three had
# individually been running longer than the whole budget before the drain
# started. The nightly exited 7 and the next attempt was 24 hours away.
#
# The fix is not a bigger constant — a constant calibrated against today's queue
# expires the moment the queue shifts toward longer items, which is exactly what
# had happened. It is to stop having a constant. The number that actually
# constrains the drain is when the window closes: the redeploy must not bounce
# the fleet into the working day. So the budget is
#
#     (seconds until WINDOW_END) - RESERVE, capped at MAX_DRAIN
#
# where RESERVE is the time the build, do_prove, kickstart and verification need
# after the drain finishes. A 03:00 fire under the production window gets 2h
# rather than 30m, and a fire that lands too late to finish gets no attempt at
# all rather than a doomed one. Nothing here needs recalibrating when polecats
# get longer — the window is the thing an operator already reasons about.
#
# A SYNC FAILURE IS CLASSIFIED BEFORE IT IS BLAMED, AND RETRIED IF IT IS THE
# NETWORK (mg-0d70)
# ---------------------------------------------------------------------------
# On 2026-08-05 the nightly fired on time and aborted one second in:
#
#     ssh: connect to host github.com port 22: Undefined error: 0
#     fatal: Could not read from remote repository.
#     02:00:04Z ERROR: sync: git fetch origin failed
#
# and mailed Daniel a remedy that read "inspect 'git -C ~/.pogo/deploy-src
# status' — dirty or diverged aborts by design." The checkout was neither; it was
# clean and on main. An operator following that remedy finds nothing wrong and
# concludes the alert was spurious, which is worse than no alert because it
# spends the reader's trust. Four hours of window went unused for a fault that
# lasted one second.
#
# Two defects, and they are separate:
#
# (1) ONE remedy was printed for every way sync_src can fail. sync_src already
#     knows which of its five steps failed — fetch, clone, porcelain, checkout,
#     ff-merge — and threw that away. It now records the step in SYNC_CLASS and
#     the underlying stderr in SYNC_DETAIL, and the alert prints both. "dirty or
#     diverged" is now said ONLY when the porcelain or the ff-merge actually said
#     so.
#
#     Splitting a transport failure into "the network" and "auth/permission"
#     needs one more bit, and it is NOT obtained by reading git's English. git
#     prints "make sure you have the correct access rights" after ANY ssh
#     failure including a pure connectivity one, and prose matching stops working
#     the day the tool rewords it (the trap t55ca refused on gh#113). Instead the
#     runner MEASURES: it parses host and port out of the remote URL and tries to
#     open a TCP connection. Reachable and the fetch still failed -> not the
#     network. Unreachable -> the network. No endpoint to probe -> it says it
#     could not classify and prints the error verbatim rather than guessing.
#
#     The probe is a bash /dev/tcp redirect, deliberately: it is a shell builtin,
#     so it adds no binary to resolve at 03:00 on the path that has to work when
#     everything else is broken.
#
# (2) A network-class abort settled the night. pm-pogo's ruling gives the rule
#     and the discriminator to encode (see sync_class_retryable):
#
#         retry a failure that establishes NOTHING about the tree; do not retry
#         one that establishes a FACT. Concretely: would re-running plausibly
#         give a different answer, for a reason UNRELATED TO THE CODE?
#
#     So the transport classes retry — the sync never reached the tree, and the
#     repo state is simply unknown — while `dirty`, `diverged`, `checkout` and
#     `config` do not, because each is exactly as true in thirty seconds. The
#     ruling's other examples land where they already were: a build failure and a
#     do_prove RED are pogo-self-deploy's exits and gate 3 settles the night on
#     both.
#
#     It applies at BOTH layers, and they are independent:
#
#       IN-RUN    up to POGO_DEPLOY_SYNC_ATTEMPTS tries with backoff, bounded by
#                 POGO_DEPLOY_SYNC_RETRY_BUDGET. Needs no second fire, so it
#                 works against the schedule installed on this box TODAY, and it
#                 is what would have saved 08-05 — that fault lasted one second.
#       CROSS-FIRE  a retryable sync abort now exits 10 rather than 1, and gate 3
#                 reopens the night on it exactly as it does on a stalled drain.
#                 This half is INERT until mg-fc99 lands: the installed plist has
#                 StartCalendarInterval as a dict with Hour=3, so nothing fires at
#                 04:00 to carry it. Landed here anyway because the policy and
#                 the schedule are two artifacts on two install paths, and the
#                 whole reason this ticket exists is that landing one and calling
#                 it done is a mistake this box has already made once.
#
#     Time spent in backoff is time taken from the drain, so the budget is
#     RECOMPUTED after the sync returns. Sleeping for two minutes and then
#     handing the drain a budget calculated before those two minutes is how a
#     window-derived number quietly stops being derived from the window.
#
# THE OUTAGE IS LONGER THAN THE FIRES — SO PATIENCE, NOT SCHEDULE (mg-5515)
# -------------------------------------------------------------------------
# Both retry layers above are sized for a BLIP. The in-run tier waits three
# minutes; the cross-fire tier spaces three attempts across two hours (03/04/05
# local, installed by mg-b201). The one outage this box has actually been
# measured through ran 2026-08-07 13:24:30Z -> 16:14:52Z — 2h50m — so an outage
# of that length starting at or before the first fire swallows all three, and
# every fire fails on arrival. n=1: this is not "two hours is the wrong span",
# it is "the span was chosen without reference to any observed duration and the
# only observation exceeds it".
#
# NO AGENT COVERS THIS. An outage of that shape takes every agent on this box out
# at once — a watcher shares the failure mode it is meant to watch for — so more
# watchers buy nothing and the fix has to be in this file.
#
# AND THE FIRES ARE NOT THE LEVER EITHER. Three instants spend three attempts and
# about nine minutes of a four-hour window between them. Re-spacing three instants
# cannot cover a 170-minute outage; adding more only shortens the gaps between
# attempts that all fail identically; and widening the window to make room for
# them lengthens every drain (the window's width IS the drain's patience, above)
# and pushes a fleet-wide bounce toward the working day. What the window can
# afford and does not currently spend is the other 231 minutes.
#
# So the third tier is patience, spent inside the window that already exists:
#
#       VIGIL     once the blip tier is spent and the class still established
#                 NOTHING, keep probing every POGO_DEPLOY_SYNC_VIGIL_INTERVAL
#                 (300s) for as long as the window could still afford a drain on
#                 the far side of the sleep. That per-sleep test is mg-0d70's
#                 condition 2 and it is the vigil's ONLY bound: the vigil adds
#                 patience, never window. A 03:00 run therefore probes until
#                 ~05:30 and deploys the MOMENT connectivity returns, rather than
#                 giving up at 03:03 and waiting for the top of the next hour.
#
# Two consequences worth stating because they are the costs:
#
#   - THE ALERT ARRIVES LATER. A network night now mails at ~05:30 instead of
#     ~03:03. Accepted: the reader of this alert is asleep for both, and the
#     later one carries strictly more information.
#   - A RUN NOW HOLDS THE LOCK FOR HOURS. The 04:00 and 05:00 fires find it held
#     and exit 0, which is correct — they would have failed identically. But
#     acquire_lock reclaims a lock whose mtime is older than STALE_LOCK_MIN, and
#     a vigil from a 02:00 wake-fire outlives that, so the vigil refreshes the
#     mtime (touch_lock). Without it the fix would introduce a concurrent deploy.
#
# The vigil also pays for itself in evidence: SYNC_VIGIL_SPENT is a probed LOWER
# BOUND on how long the transport was unreachable, and the alert, the log and the
# recovery notice all report it. mg-5515's own honest bound was that it had one
# duration and no distribution. Every vigil night from here adds a point to one.
#
# WHAT THE VIGIL DOES NOT REACH — stated because it would otherwise read as
# solved. The vigil's reach is (vigil start) -> the moment drain_budget hits
# zero, which under the production window is 05:30: 21600s of window, less
# RESERVE (1200) and MIN_DRAIN (600). So a 03:00 fire reaches 2h30m and a 02:00
# wake-fire reaches 3h30m — and 2h30m is SHORTER than the 2h50m outage that
# prompted this. An outage of exactly that length starting at exactly 03:00
# still costs the night.
#
# That residual is not fixable here, and it is not a shortage of patience: an
# outage ending at 05:50 leaves ten minutes before the window closes, and RESERVE
# alone is twenty. There is no deploy that could complete. Saving that specific
# night needs a WIDER WINDOW, which costs longer drains and a fleet bounce nearer
# the working day, and mg-5515 is explicit that it does not have the distribution
# to justify picking a new width. It is deliberately not picked here. What the
# vigil changes is the shape of the loss: the night is lost only when the outage
# runs past 05:30, instead of whenever it merely covers three instants — and the
# alert now reports a measured duration, which is the input that would let
# somebody choose a window width on evidence rather than on this single figure.
#
# HOW THIS FIX COULD EXHIBIT THE DEFECT IT REMEDIES
# --------------------------------------------------
# The defect was naming a cause that had not been established. Three ways the
# classification above can do the same thing, smaller:
#
#   - THE PROBE MEASURES A LATER MOMENT. git failed at T; the probe connects at
#     T+ε. A blip that ended in between answers, and the runner then reports
#     `remote` — auth or permission — for what was really the network. Milder
#     than blaming the checkout, and the same species. The `remote` remedy
#     therefore says when the measurement was taken rather than presenting it as
#     a property of the world, and the retry means a blip of that shape usually
#     ends in a successful sync and no alert at all.
#
#   - A TCP HANDSHAKE IS NOT AN SSH SESSION. A captive portal or a middlebox
#     that accepts the connection and then resets it answers the probe while
#     breaking git, and lands in `remote`. The probe is a floor on connectivity,
#     not a proof of it, and the remedy is worded to that.
#
#   - A RETRY THAT USUALLY WORKS HIDES A NETWORK THAT USUALLY DOESN'T. Nights
#     that quietly succeed on attempt 3 would turn a degrading link into a
#     slower deploy nobody investigates. So a recovered sync LOGS that it was
#     recovered and how long it took — the same "leave a positive artifact"
#     argument the ticket makes about fires that did not happen.
#
# WHY A LONG SINGLE DRAIN, AND ONLY THEN A RETRY
# -----------------------------------------------
# Retries are the weaker half of this and must not be mistaken for the fix.
# `draining=true` refuses new polecat dispatch (verified live in the running
# daemon: internal/agent/api.go handleSpawnPolecat, shipped in 1b1f12d), so a
# drain is MONOTONE — the count only falls. The moment an attempt gives up,
# pogo-self-deploy's exit trap restores dispatch and the fleet refills. Three
# 30-minute attempts are therefore strictly worse than one 90-minute attempt:
# each retry starts against a partly-fresh fleet, and the long blockers it was
# waiting out have been joined by new ones.
#
# So the budget comes first and the retry is the backstop for an attempt that
# ended EARLY — one that stalled with window left over, or a night where the
# 03:00 fire never happened because the mac was asleep. It is scoped to exit 7
# for the same reason: that is the only exit whose cause is "the fleet was
# busy", which is transient, and the only one that built nothing and bounced
# nothing. A build failure or a control-suite RED will fail identically on the
# retry and mail a duplicate alert, so the night is settled on the first attempt.
#
# HYGIENE: ABSOLUTE PATHS, NEVER BARE NAMES (mg-dd5f / mg-015f)
# -------------------------------------------------------------
# launchd hands a job a minimal PATH, and on macOS /usr/bin/mg is the Micro-Emacs
# EDITOR — bare `mg` binds to it, panics headless ("standard input and output
# must be a terminal") and delivers no alert at all. So `mg` is resolved to an
# absolute path through an IDENTITY check before use, and `pogo` /
# `pogo-self-deploy` are addressed by absolute path too. A wrapper that alerts
# via a binary it did not verify is a wrapper with no alert path.
#
# ...AND AN ABSOLUTE PATH IS NOT A WORKING BINARY (mg-b72a)
# ----------------------------------------------------------
# The identity check above is really an EXECUTION check: `mg` is accepted only
# after it runs and prints something only macguffin prints. `git` was the one
# primitive that did not get the same treatment — it was pinned to /usr/bin/git
# on the reasoning that git ships in /usr/bin on every macOS. It does; but
# /usr/bin/git is the Xcode Command Line Tools SHIM, and a shim is not a git.
# When the install behind it is damaged the shim fails EVERY invocation,
# `git --version` included, with "unable to locate xcodebuild" and exit 71 —
# while staying executable, staying on PATH, and remaining indistinguishable
# from a healthy git to `-x` and to `command -v`.
#
# So `git` is now resolved the way `mg` already was: candidates in order, each
# required to prove itself by actually RUNNING. This is a CONSISTENCY change and
# is justified as one — `git` was the lone primitive trusted on sight. No such
# breakage has been reproduced on this host, and the change is deliberately
# modest here: where the only git present is a healthy /usr/bin/git the
# candidate list collapses to that one path, and the sole difference is that a
# broken shim would abort once, loudly, with an alert, instead of failing
# separately inside every clone/fetch/rev-parse in sync_src. It changes which
# git runs only on a host that has more than one — a Homebrew box — and there it
# deploys instead of aborting.
#
# GH_TOKEN IS SOURCED AT RUNTIME, NEVER FROM THE PLIST
# ----------------------------------------------------
# ~/Library/LaunchAgents is world-readable; a token in the plist is a token on
# disk for every process on the box. It is read out of ~/.zshenv at run time
# instead (one grep, one eval of one export line — not a wholesale source of a
# zsh file by bash). The value is never logged, echoed, or mailed.
#
# USAGE
#   pogo-deploy.sh              # the nightly run (what launchd invokes)
#   pogo-deploy.sh --dry-run    # every gate, no redeploy — prints what it would do
#
# ENV overrides (all optional; defaults are the production values):
#   POGO_DEPLOY_SRC          dedicated checkout to build from (~/.pogo/deploy-src)
#   POGO_DEPLOY_REMOTE       clone URL used to create it if absent
#   POGO_DEPLOY_BOOTSTRAP_REPO  repo to read the origin URL from ($HOME/dev/pogo)
#   POGO_DEPLOY_WINDOW       "START-END" local hours, half-open (default "2-6")
#   POGO_DEPLOY_ZSHENV       file to read GH_TOKEN from ($HOME/.zshenv)
#   POGO_DEPLOY_GRACE        seconds to wait before the post-bounce check (120)
#   POGO_DEPLOY_LOCK_DIR     mutual-exclusion dir
#   POGO_DEPLOY_ALERT_TO     first alert recipient (mayor; `human` is always copied)
#   POGO_DEPLOY_SKIP_WINDOW  set to 1 to bypass the window guard (controls only)
#   POGO_DEPLOY_NOW          "HH" override for the window guard (tests only)
#   POGO_DEPLOY_RESERVE      seconds of the window kept back for build+bounce (1200)
#   POGO_DEPLOY_MAX_DRAIN    ceiling on one drain, seconds (7200)
#   POGO_DEPLOY_MIN_DRAIN    floor below which a fire does not attempt at all (600)
#   POGO_DEPLOY_FIRE_HOURS   the plist's fire hours, for "will a retry follow?" ("3 4 5")
#   POGO_DEPLOY_STAMP        the night's attempt record ($POGO_HOME/deploy-attempt.stamp)
#   POGO_DEPLOY_SYNC_ATTEMPTS  total sync tries on a RETRYABLE class (4)
#   POGO_DEPLOY_SYNC_BACKOFF   seconds between them, last value repeats ("15 45 120")
#   POGO_DEPLOY_SYNC_RETRY_BUDGET  ceiling on total blip-tier backoff, seconds (300)
#   POGO_DEPLOY_SYNC_VIGIL     1 to wait a transport outage out inside the window (1)
#   POGO_DEPLOY_SYNC_VIGIL_INTERVAL  seconds between vigil probes (300)
#   POGO_DEPLOY_PROBE_TIMEOUT  seconds to wait for the reachability probe (5)
#   POGO_DEPLOY_NC           pin an nc for the probe (still checked by execution)
#   GIT                      pin a specific git (still checked by execution)
#
# HOW THE RED ALERT KNOWS WHAT FAILED (mg-0155). Not from the exit code. This
# runner passes POGO_DEPLOY_REASON_FILE to pogo-self-deploy, which writes the
# failing sentence it already computed, the stage it reached, and whether
# anything had been installed by then; the alert prints THAT. describe_exit /
# remedy_for_exit remain as the fallback for a run that left no record. See
# section 7a, and docs/deploy-exit-paths.md for the enumeration of every exit
# path the deploy script has.

set -u

HOME="${HOME:-$(cd ~ && pwd)}"
SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
BOOTSTRAP_REPO="${POGO_DEPLOY_BOOTSTRAP_REPO:-$HOME/dev/pogo}"
DEPLOY_REMOTE="${POGO_DEPLOY_REMOTE:-}"
WINDOW="${POGO_DEPLOY_WINDOW:-2-6}"
ZSHENV="${POGO_DEPLOY_ZSHENV:-$HOME/.zshenv}"
GRACE="${POGO_DEPLOY_GRACE:-120}"
LOCK_DIR="${POGO_DEPLOY_LOCK_DIR:-$HOME/.pogo/deploy.lock.d}"
# The coordinator, not a named PM. This defaulted to `pm-pogo` — an agent that
# exists on one machine (mg-f04b) — so on any other install the first alert went
# to a mailbox with no reader, and mg files mail for an unknown name rather than
# refusing, so the delivery looked fine. A deployment whose PM owns deploys sets
# POGO_DEPLOY_ALERT_TO. `human` is copied either way.
ALERT_TO="${POGO_DEPLOY_ALERT_TO:-mayor}"
DEPLOY_REF="${POGO_DEPLOY_REF:-main}"
STALE_LOCK_MIN="${POGO_DEPLOY_STALE_LOCK_MIN:-180}"
DRY_RUN=false

# The drain budget's three parameters (mg-8f7e — see the header).
#
# RESERVE is what the run still owes after the drain returns: go install, the
# post-install revision check, do_prove, the kickstart, verify_running, the
# mail-check post-check, and this file's own GRACE sleep. 20 minutes is generous
# for a build that normally takes two, and being generous is the right side to
# err on — overrunning the window means bouncing the fleet into the working day,
# while under-using it costs only drain patience.
#
# MAX_DRAIN caps one attempt at 2h. The drain holds `draining=true` for its whole
# duration and nothing dispatches while it does, so an unbounded budget would
# trade a missed deploy for a night of no dispatch at all. 2h clears the longest
# polecat lifetime observed on 07-31 (1h33m) with margin.
#
# MIN_DRAIN is the floor. A fire landing at 05:50 has minutes, not hours; it must
# skip rather than start a drain it cannot finish, because a drain that times out
# has still stopped dispatch for its whole length and delivered nothing.
RESERVE="${POGO_DEPLOY_RESERVE:-1200}"
MAX_DRAIN="${POGO_DEPLOY_MAX_DRAIN:-7200}"
MIN_DRAIN="${POGO_DEPLOY_MIN_DRAIN:-600}"

# The plist's fire hours. Duplicated from com.pogo.deploy.plist rather than read
# from it, and that duplication buys exactly one thing: the RED alert can say
# whether a retry is coming instead of asserting "did not retry" and being wrong.
# A drifted list makes one sentence of one alert optimistic; reading the plist at
# 03:00 to avoid that would add a parse and a failure mode to the path that has
# to work when everything else is broken.
FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-3 4 5}"

# The in-run sync retry, BLIP TIER (mg-0d70). Four attempts at 15s / 45s / 120s
# is three minutes of patience against a four-hour window — the 08-05 fault
# lasted one second, and this is sized to cross a blip rather than to wait out
# an outage. RETRY_BUDGET is the hard ceiling on the sleeping, so the numbers
# above can be tuned without anyone having to re-derive what the worst case
# costs the drain.
SYNC_ATTEMPTS="${POGO_DEPLOY_SYNC_ATTEMPTS:-4}"
SYNC_BACKOFF="${POGO_DEPLOY_SYNC_BACKOFF:-15 45 120}"
SYNC_RETRY_BUDGET="${POGO_DEPLOY_SYNC_RETRY_BUDGET:-300}"

# The in-run sync retry, VIGIL TIER (mg-5515). The blip tier's own comment says
# what it does not do, and mg-5515 is the ticket for that gap: it is sized to
# cross a blip, not to wait out an outage. The outage measured on this box on
# 2026-08-07 ran 13:24:30Z -> 16:14:52Z — 2h50m — and the three fires at 03/04/05
# local span two hours, so an outage of that length beginning at or before the
# first fire swallows all three and costs the night.
#
# THE FIRES ARE NOT THE LEVER. Three instants spend, between them, three attempts
# and about nine minutes of the four-hour window; re-spacing three instants
# cannot cover a 170-minute outage, and widening the window to make room for more
# of them lengthens every drain (the window's width IS the drain's patience,
# mg-8f7e) and pushes a fleet-wide bounce toward the working day. What the window
# can afford and does not currently spend is the other 231 minutes.
#
# So: once the blip tier is spent and the class still established NOTHING, keep
# probing at a flat interval for as long as the window could still afford a drain
# on the far side of the sleep. That last clause is the only bound, and it is the
# one mg-0d70's condition 2 already enforces per-sleep — the vigil adds patience,
# never window. A run that starts at 03:00 therefore probes until ~05:30 and
# deploys the moment connectivity returns, instead of giving up at 03:03 and
# waiting for the top of the next hour.
#
# The interval is FLAT, not geometric. The blip tier's 15/45/120 ramp is right
# for crossing a one-second fault fast; a ramp here would keep doubling until the
# run was asleep through the recovery it exists to catch. Five minutes is cheap
# against a 150-minute vigil (about 30 probes, each a TCP connect) and fine-
# grained enough that the log doubles as a duration measurement of the outage —
# which is the n>1 evidence mg-5515 says is the input actually worth having.
SYNC_VIGIL="${POGO_DEPLOY_SYNC_VIGIL:-1}"
SYNC_VIGIL_INTERVAL="${POGO_DEPLOY_SYNC_VIGIL_INTERVAL:-300}"

PROBE_TIMEOUT="${POGO_DEPLOY_PROBE_TIMEOUT:-5}"

# Set by sync_src on every exit path. SYNC_CLASS is which STEP failed — the fact
# the runner actually has — and SYNC_DETAIL is that step's stderr, kept verbatim
# so the alert can print what was observed instead of what is usually true.
SYNC_CLASS=""
SYNC_DETAIL=""

# Where the night's outcome is recorded, so a 04:00 fire knows what the 03:00
# fire did. Under POGO_HOME, which the plist binds, so the job and an operator
# running it by hand agree on the path.
STAMP="${POGO_DEPLOY_STAMP:-${POGO_HOME:-$HOME/.pogo}/deploy-attempt.stamp}"

# Set once a fire is past the window/lock/stamp gates and is really attempting
# tonight's deploy. The EXIT trap records an attempt only when it is true, so a
# fire that skipped (late, locked out, already settled) leaves no trace and
# cannot be mistaken for one that ran.
ATTEMPT_ARMED=false
ATTEMPT_N=0

# GIT, MG and POGO_CLI are all resolved at run time and all checked by RUNNING
# the candidate, never by trusting its path. GIT is seeded from the environment
# when an operator pins one, and that pin is health-checked like any other
# candidate: a pinned path that cannot run is the same outage as no pin at all.
GIT="${GIT:-}"
MG=""
POGO_CLI=""

ts()  { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "[$(ts)] $*"; }
err() { echo "[$(ts)] ERROR: $*" >&2; }

# ---------------------------------------------------------------------------
# 1. Window guard
# ---------------------------------------------------------------------------
# StartCalendarInterval is not a promise that the job runs at 03:00 — it is a
# promise that the job runs. If the mac is asleep at 03:00 the fire is deferred
# and launchd delivers it on the next wake, which is whenever Daniel opens the
# lid: 09:00, 14:00, mid-demo. A redeploy bounces the entire fleet, so a
# deferred fire must be DROPPED, not honoured late.
#
# The window is a RANGE and not an instant for the opposite failure: a mac that
# wakes at 04:15 for a Time Machine run should still get its deploy. Too narrow
# and the job silently never deploys — which reads identically to a job that was
# never installed. 02:00–06:00 is wide enough to catch a nearby wake and narrow
# enough that nobody is working.
#
# It widened from 2-5 to 2-6 with mg-8f7e, and the extra hour is not slack: the
# drain budget is now derived from the distance to WINDOW_END, so the window's
# width IS the deploy's patience. 06:00 is still comfortably before anybody is
# at the machine, and it buys the 03:00 fire a two-hour drain where 05:00 would
# have capped it at 100 minutes.
#
# Half-open [START, END): hour 6 is out, so a 06:00 wake does not deploy into
# the start of the day.
parse_window() {
    local spec="$1"
    case "$spec" in
        [0-9]*-[0-9]*) : ;;
        *) return 1 ;;
    esac
    WINDOW_START=$(( 10#${spec%%-*} ))
    WINDOW_END=$(( 10#${spec##*-} ))
    { [ "$WINDOW_START" -ge 0 ] && [ "$WINDOW_END" -le 24 ] && [ "$WINDOW_START" -lt "$WINDOW_END" ]; }
}

# in_window HOUR START END — half-open [START,END). Base-10 forced: `date +%H`
# emits "08"/"09", which bash reads as invalid OCTAL and errors under set -u,
# turning a routine 08:00 catch-up fire into a crash instead of a skip.
in_window() {
    local h=$(( 10#$1 )) start=$(( 10#$2 )) end=$(( 10#$3 ))
    [ "$h" -ge "$start" ] && [ "$h" -lt "$end" ]
}

current_hour() {
    if [ -n "${POGO_DEPLOY_NOW:-}" ]; then printf '%s' "$POGO_DEPLOY_NOW"; else date +%H; fi
}

# now_hms — the local clock as "H M S", honouring POGO_DEPLOY_NOW so a control
# can place a run anywhere in (or out of) the window. Under the override minutes
# and seconds are zero: a test that says "it is 05:00" means the top of the hour,
# not five o'clock plus whatever the real wall clock happens to read.
now_hms() {
    if [ -n "${POGO_DEPLOY_NOW:-}" ]; then printf '%s 0 0' "$POGO_DEPLOY_NOW"; else date '+%H %M %S'; fi
}

# deploy_date — the night this fire belongs to. The window never crosses
# midnight (parse_window rejects an inverted range), so the calendar date is the
# night's identity and no rollover arithmetic is needed.
deploy_date() {
    if [ -n "${POGO_DEPLOY_DATE:-}" ]; then printf '%s' "$POGO_DEPLOY_DATE"; else date +%F; fi
}

# ---------------------------------------------------------------------------
# 1b. Drain budget (mg-8f7e)
# ---------------------------------------------------------------------------
# See the header for why this is derived rather than a constant. The arithmetic
# is deliberately plain integer maths on H/M/S rather than `date -j` epoch
# parsing: this runs at 03:00 as the only thing standing between a merge and
# activation, and a date-format dependency is one more way for it to fail on a
# host where nobody is watching.
#
# Returns 0 — not a negative number — when the remaining window is below the
# floor. "Zero seconds" is the caller's cue to skip; a negative budget handed on
# to --drain-timeout would be read as an immediate timeout and produce a fake
# exit 7 rather than an honest skip.
drain_budget() {
    local end=$(( 10#$1 )) reserve=$(( 10#$2 )) max=$(( 10#$3 )) min=$(( 10#$4 ))
    local h m s left
    if [ $# -ge 7 ]; then h="$5"; m="$6"; s="$7"; else read -r h m s <<<"$(now_hms)"; fi
    left=$(( end * 3600 - (10#$h * 3600 + 10#$m * 60 + 10#$s) - reserve ))
    [ "$left" -gt "$max" ] && left="$max"
    [ "$left" -lt "$min" ] && left=0
    printf '%s' "$left"
}

# next_fire_hour NOW_H [HOURS] — the next scheduled fire strictly after NOW_H;
# non-zero when tonight has none left. FIRE_HOURS must stay sorted ascending.
next_fire_hour() {
    local now=$(( 10#$1 )) hours="${2:-$FIRE_HOURS}" h
    for h in $hours; do
        if [ $(( 10#$h )) -gt "$now" ]; then printf '%s' "$(( 10#$h ))"; return 0; fi
    done
    return 1
}

# retry_will_follow RC — will a later fire tonight actually try again? Three
# things all have to hold, and asserting it without checking them is how the RED
# alert came to claim "did not retry" as an unconditional fact.
retry_will_follow() {
    local rc="$1" nxt
    rc_reopens_night "$rc" || return 1
    nxt="$(next_fire_hour "$(current_hour)")" || return 1
    [ "$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN" "$nxt" 0 0)" -gt 0 ]
}

# ---------------------------------------------------------------------------
# 1c. The night's attempt record (mg-8f7e)
# ---------------------------------------------------------------------------
# One line: "<date> <attempts> <last_rc>". It exists so a 04:00 fire can tell
# the two nights apart that otherwise look identical from inside it — one where
# 03:00 stalled on a busy fleet (retry: the fleet may have thinned) and one
# where 03:00 failed on the build (do not retry: it will fail the same way and
# mail a second identical alert).
#
# An unreadable or absent record reads as "first attempt", which is the same
# behaviour this job had before the record existed. Failing that way round
# means a corrupt stamp costs at most one extra deploy attempt, where the
# opposite default would silently disable the nightly.
stamp_write() {
    mkdir -p "$(dirname "$1")" 2>/dev/null
    printf '%s %s %s\n' "$2" "$3" "$4" > "$1"
}

stamp_read() { cat "$1" 2>/dev/null || true; }

# rc_reopens_night RC — which recorded outcomes a LATER fire should retry.
#
# The same discriminator sync_class_retryable applies, one layer up (mg-0d70):
# would re-running plausibly give a different answer for a reason unrelated to
# the code?
#
#    7  the drain stalled. The fleet was busy; by 04:00 it may not be.
#   10  the sync aborted on a class that established nothing — the network was
#       unreachable, or the far end refused for a reason a TCP handshake cannot
#       separate from a 5xx. The repo state is simply unknown.
#
# Everything else settled the night by establishing something: a build failure, a
# do_prove RED, a dirty tree and a diverged branch are each exactly as true an
# hour later, and re-running them mails a duplicate alert for no new information.
#
# Exit 10 is THIS runner's code, not pogo-self-deploy's — that script's range
# ends at 9 and this outcome happens before it is ever invoked.
rc_reopens_night() {
    case "${1:-}" in
        7|10) return 0 ;;
        *) return 1 ;;
    esac
}

# attempt_disposition TODAY LINE -> first | retry | settled
attempt_disposition() {
    local today="$1" line="$2" d n rc
    [ -n "$line" ] || { echo first; return 0; }
    read -r d n rc <<<"$line"
    : "$n"
    case "${rc:-}" in
        ''|*[!0-9]*) echo first; return 0 ;;
    esac
    [ "$d" = "$today" ] || { echo first; return 0; }
    if rc_reopens_night "$rc"; then echo retry; else echo settled; fi
}

# stamp_rc LINE — the exit code the last attempt recorded, or empty. The retry
# log line names it rather than asserting "exited 7": since mg-0d70 there are two
# codes that reopen a night, and a message that hardcodes one of them is the same
# species of defect as an alert that hardcodes one remedy.
stamp_rc() {
    local line="$1" rc
    [ -n "$line" ] || return 1
    read -r _ _ rc <<<"$line"
    case "${rc:-}" in
        ''|*[!0-9]*) return 1 ;;
    esac
    printf '%s' "$rc"
}

# stamp_attempts LINE TODAY — attempts already made tonight (0 for another night).
stamp_attempts() {
    local line="$1" today="$2" d n
    [ -n "$line" ] || { echo 0; return 0; }
    read -r d n _ <<<"$line"
    if [ "$d" = "$today" ] && [ -n "$n" ] && [ -z "${n//[0-9]/}" ]; then echo "$n"; else echo 0; fi
}

# ---------------------------------------------------------------------------
# 2. Tool resolution — identity, not existence (mg-015f / mg-dd5f)
# ---------------------------------------------------------------------------
# /usr/bin/mg satisfies -x and `command -v mg`; it is the Micro-Emacs editor.
# Locating a candidate is never the same as trusting it, so each must
# self-identify as macguffin before it is accepted. GOPATH candidates are tried
# FIRST so the production run — launchd PATH, /usr/bin ahead of ~/go/bin —
# resolves the real binary without consulting PATH at all.
resolve_mg() {
    local cand gobin gopath pathmg
    local -a cands=()
    gobin="$(go env GOBIN 2>/dev/null)"
    [ -n "$gobin" ] && cands+=("$gobin/mg")
    gopath="$(go env GOPATH 2>/dev/null)"
    [ -n "$gopath" ] && cands+=("$gopath/bin/mg")
    [ -n "${GOPATH:-}" ] && cands+=("$GOPATH/bin/mg")
    cands+=("$HOME/go/bin/mg")
    pathmg="$(command -v mg 2>/dev/null)"
    [ -n "$pathmg" ] && cands+=("$pathmg")

    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" --help 2>/dev/null | grep -q 'macguffin' || continue
        MG="$cand"
        log "mg: resolved macguffin at $MG"
        return 0
    done
    err "mg: no macguffin 'mg' among ${cands[*]} — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR)"
    return 1
}

# `git`, resolved by EXECUTION rather than by existence, for the reason in the
# header: /usr/bin/git is the Command Line Tools shim, and a damaged CLT leaves
# it executable, on PATH, and unable to complete a single call. `-x` and
# `command -v` both say yes about it. Requiring "git version" on stdout is the
# cheapest question that only a working git can answer.
#
# A real Homebrew/local git is preferred over the shim because the shim is the
# fragile one, but the shim stays in the list so a CLT-only box still deploys.
# On a host where it is the only git present, this list has exactly one entry
# and the preference never comes up.
#
# Factored out from resolve_git so the tests can substitute a list of fakes. On
# a host with a healthy git the real list can never produce a rejection, and an
# execution check that has only ever been exercised against a working binary has
# not been tested at all.
git_candidates() {
    local -a cands=()
    [ -n "${GIT:-}" ] && cands+=("$GIT")
    cands+=("/opt/homebrew/bin/git" "/usr/local/bin/git" "/usr/bin/git")
    local onpath; onpath="$(command -v git 2>/dev/null)"
    [ -n "$onpath" ] && cands+=("$onpath")
    printf '%s\n' "${cands[@]}"
}

resolve_git() {
    local cand
    local -a cands=()
    while IFS= read -r cand; do
        [ -n "$cand" ] && cands+=("$cand")
    done < <(git_candidates)

    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" --version 2>/dev/null | grep -q '^git version' || continue
        GIT="$cand"
        log "git: resolved working git at $GIT ($("$cand" --version 2>/dev/null))"
        return 0
    done
    err "git: no WORKING git among ${cands[*]:-<none>} — a present-but-broken /usr/bin/git (damaged Xcode CLT) fails every call with exit 71, so existence proves nothing"
    return 1
}

# The `pogo` CLI, wanted for the post-bounce schedule read and for events emit.
# Same absolute-path discipline; the identity check is cheaper (there is no
# same-named impostor in /usr/bin) but PATH still may not contain it.
resolve_pogo() {
    local cand
    local -a cands=()
    [ -n "${POGO_BIN:-}" ] && cands+=("$POGO_BIN")
    cands+=("$HOME/go/bin/pogo" "$HOME/.local/bin/pogo")
    cand="$(command -v pogo 2>/dev/null)"
    [ -n "$cand" ] && cands+=("$cand")
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        POGO_CLI="$cand"
        log "pogo: resolved CLI at $POGO_CLI"
        return 0
    done
    err "pogo: no 'pogo' CLI found among ${cands[*]}"
    return 1
}

# ---------------------------------------------------------------------------
# 3. GH_TOKEN at run time
# ---------------------------------------------------------------------------
# Bash cannot `source` a zsh file safely, and we do not want to run arbitrary
# init anyway — we want ONE variable. So: match the export line, eval that line
# alone. If GH_TOKEN is already in the environment (an operator running this by
# hand from a shell) we keep it and read nothing.
#
# The value is never logged. Callers get "present"/"absent", which is the only
# fact any diagnostic needs; a prefix echo is just as leakable as the whole
# token and the transcript outlives the run.
load_gh_token() {
    local f="${1:-$ZSHENV}" line
    if [ -n "${GH_TOKEN:-}" ]; then
        log "GH_TOKEN: already present in the environment"
        return 0
    fi
    if [ ! -r "$f" ]; then
        err "GH_TOKEN: cannot read $f"
        return 1
    fi
    line="$(grep -E '^[[:space:]]*export[[:space:]]+GH_TOKEN=' "$f" 2>/dev/null | tail -1)"
    if [ -z "$line" ]; then
        err "GH_TOKEN: no 'export GH_TOKEN=' line in $f"
        return 1
    fi
    eval "$line" || { err "GH_TOKEN: could not evaluate the export line in $f"; return 1; }
    if [ -z "${GH_TOKEN:-}" ]; then
        err "GH_TOKEN: the export line in $f yielded an empty value"
        return 1
    fi
    export GH_TOKEN
    log "GH_TOKEN: sourced from $f (present, ${#GH_TOKEN} chars)"
    return 0
}

# ---------------------------------------------------------------------------
# 4. Alerting
# ---------------------------------------------------------------------------
# Two channels, on purpose. The event is for the digest and is best-effort; the
# mail is the loud half. `human` is always copied on a RED because a deploy that
# refused to deploy is a thing the operator has to know about by morning, and a
# fleet agent reading it is not the same as the operator seeing it.
alert() {
    local subject="$1" body="$2" bf rc=0
    err "ALERT: $subject"
    printf '%s\n' "$body" >&2

    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type=deploy_nightly_failed --agent=pogo-deploy \
            --details="{\"subject\":\"$subject\"}" >/dev/null 2>&1 || true
    fi
    [ -n "$MG" ] || { err "alert: no macguffin resolved — NOTHING WAS MAILED"; return 1; }

    bf="$(mktemp)" || { err "alert: cannot create a body file"; return 1; }
    printf '%s\n' "$body" > "$bf"
    # --body-file, not --body: the shell expands $VAR / $(cmd) inside a --body
    # string before mg ever sees it, and mg reports Delivered on the mangled
    # text (mg-8380). These bodies carry log paths and remedies verbatim.
    "$MG" mail send "$ALERT_TO" --from=pogo-deploy --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || { err "alert: mail to '$ALERT_TO' failed"; rc=1; }
    "$MG" mail send human --from=pogo-deploy --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || { err "alert: mail to 'human' failed"; rc=1; }
    rm -f "$bf"
    [ "$rc" -eq 0 ] && log "alert: mailed '$ALERT_TO' and 'human'"
    return $rc
}

# ---------------------------------------------------------------------------
# 5. Safe sync of the dedicated checkout
# ---------------------------------------------------------------------------
# A DEDICATED checkout, not /Users/daniel/dev/pogo. The dev tree is a place a
# human works: at 03:00 it may hold a half-finished edit, a rebase in progress,
# or a branch that is not main. Nothing this job does should be able to touch
# it, and the strongest way to guarantee that is to never name it. The only read
# of the dev tree here is `git remote get-url origin` during the one-time clone.
#
# Even in the dedicated tree, DIRTY ABORTS. It should never be dirty; if it is,
# something is going on that nobody has explained, and a reset would destroy the
# evidence of it. Fail loudly, deploy nothing.
#
# ---------------------------------------------------------------------------
# 5a. Classifying a transport failure by MEASUREMENT, not by prose (mg-0d70)
# ---------------------------------------------------------------------------
# remote_endpoint URL -> "HOST PORT", non-zero when the URL has no TCP endpoint
# to probe (a local path, a file:// URL, an empty string). Every form git accepts
# for a remote is handled by its scheme, which is structure; nothing here reads
# an error message.
remote_endpoint() {
    local url="${1:-}" rest host="" port="" after
    case "$url" in
        ssh://*)     rest="${url#ssh://}";     port=22 ;;
        git+ssh://*) rest="${url#git+ssh://}"; port=22 ;;
        https://*)   rest="${url#https://}";   port=443 ;;
        http://*)    rest="${url#http://}";    port=80 ;;
        git://*)     rest="${url#git://}";     port=9418 ;;
        ''|file://*|/*|./*|../*) return 1 ;;
        # scp-like: [user@]host:path. No scheme, so the FIRST colon separates
        # the host from the path — a port cannot appear in this form.
        *:*)
            host="${url%%:*}"
            host="${host#*@}"
            [ -n "$host" ] || return 1
            printf '%s %s' "$host" 22
            return 0 ;;
        *) return 1 ;;
    esac
    rest="${rest%%/*}"   # drop the path
    rest="${rest#*@}"    # drop any user
    case "$rest" in
        \[*\]*)          # bracketed IPv6, optionally :port
            host="${rest%%]*}"; host="${host#[}"
            after="${rest##*]}"
            case "$after" in :*) port="${after#:}" ;; esac ;;
        *:*) host="${rest%%:*}"; port="${rest##*:}" ;;
        *)   host="$rest" ;;
    esac
    [ -n "$host" ] || return 1
    printf '%s %s' "$host" "$port"
}

# THE PROBE, AND WHY IT IS NOT JUST A /dev/tcp REDIRECT
# ------------------------------------------------------
# This was first written as a bare bash /dev/tcp redirect, on the reasoning that
# a BUILTIN needs no binary resolved at 03:00 and cannot have an impostor — the
# same argument the mg-015f / mg-dd5f / mg-b72a sections make about `mg` and
# `git`. That reasoning was fine and the conclusion was wrong, and it was wrong
# ON THIS BOX. Measured here:
#
#     /bin/bash 3.2.57            exec 3<>/dev/tcp/github.com/22   HANGS
#     /bin/bash 3.2.57            exec 3<>/dev/tcp/20.26.156.215/22 connects
#     /usr/bin/nc                 nc -z -w 5 github.com 22          connects
#     python3, ssh                                                  connect
#
# macOS's bash 3.2 hangs resolving a hostname in a /dev/tcp redirect while
# connecting happily to a literal IP. Since the deploy remote is a HOSTNAME, the
# probe would have timed out every single time and reported "unreachable" —
# which would have classified a rejected key, a 5xx and a real outage
# identically as `network`. That is this ticket's own defect, rebuilt inside its
# own fix: a component asserting a cause it had not established.
#
# It survived the first round of tests because the positive control probed
# 127.0.0.1 — a literal IP. The control could not fail for the reason the probe
# actually fails, so it passed while the probe was broken. A check invariant
# under the failure it guards fires for nothing.
#
# So there are two changes. `nc` is resolved by EXECUTION, like every other
# primitive here, and preferred because it does its own resolution. And the
# probe now has THREE answers rather than two.
#
# probe_tcp HOST PORT [TIMEOUT]:
#     0  reachable            — a definite yes
#     1  unreachable          — a definite no, from a primitive that can give one
#     2  could not probe      — no answer. NOT a no.
#
# The third is the whole correction. A timed-out probe, or a primitive that
# cannot be trusted to distinguish "unreachable" from "I broke", must not
# produce a verdict — it yields `unclassified`, which prints the underlying
# error verbatim and names no cause. Concretely: with a proven `nc` the probe
# can answer all three; with only /dev/tcp it may answer 0 or 2 and NEVER 1,
# because on a box like this one its failure means nothing about the network.
NC=""
NC_FLAGS=""

resolve_nc() {
    local cand
    local -a cands=()
    # -G is macOS's CONNECT timeout. Measured here: `nc -z -w 5` alone does NOT
    # bound the connect on Darwin — a blackholed host ran 12s past it — while -G
    # does. Linux's nc has no -G and bounds connects with -w, so the flag is
    # chosen by OS rather than by parsing an error message.
    if [ "$(uname -s 2>/dev/null)" = "Darwin" ]; then NC_FLAGS="-G 5"; else NC_FLAGS=""; fi
    [ -n "${POGO_DEPLOY_NC:-}" ] && cands+=("$POGO_DEPLOY_NC")
    cands+=("/usr/bin/nc" "/opt/homebrew/bin/nc" "/usr/local/bin/nc")
    cand="$(command -v nc 2>/dev/null)"
    [ -n "$cand" ] && cands+=("$cand")
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        # Proved by execution, and specifically by its ability to say NO: port 1
        # on loopback is closed everywhere, so a working nc refuses it with
        # exit 1 in well under a second. A missing binary exits 127, a
        # non-executable one 126, and one that cannot parse these flags does not
        # reach the connect at all — none of which is a 1.
        "$cand" $NC_FLAGS -w 2 -z 127.0.0.1 1 >/dev/null 2>&1
        [ "$?" -eq 1 ] || continue
        NC="$cand"
        log "nc: resolved a probe that can return a definite refusal at $NC ($NC_FLAGS)"
        return 0
    done
    log "nc: none usable — the reachability probe can confirm a host is UP but will never assert one is DOWN, so a failed sync stays 'unclassified' rather than being called the network"
    return 1
}

# The connect runs in a background subshell with a killer beside it, because an
# unreachable host does not fail fast: a silently-dropped SYN sits in the kernel
# for ~75s, which would delay the alert past the point of being useful.
probe_tcp() {
    local host="$1" port="$2" timeout="${3:-5}" p k rc
    if [ -n "$NC" ]; then
        ( "$NC" $NC_FLAGS -w "$timeout" -z "$host" "$port" ) >/dev/null 2>&1 &
    else
        ( exec 3<>"/dev/tcp/$host/$port" ) >/dev/null 2>&1 &
    fi
    p=$!
    ( sleep "$timeout"; kill -9 "$p" ) >/dev/null 2>&1 &
    k=$!
    wait "$p" 2>/dev/null; rc=$?
    kill "$k" >/dev/null 2>&1
    wait "$k" 2>/dev/null
    case "$rc" in
        0) return 0 ;;
        # Killed by the watchdog. No answer was obtained, from either primitive.
        137|143) return 2 ;;
        *)
            # A non-zero from a PROVEN nc is a real refusal. The same from
            # /dev/tcp is not evidence of anything: on this box that is what a
            # resolver failure looks like, and it is indistinguishable from an
            # unsupported redirect.
            [ -n "$NC" ] && return 1
            return 2 ;;
    esac
}

# classify_transport URL — sets SYNC_CLASS to `network`, `remote`, or
# `unclassified` after a clone/fetch has already failed.
#
# The asymmetry is deliberate. A REACHABLE endpoint is a positive measurement,
# and it rules the network out: the fetch failed for some other reason, which is
# auth, permission, or the repository. An UNREACHABLE one is the network. And
# when there is no endpoint to probe the answer is `unclassified` — not the most
# likely cause, which is precisely the guess that produced the 08-05 alert.
classify_transport() {
    local url="${1:-}" host port rc
    read -r host port <<<"$(remote_endpoint "$url")"
    if [ -z "${host:-}" ] || [ -z "${port:-}" ]; then
        SYNC_CLASS=unclassified
        log "sync: no TCP endpoint could be derived from remote '$url' — NOT classifying the failure"
        return 0
    fi
    probe_tcp "$host" "$port" "$PROBE_TIMEOUT"; rc=$?
    case "$rc" in
        0)  SYNC_CLASS=remote
            log "sync: $host:$port ANSWERED a TCP connection — connectivity is up, so the failure is at the far end (auth, permission, or the repository)" ;;
        1)  SYNC_CLASS=network
            log "sync: $host:$port REFUSED a TCP connection — NETWORK-class failure" ;;
        # The correction that matters: no answer is not a no. Naming the network
        # here on a probe that merely failed to complete would be this ticket's
        # own defect, committed by its own fix.
        *)  SYNC_CLASS=unclassified
            log "sync: the reachability probe for $host:$port returned no answer within ${PROBE_TIMEOUT}s — that is NOT evidence the network is down, so the failure stays unclassified and the error is reported verbatim" ;;
    esac
}

# git_step CMD... — run a git step, keep its stderr in SYNC_DETAIL, and still
# print it. The log is the operator's primary artifact and losing the ssh line
# from it to gain it in the mail would be a straight trade, not an improvement.
git_step() {
    local f rc
    f="$(mktemp)" || { SYNC_DETAIL=""; "$@"; return $?; }
    "$@" 2>"$f"; rc=$?
    SYNC_DETAIL="$(cat "$f" 2>/dev/null)"
    [ -s "$f" ] && cat "$f" >&2
    rm -f "$f"
    return "$rc"
}

sync_src() {
    SYNC_CLASS=""; SYNC_DETAIL=""
    local remote=""
    if [ ! -d "$SRC/.git" ]; then
        remote="$DEPLOY_REMOTE"
        if [ -z "$remote" ]; then
            remote="$("$GIT" -C "$BOOTSTRAP_REPO" remote get-url origin 2>/dev/null)"
        fi
        if [ -z "$remote" ]; then
            SYNC_CLASS=config
            SYNC_DETAIL="no POGO_DEPLOY_REMOTE, and 'git -C $BOOTSTRAP_REPO remote get-url origin' produced nothing"
            err "sync: no clone URL (set POGO_DEPLOY_REMOTE) and could not read origin from $BOOTSTRAP_REPO"
            return 1
        fi
        log "sync: bootstrapping the dedicated checkout at $SRC from $remote"
        if ! git_step "$GIT" clone --quiet "$remote" "$SRC"; then
            err "sync: clone failed"
            classify_transport "$remote"
            return 1
        fi
    fi

    if ! git_step "$GIT" -C "$SRC" fetch --quiet origin; then
        err "sync: git fetch origin failed in $SRC"
        remote="$("$GIT" -C "$SRC" remote get-url origin 2>/dev/null)"
        [ -n "$remote" ] || remote="$DEPLOY_REMOTE"
        classify_transport "$remote"
        return 1
    fi

    if [ -n "$("$GIT" -C "$SRC" status --porcelain 2>/dev/null)" ]; then
        SYNC_CLASS=dirty
        SYNC_DETAIL="$("$GIT" -C "$SRC" status --short 2>&1)"
        err "sync: $SRC is DIRTY — refusing to touch it"
        printf '%s\n' "$SYNC_DETAIL" >&2
        return 1
    fi

    if ! git_step "$GIT" -C "$SRC" checkout --quiet "$DEPLOY_REF"; then
        SYNC_CLASS=checkout
        err "sync: cannot checkout $DEPLOY_REF in $SRC"
        return 1
    fi
    # --ff-only: a deploy tree that has diverged from origin has commits nobody
    # meant to build. Merging them would deploy them; resetting would erase
    # them. Refuse, and let a human look.
    if ! git_step "$GIT" -C "$SRC" merge --ff-only --quiet "origin/$DEPLOY_REF"; then
        SYNC_CLASS=diverged
        err "sync: $SRC has DIVERGED from origin/$DEPLOY_REF — refusing a non-fast-forward"
        return 1
    fi
    log "sync: $SRC at $DEPLOY_REF $("$GIT" -C "$SRC" rev-parse --short HEAD)"
    return 0
}

# ---------------------------------------------------------------------------
# 5b. The in-run retry (mg-0d70)
# ---------------------------------------------------------------------------
# sync_backoff N LIST — the delay before attempt N+1. The last entry repeats, so
# a shortened list degrades into a constant rather than into zero.
sync_backoff() {
    local n="$1" list="${2:-$SYNC_BACKOFF}" i=1 d last=0
    for d in $list; do
        last="$d"
        [ "$i" -eq "$n" ] && { printf '%s' "$d"; return 0; }
        i=$(( i + 1 ))
    done
    printf '%s' "$last"
}

# sync_class_retryable CLASS — THE DISCRIMINATOR (pm-pogo's ruling, mg-0d70):
#
#     would re-running plausibly give a different answer, for a reason
#     UNRELATED TO THE CODE? If yes, retry.
#
# It is a question about what the failure ESTABLISHED, not about how annoying it
# was. The split falls exactly where sync_src stops:
#
#   RETRYABLE — the transport classes. `network`, `remote` and `unclassified`
#     all mean the sync never reached the tree, so NOTHING about the repository
#     state was established. The state is simply unknown and re-asking is how
#     you find out. `remote` is retryable despite naming the far end, because it
#     conflates a stable cause (a rejected key) with transient ones the ruling
#     lists by name (GitHub 5xx, rate limiting) — and a TCP handshake cannot
#     separate them without reading prose, which this file will not do. Given
#     that conflation the asymmetry decides it: retrying a genuinely dead key
#     costs ~3 minutes of a 4-hour window, once, logged; not retrying a 5xx
#     costs the whole night. That is the same asymmetry the ticket is about.
#
#   NOT RETRYABLE — the classes that established a FACT. `dirty` and `diverged`
#     are statements about the tree, `checkout` about the ref, and `config`
#     about this box's setup: each is exactly as true in thirty seconds, so a
#     retry only re-establishes it, burns the window, and risks reading as flake.
#
# Note what is NOT in scope here: `do_prove` RED, a build failure and a test
# failure are pogo-self-deploy's exits, they establish facts, and gate 3's
# attempt_disposition already settles the night on every one of them.
sync_class_retryable() {
    case "${1:-}" in
        network|remote|unclassified) return 0 ;;
        *) return 1 ;;
    esac
}

# sync_with_retry — the retry itself, under the ruling's three conditions:
#
#   1. BOUNDED AND LOGGED PER ATTEMPT. Every attempt logs its own number and
#      class, the total lands in SYNC_TRIES, and the alert prints it — so
#      "failed once" and "failed after four attempts" are different sentences in
#      both places. A retry that hides how hard it tried is a retry that makes
#      the next incident harder to read than this one was.
#   2. RETRIES STAY INSIDE THE WINDOW. Backoff is charged against the drain
#      budget twice over: SYNC_RETRY_BUDGET caps the total, and every individual
#      sleep is refused if the window would not still afford a drain on the far
#      side of it. Retries consume the existing allowance; they never extend it.
#   3. A RETRIED SUCCESS SAYS SO. The winning attempt is named in the log, in an
#      event, and in mail — see sync_recovery_notice. A silent retry converts a
#      flaky night into an invisible one, and invisible is how this box's network
#      came to be the dominant failure mode without anybody having the evidence.
#
# SYNC_RETRY_SPENT is left behind for the caller, which owes the drain a budget
# recomputed against the time this actually took. SYNC_VIGIL_SPENT is the part of
# it the vigil tier accounts for, kept apart so the alert can report the observed
# outage duration rather than one undifferentiated sleep total (mg-5515).
SYNC_RETRY_SPENT=0
SYNC_BLIP_SPENT=0
SYNC_VIGIL_SPENT=0
SYNC_TRIES=0

# sync_next_delay ATTEMPT — prints "<tier> <seconds>" for the wait before attempt
# N+1, or returns 1 when both tiers are out of patience.
#
# The tiers are tried in order and the blip tier's two stop conditions HAND OVER
# to the vigil rather than ending the run: exhausting four fast attempts is what
# tells you this is an outage and not a blip, which is precisely the moment the
# vigil is for. Before mg-5515 that same moment ended the run.
sync_next_delay() {
    local attempt="$1" delay
    if [ "$attempt" -lt "$SYNC_ATTEMPTS" ]; then
        delay="$(sync_backoff "$attempt")"
        if [ $(( SYNC_BLIP_SPENT + delay )) -le "$SYNC_RETRY_BUDGET" ]; then
            printf 'blip %s' "$delay"; return 0
        fi
    fi
    [ "$SYNC_VIGIL" = "1" ] || return 1
    printf 'vigil %s' "$SYNC_VIGIL_INTERVAL"
}

# touch_lock — keep the deploy lock's mtime fresh while the vigil sleeps.
#
# acquire_lock reclaims a lock whose DIRECTORY MTIME is older than
# STALE_LOCK_MIN (180 min), and before the vigil no run could hold one that long.
# A vigil started by a 02:00 wake-fire runs to ~05:30, which is 210 minutes — so
# without this the 05:00 fire would reclaim a lock that is being held by a live
# run and start a competing deploy. Refreshing it is also what makes the
# threshold mean what it says: "no run has made progress in 180 minutes", not
# "some run started 180 minutes ago".
touch_lock() {
    [ -d "$LOCK_DIR" ] && touch "$LOCK_DIR" 2>/dev/null
    return 0
}

sync_with_retry() {
    local attempt=1 tier delay left next
    SYNC_RETRY_SPENT=0
    SYNC_BLIP_SPENT=0
    SYNC_VIGIL_SPENT=0
    while :; do
        SYNC_TRIES="$attempt"
        if sync_src; then
            [ "$attempt" -gt 1 ] && log "sync: RECOVERED — attempt $attempt succeeded after ${SYNC_RETRY_SPENT}s of waiting$([ "$SYNC_VIGIL_SPENT" -gt 0 ] && printf ', %ss of it a vigil that sat the outage out' "$SYNC_VIGIL_SPENT"). Attempt 1 failed on a transient cause and under the pre-mg-0d70 policy would have ended the night here."
            return 0
        fi
        err "sync: attempt $attempt failed — class=${SYNC_CLASS:-unclassified}"
        if ! sync_class_retryable "$SYNC_CLASS"; then
            log "sync: class=${SYNC_CLASS:-unclassified} ESTABLISHED a fact about the tree or this box's setup — re-running would only re-establish it. Not retrying."
            return 1
        fi
        if ! next="$(sync_next_delay "$attempt")"; then
            err "sync: $attempt attempts over ${SYNC_RETRY_SPENT}s exhausted both the blip tier and the vigil — stopping"
            return 1
        fi
        read -r tier delay <<<"$next"
        # Condition 2 of mg-0d70's ruling, enforced rather than asserted, and now
        # the vigil's ONLY bound: the sleep is only taken if the window would
        # still afford a drain once it is over. Retrying past the point where a
        # deploy could still happen spends the fleet's window to arrive at the
        # same skip, later and with less of it left.
        left="$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
        if [ "$left" -le "$delay" ]; then
            if [ "$tier" = vigil ]; then
                err "sync: the vigil ends — after ${SYNC_RETRY_SPENT}s (${SYNC_VIGIL_SPENT}s of it vigil) across $attempt probes the transport was still unreachable, and a further ${delay}s would leave under ${MIN_DRAIN}s of usable window. The outage outlasted tonight's window; it was NOT a shortage of fires."
            else
                err "sync: a ${delay}s backoff would leave under ${MIN_DRAIN}s of usable window — retries consume the deploy budget, they do not extend it. Stopping after $attempt attempts."
            fi
            return 1
        fi
        if [ "$tier" = vigil ]; then
            log "sync: VIGIL probe — attempt $attempt failed on ${SYNC_CLASS}, which established nothing, and the blip tier is spent. This is an outage, not a blip: re-probing every ${delay}s for as long as the window can still afford a drain (${left}s usable; ${SYNC_VIGIL_SPENT}s of vigil so far)."
            touch_lock
        else
            log "sync: attempt $attempt failed on a class that established nothing (${SYNC_CLASS}) — retrying in ${delay}s (attempt $(( attempt + 1 )) of $SYNC_ATTEMPTS; ${left}s of window still usable)"
        fi
        sleep "$delay"
        SYNC_RETRY_SPENT=$(( SYNC_RETRY_SPENT + delay ))
        if [ "$tier" = vigil ]; then
            SYNC_VIGIL_SPENT=$(( SYNC_VIGIL_SPENT + delay ))
        else
            SYNC_BLIP_SPENT=$(( SYNC_BLIP_SPENT + delay ))
        fi
        attempt=$(( attempt + 1 ))
    done
}

# sync_recovery_notice — condition 3. A night that needed retries mails a
# NOTICE, separately from alert(): alert() always copies `human` because a
# refused deploy is something Daniel must know by morning, and a night that
# WORKED is not. This goes to ALERT_TO alone (the coordinator by default), which
# is where the evidence accumulates — every recovered night is one more data
# point about the host's dominant failure mode, and the log alone is what nobody
# reads.
sync_recovery_notice() {
    local n="$1" spent="$2" bf vigil="${SYNC_VIGIL_SPENT:-0}" measured=""
    [ "$n" -gt 1 ] || return 0
    # Built before the heredoc rather than substituted inside it: a nested
    # heredoc in a command substitution inside a heredoc parses, and nobody
    # editing this file at 03:00 should have to be sure of that.
    if [ "$vigil" -gt 0 ]; then
        measured="
The vigil figure above is also a MEASUREMENT: the transport was unreachable from
this box for at least ${vigil}s, observed by probing rather than inferred.
mg-5515 was filed against a sample of one such duration (2h50m on 2026-08-07)
and said the input worth having is a distribution. This is one point in it."
    fi
    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type=deploy_sync_recovered --agent=pogo-deploy \
            --details="{\"attempts\":$n,\"backoff_s\":$spent,\"vigil_s\":$vigil}" >/dev/null 2>&1 || true
    fi
    [ -n "$MG" ] || return 0
    bf="$(mktemp)" || return 0
    cat > "$bf" <<EOF
Tonight's nightly redeploy synced successfully, but NOT on the first try.

  attempt that won: $n (blip tier is $SYNC_ATTEMPTS attempts; anything past that
                    is the mg-5515 vigil)
  waiting spent:    ${spent}s, ${vigil}s of it vigil (charged against the drain
                    budget, not added to it)
  log:              $HOME/Library/Logs/pogo/pogo-deploy.log

This is not a failure and needs no action. It is recorded because a retry that
succeeds silently turns a flaky night into an invisible one, and the count of
these is the evidence for how often this box's network is the thing that breaks
(mg-0d70, mg-0ffc, mg-dd22). If these become routine, the network is the ticket
— not the deploy.
$measured
EOF
    "$MG" mail send "$ALERT_TO" --from=pogo-deploy \
        --subject="[pogo-deploy] NOTICE: tonight's sync needed $n attempts" --body-file "$bf" >/dev/null 2>&1 \
        || err "notice: mail to '$ALERT_TO' failed"
    rm -f "$bf"
    log "sync: recovery notice mailed to $ALERT_TO (attempt $n won)"
}

# describe_sync_class CLASS — one line naming what was established, for the
# alert's subject and summary.
describe_sync_class() {
    case "${1:-}" in
        network)      echo "NETWORK — the remote host could not be reached from this box" ;;
        remote)       echo "REMOTE — the host answered but the transfer was refused (auth, permission, or the repository)" ;;
        dirty)        echo "DIRTY CHECKOUT — the deploy tree has uncommitted changes" ;;
        diverged)     echo "DIVERGED — the deploy tree has commits that are not on the remote" ;;
        checkout)     echo "CHECKOUT — the deploy ref could not be checked out" ;;
        config)       echo "CONFIGURATION — no usable clone URL for the dedicated checkout" ;;
        *)            echo "UNCLASSIFIED — the runner could not establish which of these it was" ;;
    esac
}

# remedy_for_sync_class CLASS — what to DO. The 08-05 alert printed the `dirty`
# paragraph under a `network` failure and sent Daniel to a `git status` that was
# clean, so the one rule these paragraphs obey is that each sends the reader
# somewhere the evidence actually points.
remedy_for_sync_class() {
    case "${1:-}" in
        network)
            cat <<EOF
This box could NOT open a TCP connection to the remote, measured directly at the
time of the failure — so the deploy tree is not the place to look. It is neither
dirty nor diverged; the sync never got far enough to have an opinion about it.

The runner already retried this — $SYNC_TRIES attempts over ${SYNC_RETRY_SPENT}s, ${SYNC_VIGIL_SPENT}s of it
a vigil (mg-5515) that re-probed every ${SYNC_VIGIL_INTERVAL}s for as long as the window could
still afford a drain. So the outage outlasted the whole usable window, not just a
few minutes of backoff, and it was NOT a shortage of retry fires: a fire that
lands inside an outage this long fails on arrival exactly as this one did.
This host's network is independently known to be intermittent (mg-0ffc, mg-dd22).

READ ${SYNC_VIGIL_SPENT}s AS A MEASUREMENT. It is a LOWER BOUND on how long the transport was
unreachable from this box, observed by probing rather than inferred, and it is
the evidence mg-5515 asked for: that ticket had a sample of exactly one duration
(2h50m, 2026-08-07) and could not say whether it was typical.

  ping -c 3 github.com
  ssh -T git@github.com          # 'successfully authenticated' is the good answer
  curl -sS -o /dev/null -w '%{http_code}\\n' https://api.github.com

If connectivity is back, nothing needs fixing here — but do NOT assume a later
fire tonight will carry it. The vigil ran to the edge of the usable window, so
by the time you read this the night is over; the next attempt is tomorrow.
EOF
            ;;
        remote)
            cat <<EOF
This was RETRIED $SYNC_TRIES times over ${SYNC_RETRY_SPENT}s of backoff before the runner gave up,
so whatever it is outlasted that.

The remote host ANSWERED a TCP connection, measured moments AFTER the transfer
failed — so the likely cause is authentication, permission, or the repository
itself rather than connectivity. Two caveats on that, because this is an
inference and not a proof: a blip that had already ended by the time of the
probe presents exactly this way, and a middlebox that completes a handshake and
then resets does too. The probe is a floor on connectivity, not a guarantee.

Note also that git says "make sure you have the correct access rights" for the
network case too, so the message above is not by itself evidence of a key
problem. Do NOT go looking at the deploy tree: it is neither dirty nor diverged,
and the sync never reached the point of inspecting it.

  ssh -T git@github.com
  ssh-add -l
  git -C ~/.pogo/deploy-src remote -v
EOF
            ;;
        dirty)
            cat <<'EOF'
The dedicated checkout has UNCOMMITTED CHANGES, and the exact ones are printed
verbatim above. It should never be dirty — nothing but this job writes to it —
so the interesting question is what put them there. The runner aborts rather
than resetting precisely so that evidence survives for you to read.

  git -C ~/.pogo/deploy-src status
  git -C ~/.pogo/deploy-src diff
EOF
            ;;
        diverged)
            cat <<'EOF'
The dedicated checkout has commits that are NOT on the remote, so the
fast-forward was refused. Merging them would deploy commits nobody meant to
build and resetting would erase them, which is why this aborts instead.

  git -C ~/.pogo/deploy-src log --oneline origin/main..HEAD
EOF
            ;;
        checkout)
            cat <<'EOF'
The fetch succeeded and the tree is clean, but the deploy ref could not be
checked out. The verbatim git error is above; the usual causes are a ref that no
longer exists on the remote and an index left in a bad state.

  git -C ~/.pogo/deploy-src status
  git -C ~/.pogo/deploy-src branch -a
EOF
            ;;
        config)
            cat <<'EOF'
There is no dedicated checkout yet and the runner could not work out where to
clone it from: POGO_DEPLOY_REMOTE is unset and the bootstrap repo yielded no
origin URL. Nothing was fetched and nothing was inspected.

  git -C ~/dev/pogo remote get-url origin
EOF
            ;;
        *)
            cat <<EOF
The runner could NOT establish which kind of failure this was, and is telling you
so rather than naming the most common one. The verbatim error above is the
evidence; read it before assuming anything about the deploy tree, the network or
the credentials.

It was still RETRIED ($SYNC_TRIES attempts over ${SYNC_RETRY_SPENT}s): the failure happened at the
transport step, so the sync never inspected the tree and established nothing
about it. What it did NOT do is guess a cause to go with that.
EOF
            ;;
    esac
}

# ---------------------------------------------------------------------------
# 6. Drift gate
# ---------------------------------------------------------------------------
# `pogo-self-deploy check` is read-only and never acts, and its classifier is
# the one already trusted to decide what a redeploy owes — including CLI drift,
# which it covers deliberately (mg-ddf1). Reusing its verdict rather than
# recomputing "running rev == origin/main" here is the difference between a
# trigger and a second deployer with its own opinion.
#
# no drift -> exit 0 WITHOUT bouncing. A fleet-wide bounce costs every agent its
# session; doing it for a no-op is strictly worse than not running at all.
is_clean_verdict() {
    printf '%s' "$1" | grep -qE '^action[[:space:]]*:[[:space:]]*clean'
}

# ---------------------------------------------------------------------------
# 7. Outcome classification
# ---------------------------------------------------------------------------
# THE REASON CHANNEL COMES FIRST (mg-0155). Everything in this section is the
# FALLBACK. When pogo-self-deploy leaves a reason record, the alert is written
# from the sentence the deploy script itself computed, because that script is the
# only process that knew which failure this was; describe_exit is what the alert
# falls back to when no record was left (an exit before the record was armed, an
# unwritable path, an older deploy script). See deploy_reason_field.
#
# WHY A FALLBACK AND NOT A REPLACEMENT. A code is still worth describing: it says
# roughly WHERE the run stopped, and that is a real fact even with no reason
# attached. What it can no longer be asked to do is carry WHICH failure occurred
# — that is what four generations of this defect were made of (mg-8f7e, mg-65b2,
# mg-0d70, and 2026-08-07's exit 6). So the descriptions below are deliberately
# coarse where the code is coarse, and say so.
#
# exit 9 (do_prove RED) is the important one and the best one: do_prove runs
# AFTER the build and BEFORE the kickstart, so a 9 means the running pogod was
# never touched. It is a clean negative control, not an outage — and the correct
# response is emphatically not to retry with --force.
describe_exit() {
    case "$1" in
        0) echo "OK" ;;
        1) echo "refused before deploying (ancestry guard, unresolvable mg, or unclassifiable drift)" ;;
        2) echo "bad invocation or unusable repo path" ;;
        3) echo "refused: non-interactive without --yes (a bug in this wrapper — it must pass --yes)" ;;
        4) echo "BUILD FAILED (dirty tree, go install, or the post-install revision check)" ;;
        5) echo "launchctl kickstart failed — pogod may be DOWN" ;;
        # 6 was "post-restart verification failed" until mg-0155, and had never
        # been that: every exit-6 site is a drain PRECONDITION refusal, reached
        # before the build. The description sent the 2026-08-07 03:00 reader to
        # look for a half-installed binary after a run that installed nothing.
        # It is coarse on purpose now — four refusals share it (this pogod
        # predates /agents/drain; pogod is not answering; orchestration is
        # stopped; an unexpected status) and the reason record is what separates
        # them. What the code alone honestly settles is the part that matters
        # most: the run stopped before do_build, so nothing was installed.
        6) echo "drain precondition refused, BEFORE the build — nothing was built, installed or restarted (the reason line says which refusal)" ;;
        7) echo "drain stalled" ;;
        8) echo "verify_running failed — the new pogod did not come up" ;;
        9) echo "do_prove RED — the control suite refused the artifact BEFORE the restart; the running pogod is UNTOUCHED" ;;
        # 10 is this runner's own, not pogo-self-deploy's: the sync aborted
        # before the deploy script was ever invoked, on a class that established
        # nothing about the tree (mg-0d70).
        10) echo "sync aborted on a transient class — the repo state was never established, and a later fire retries" ;;
        # 130/143 are the shell's, not either script's: SIGINT and SIGTERM during
        # the drain window, converted to exits so the restore trap runs. They
        # rendered as "unclassified failure" until the mg-0155 enumeration asked
        # for a row per exit path and found two with no story at all — which is
        # how a launchd shutdown mid-deploy would have been reported.
        130) echo "INTERRUPTED (SIGINT) during the drain window — aborted from a terminal; dispatch was restored on the way out and nothing was installed" ;;
        143) echo "TERMINATED (SIGTERM) during the drain window — something killed the deploy (a logout or shutdown will do it); dispatch was restored on the way out and nothing was installed" ;;
        *) echo "unclassified failure" ;;
    esac
}

# ---------------------------------------------------------------------------
# 7a. The reason record (mg-0155)
# ---------------------------------------------------------------------------
# pogo-self-deploy writes one of these to $POGO_DEPLOY_REASON_FILE on every exit
# from `redeploy`. Format, deliberately sed-readable rather than JSON (the deploy
# path stays jq-free — one fewer dependency in the code that has to work when
# everything else is broken):
#
#   exit=6
#   stage=drain
#   installed=no
#   reason=orchestration is STOPPED: POST http://127.0.0.1:10000/agents/drain answered HTTP 503
#   --- verbatim ---
#   <every ERROR line the deploy script emitted, in order>
#
# `installed` is the field that ends a specific class of lie. Whether an install
# happened is a property of how far the run got, not of which code it exited
# with, and the alert used to infer it from the code — which is how a run with
# `elapsed: 0s` was reported as "the binary on disk is the NEW one".
#
# EVERY READ IS TOTAL. A missing file, an empty file, a key that is not there:
# all of them yield empty, and every caller treats empty as "fall back". An
# alerting path that can itself fail on a malformed input is an alerting path
# that goes quiet exactly when something has gone unusually wrong.
deploy_reason_field() {
    local file="$1" key="$2"
    [ -n "$file" ] && [ -f "$file" ] || return 0
    # Stop at the separator so a verbatim line that happens to start with
    # "reason=" cannot be read as the header.
    sed -n "/^--- verbatim ---$/q; s/^${key}=//p" "$file" 2>/dev/null | head -n 1
}

# deploy_reason_detail FILE — the verbatim transcript, or empty.
deploy_reason_detail() {
    local file="$1"
    [ -n "$file" ] && [ -f "$file" ] || return 0
    sed -n '/^--- verbatim ---$/,$p' "$file" 2>/dev/null | sed '1d'
}

# describe_outcome RC FILE — the alert's one-line description of what happened.
#
# The deploy script's own sentence when it left one, describe_exit otherwise.
# This is the whole mechanism: the text is written once, at the site that knows
# which failure it is, and carried — not re-derived from an integer by a process
# that was not there.
describe_outcome() {
    local reason; reason="$(deploy_reason_field "$2" reason)"
    if [ -n "$reason" ]; then printf '%s\n' "$reason"; else describe_exit "$1"; fi
}

# stage_is_past_kickstart STAGE — did this run reach `launchctl kickstart -k`?
#
# The stage names are pogo-self-deploy's, in the order it sets them: startup,
# drain, build, prove, restart, verify, post-check, done. Everything from
# `restart` on has bounced the daemon.
#
# TWO facts, not one. "Was anything installed" and "was pogod bounced" come
# apart on a restart-only deploy, which installs nothing and still kills the
# daemon — so an alert that derives the second from the first tells a kickstart
# failure that "the running pogod is exactly as it was before". That is the
# 2026-08-07 defect rebuilt inside its own fix, and it is the reason the
# enumeration in docs/deploy-exit-paths.md is a deliverable rather than a nicety:
# writing the table is what found it.
stage_is_past_kickstart() {
    case "${1:-}" in
        restart|verify|post-check|done) return 0 ;;
        *) return 1 ;;
    esac
}

# what_the_run_changed FILE — one line on what state the attempt left behind.
#
# Its absence is what let the 03:00 alert assert an install over a run that made
# none. Empty when there is no record: a sentence about what changed is worth
# printing only when something measured it.
what_the_run_changed() {
    local installed stg bounced
    installed="$(deploy_reason_field "$1" installed)"
    stg="$(deploy_reason_field "$1" stage)"
    [ -n "$installed" ] || return 0
    if stage_is_past_kickstart "$stg"; then bounced=yes; else bounced=no; fi
    case "$installed:$bounced" in
        no:no)
            echo "NOTHING. The attempt stopped at the '${stg}' stage, before \`go install\` and before the kickstart, so the binaries on disk and the running pogod are both exactly as they were before it started. There is nothing to roll back." ;;
        no:yes)
            echo "No install (this was a restart-only deploy — the binaries on disk already matched main), but pogod WAS bounced at the '${stg}' stage. The binaries are unchanged; the process is not the one that was running before. Check it is up: \`curl -s http://127.0.0.1:10000/version\`." ;;
        partial:*)
            echo "An install was IN PROGRESS when the attempt stopped (stage '${stg}'), so GOBIN may hold a partial or unverified set. Ask each binary its revision before doing anything else: \`pogod --version\`, \`pogo --version\`." ;;
        yes:no)
            echo "The install COMPLETED and every binary was verified at main (stage '${stg}'), but pogod was NOT bounced — the process still running is the old one. The box is in the split state the next successful deploy resolves; it is not an outage." ;;
        yes:yes)
            echo "The install COMPLETED and pogod WAS bounced (stage '${stg}'). Both the binaries and the process are the new ones, so a failure here is about the new pogod, not about getting it onto the box." ;;
        *) : ;;
    esac
}

# remedy_for_exit CODE — what to DO about this exit, and why.
#
# The RED alert used to carry ONE paragraph, about exit 9, under every code
# (mg-8f7e). Under the 07-31 exit 7 it told the reader that "the control suite
# went RED before the kickstart ... the artifact is the problem" — a description
# of a different failure. Exit 7 never reaches the build, so there is no artifact
# to suspect and nothing in the build log to find. The advice that followed it
# ("read the log, fix the cause, let the next nightly carry it") was right by
# accident, and being right for a stated wrong reason is worse than being wrong:
# a reader who trusts the reasoning goes and looks at the build.
#
# So each code gets the paragraph that is true of it. describe_exit says what
# happened; this says what it means and what to do.
#
# AND IT IS STILL ONLY THE FALLBACK HALF (mg-0155). Correct as far as it goes,
# and it did not go far enough: it assumes one code means one failure, and code 6
# meant four. A paragraph keyed on an integer can only ever say what everything
# sharing that integer has in common — so these paragraphs now say exactly that
# much and no more, and the alert prints the deploy script's own sentence above
# them. Where a code's paragraph would have to assert something only some of its
# failures did (an install, a bounce), it says the common part instead.
# dispatch_freeze_note RC ELAPSED — what the attempt cost the fleet, in seconds.
#
# `draining=true` refuses ALL new polecat dispatch, so a drain is not just a late
# bounce: it is an interval in which no new work starts for anyone. That cost was
# never reported, which made "raise the budget" look free — the architect's
# reading of mg-8f7e, and correct as far as it goes.
#
# Two things bound it, and neither is visible without a number in the log. The
# freeze ends when the fleet quiesces, NOT when the budget runs out: the 07-30
# deploy drained in 3m50s under the same 1800s budget, so a larger budget costs
# nothing on a night that would have succeeded anyway. It is only the nights that
# STALL that pay the full budget and get nothing for it — and that is exactly the
# case this prints, so the trade-off accumulates as measurements instead of
# staying an argument.
#
# Reported for exit 7 only. On every other outcome the drain reached zero and the
# elapsed time includes the build and the bounce, so quoting it as a freeze would
# overstate it.
dispatch_freeze_note() {
    local rc="$1" elapsed="$2"
    [ "$rc" -eq 7 ] || return 0
    cat <<EOF
COST: while the drain waited, pogod refused all new polecat dispatch. This
attempt froze dispatch for about ${elapsed}s and deployed nothing — when a run
ends in exit 7 the run IS essentially the drain, since every phase before it
takes seconds. A stalled night pays that in full for no activation, which is why
POGO_DEPLOY_MAX_DRAIN caps one attempt at ${MAX_DRAIN}s instead of letting it
run to the end of the window. A night that quiesces pays only until it does.
EOF
}

remedy_for_exit() {
    case "$1" in
        4)
            cat <<'EOF'
The BUILD failed, so nothing was installed and nothing was bounced — the running
pogod is untouched and the fleet is intact. The artifact is the problem and the
log holds the compiler or `go install` error verbatim. A retry cannot help: the
same commit will fail the same way. Fix main, and the next nightly carries it.
EOF
            ;;
        5)
            cat <<'EOF'
The launchctl kickstart failed AFTER a successful install, which is the one exit
here that can leave the box worse than it found it: pogod may be DOWN. Check it
first and restore service before diagnosing anything else:

  launchctl print gui/$(id -u)/com.pogo.daemon | head -20
  launchctl kickstart -k gui/$(id -u)/com.pogo.daemon
  curl -s http://127.0.0.1:10000/version
EOF
            ;;
        6)
            cat <<'EOF'
The DRAIN PRECONDITION refused: the deploy asked pogod to stop dispatching and
did not get an answer it was willing to act on. Note what this exit does NOT
mean — it happens before `go install`, so nothing was built, nothing was
installed, nothing was restarted, and there is nothing to roll back. The running
pogod is the one that was running before the attempt.

Four different refusals share this code and they need four different reactions
(this pogod predates /agents/drain; pogod is not answering at all; orchestration
is stopped; some other HTTP status). The description line above is the deploy
script's own words about which one it was — read it rather than this paragraph,
which can only say what all four have in common.
EOF
            ;;
        8)
            cat <<'EOF'
The new pogod was installed and started but did not verify. The binary on disk is
the NEW one while the process may be missing or unhealthy, so this is the state
to resolve by hand rather than leave for the next nightly. Confirm what is
actually running, then decide whether to kickstart again or roll the install
back:

  curl -s http://127.0.0.1:10000/version
  tail -50 ~/Library/Logs/pogo/pogod.log
EOF
            ;;
        7)
            cat <<'EOF'
The DRAIN timed out: when the budget ran out, polecats still OWED THE REFINERY A
MERGE — work that is pushed to origin and not yet contained in the integration
branch, which is the one thing a bounce could lose (mg-853a). It does not mean
polecats were merely still running; since mg-853a a drain can clear with several
mid-analysis polecats alive, because they hold nothing the refinery is owed.

Note what this exit does NOT mean — the build never ran, so there is no artifact
to suspect and nothing in the build log to read. pogod was never replaced, and
pogo-self-deploy's exit trap has already restored dispatch (draining=false).

Nor was the fleet racing the drain: `draining=true` makes pogod refuse new
polecat dispatch, so the set of unmerged branches only shrinks. Whatever still
owed a merge had been running before the drain started.

The description line above names the polecats that were still owing, and the
verbatim block lists them. Start there rather than with a fleet-wide readout:

  curl -s http://127.0.0.1:10000/agents/drain | python3 -m json.tool
  pogo refinery queue

If the same branches block it night after night, the question is why their merges
are not landing, not how long their polecats live.
EOF
            ;;
        130|143)
            cat <<'EOF'
The deploy was KILLED mid-drain — not by anything it found, but by a signal. It
had not reached `go install`, so nothing was built, installed or restarted, and
its exit trap put dispatch back on the way out. There is nothing to repair.

What is worth knowing is WHO sent it. A SIGTERM at 03:00 with nobody at the
keyboard usually means the machine was going down (logout, shutdown, a forced
restart) — in which case the deploy is the symptom, not the problem. The next
nightly fire carries the same work.
EOF
            ;;
        9)
            cat <<'EOF'
The control suite went RED BEFORE the kickstart, which means the running pogod
was never replaced — the box is in a known-good state and the artifact is the
problem. This is the best failure in the list: a clean negative control, not an
outage. Read the log, fix the cause, and let the next nightly carry it.
EOF
            ;;
        *)
            cat <<'EOF'
Read the log before acting. Where in the sequence it stopped decides whether the
running pogod was replaced, and that is the fact everything else depends on:

  curl -s http://127.0.0.1:10000/version
EOF
            ;;
    esac
}

# red_alert_body RC ELAPSED TODAY BUDGET REASON_FILE — the whole RED mail body.
#
# A FUNCTION, not a heredoc inline in main(), because of what the ticket asked
# for: "a remedy paragraph is only reviewable next to the failure it is claimed
# to describe." While this text was assembled inside main() the only way to see
# it was to run a nightly deploy and fail it, so what got reviewed was the pieces
# — and the pieces were individually correct on 2026-08-07, when the assembled
# mail was wrong on every material point. The tests drive each real failure and
# print what this returns.
#
# ORDER IS THE ARGUMENT. The description (the deploy script's own sentence) and
# what the run changed come FIRST, above the code-keyed remedy, because they are
# the two facts that are known to be about THIS failure. The remedy paragraph
# below them can only speak for everything sharing the exit code.
red_alert_body() {
    local rc="$1" elapsed="$2" today="$3" budget="$4" file="${5:-}"
    local changed detail
    changed="$(what_the_run_changed "$file")"
    detail="$(deploy_reason_detail "$file")"
    printf '%s\n' "The unattended nightly redeploy FAILED, and no fire is left tonight to retry it.

  exit $rc:  $(describe_outcome "$rc" "$file")
  attempt:   $(( ATTEMPT_N + 1 )) tonight ($today)
  drain:     up to ${budget}s were allowed
  elapsed:   ${elapsed}s
  checkout:  $SRC ($("$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null))
  log:       $HOME/Library/Logs/pogo/pogo-deploy.log"
    if [ -n "$changed" ]; then
        printf '\nWHAT THIS ATTEMPT CHANGED: %s\n' "$changed"
    fi
    printf '\nDO NOT re-run with --force.\n'
    if [ -n "$detail" ]; then
        printf '\nWHAT THE DEPLOY SCRIPT ACTUALLY SAID, verbatim:\n\n%s\n' "$detail"
    fi
    printf '\n%s\n' "$(remedy_for_exit "$rc")"
    local freeze; freeze="$(dispatch_freeze_note "$rc" "$elapsed")"
    [ -n "$freeze" ] && printf '%s\n' "$freeze"
    return 0
}

# ---------------------------------------------------------------------------
# 8. Post-bounce mail-check verification
# ---------------------------------------------------------------------------
# pogo-self-deploy already runs this check within ~30s of the kickstart. This
# one runs after a longer grace, and that is not redundancy: on 07-17 the
# schedules were present immediately after the bounce and were reaped minutes
# later as agent_gone, leaving crew without their 10-minute mail loop while
# every health signal stayed green. A second look, later, is the only thing that
# sees that.
mail_check_ids() {
    [ -n "$POGO_CLI" ] || { echo "?"; return 1; }
    local out
    out="$("$POGO_CLI" schedule list --json 2>/dev/null)" || { echo "?"; return 1; }
    printf '%s' "$out" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\(mail-check-[^"]*\)".*/\1/p' | sort -u
}

# missing_ids PRE POST — ids present before the bounce and absent after.
missing_ids() {
    local pre="$1" post="$2"
    [ "$pre" = "?" ] || [ "$post" = "?" ] && { echo "?"; return 0; }
    comm -23 <(printf '%s\n' "$pre" | sed '/^$/d' | sort -u) \
             <(printf '%s\n' "$post" | sed '/^$/d' | sort -u)
}

# ---------------------------------------------------------------------------
# 9. Lock
# ---------------------------------------------------------------------------
# A redeploy can legitimately take an hour (the drain waits for polecats). If a
# fire lands while one is running, the second must NOT start a competing drain —
# it exits 0 and lets the first finish.
acquire_lock() {
    mkdir -p "$(dirname "$LOCK_DIR")" 2>/dev/null
    if mkdir "$LOCK_DIR" 2>/dev/null; then return 0; fi
    if find "$LOCK_DIR" -maxdepth 0 -type d -mmin +"$STALE_LOCK_MIN" 2>/dev/null | grep -q .; then
        log "lock: reclaiming a stale lock (>${STALE_LOCK_MIN}min)"
        rm -rf "$LOCK_DIR"
        mkdir "$LOCK_DIR" 2>/dev/null && return 0
    fi
    return 1
}

# on_exit RC — the single EXIT trap: record the night's outcome, then drop the
# lock. One trap rather than a record call at each of the seven exit points,
# because the exit point that gets forgotten is exactly the one that leaves the
# night looking unattempted and lets the next fire repeat a failure.
#
# The recording is conditional on ATTEMPT_ARMED, which is set only after every
# skip gate has passed. A fire that was late, locked out or already settled must
# leave the record untouched: it did not attempt anything, and writing "attempt
# 2, rc 0" for it would settle a night whose real attempt is still running.
on_exit() {
    local rc="$1"
    if $ATTEMPT_ARMED; then
        ATTEMPT_N=$(( ATTEMPT_N + 1 ))
        if stamp_write "$STAMP" "$(deploy_date)" "$ATTEMPT_N" "$rc"; then
            log "attempt recorded: $(deploy_date) attempt=$ATTEMPT_N rc=$rc ($STAMP)"
        else
            err "could not write the attempt record at $STAMP — a later fire tonight may repeat this attempt"
        fi
    fi
    rmdir "$LOCK_DIR" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dry-run) DRY_RUN=true ;;
            # Bounded by the `set -u` sentinel, not by a line number. The header
            # is long and grows, and a hardcoded range starts truncating --help
            # mid-thought the first time anybody documents anything — the old
            # '2,80p' had already drifted and was cutting the ENV list off in
            # the middle of itself. The `s///p` prints only lines that were
            # comments, so the sentinel and the blank line before it drop out.
            -h|--help) sed -n '2,/^set -u/p' "${BASH_SOURCE[0]}" | sed -n 's/^# \{0,1\}//p'; exit 0 ;;
            *) err "unknown flag: $1"; exit 2 ;;
        esac
        shift
    done

    log "pogo-deploy: start (src=$SRC window=$WINDOW dry_run=$DRY_RUN)"

    # --- gate 1: the window -------------------------------------------------
    parse_window "$WINDOW" || { err "bad POGO_DEPLOY_WINDOW '$WINDOW' (want START-END)"; exit 2; }
    local hour; hour="$(current_hour)"
    if [ "${POGO_DEPLOY_SKIP_WINDOW:-}" = "1" ]; then
        log "window: guard BYPASSED (POGO_DEPLOY_SKIP_WINDOW=1) at hour $hour"
    elif ! in_window "$hour" "$WINDOW_START" "$WINDOW_END"; then
        log "window: local hour $hour is outside [${WINDOW_START},${WINDOW_END}) — this is a deferred/catch-up fire, NOT deploying. Exit 0."
        exit 0
    else
        log "window: local hour $hour is inside [${WINDOW_START},${WINDOW_END})"
    fi

    # --- gate 2: one at a time ----------------------------------------------
    if ! acquire_lock; then
        log "lock: another pogo-deploy run holds $LOCK_DIR — exiting 0"
        exit 0
    fi
    trap 'on_exit $?' EXIT

    # --- gate 3: has tonight already been settled? (mg-8f7e, mg-0d70) -------
    # The 04:00 and 05:00 fires are retries for the failures that established
    # NOTHING: a stalled drain (7) and a sync that never reached the tree (10).
    # Every other outcome is settled by the first attempt, because it established
    # a fact and re-running it reproduces the failure and mails a duplicate alert.
    local today stamp_line disp
    today="$(deploy_date)"
    stamp_line="$(stamp_read "$STAMP")"
    disp="$(attempt_disposition "$today" "$stamp_line")"
    ATTEMPT_N="$(stamp_attempts "$stamp_line" "$today")"
    case "$disp" in
        first) log "attempt: first of the night ($today)" ;;
        retry) log "attempt: RETRY $(( ATTEMPT_N + 1 )) — tonight's earlier attempt exited $(stamp_rc "$stamp_line") ($(describe_exit "$(stamp_rc "$stamp_line")")), which established nothing, so a later fire can still fix it" ;;
        settled)
            log "attempt: tonight ($today) is already settled by attempt ${ATTEMPT_N} — its outcome was not a drain stall, so a retry would repeat the same failure and the same alert. Exit 0."
            exit 0 ;;
    esac

    # --- gate 4: is there enough window left to drain AND deploy? -----------
    local budget
    budget="$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
    if [ "$budget" -le 0 ]; then
        log "budget: under ${MIN_DRAIN}s of usable window is left before ${WINDOW_END}:00 (reserve ${RESERVE}s) — a drain started now could not finish, and one that times out has stopped dispatch for its whole length and delivered nothing. NOT deploying. Exit 0."
        exit 0
    fi
    log "budget: drain gets up to ${budget}s (window ends ${WINDOW_END}:00, reserve ${RESERVE}s, cap ${MAX_DRAIN}s)"

    # Past every skip: this fire is really attempting tonight's deploy, so its
    # outcome is the night's outcome. A dry run never arms — it decides nothing
    # and must not settle a night it did not attempt.
    $DRY_RUN || ATTEMPT_ARMED=true

    # --- tools + credentials ------------------------------------------------
    # Resolve the alert path BEFORE anything that can fail, so a failure has
    # somewhere to be reported to. A wrapper whose first failure is "I cannot
    # tell you about failures" is the silent nightly all over again.
    resolve_mg   || { err "no alert path — refusing to run unattended"; exit 1; }
    resolve_pogo || log "pogo CLI unresolved — the post-bounce schedule check will be skipped"
    # Never fatal. Without it the sync-failure classifier loses only its ability
    # to assert that a host is DOWN; it can still confirm one is up, and it
    # reports `unclassified` rather than guessing (mg-0d70).
    resolve_nc || true
    # After resolve_mg, so a git failure has somewhere to be reported; before
    # sync_src, which is the first thing that needs git.
    resolve_git || {
        alert "[pogo-deploy] ABORTED: no working git" \
"The nightly redeploy could not find a git that RUNS, so it could not sync its
checkout. Nothing was deployed and the running pogod is untouched.

One cause that presents this way is a damaged Xcode Command Line Tools install:
/usr/bin/git is the CLT shim, and when the install behind it is broken it fails
every call — 'git --version' included — with 'unable to locate xcodebuild' and
exit 71. Existence checks pass; the binary just cannot work.

  log: $HOME/Library/Logs/pogo/pogo-deploy.log
  fix: install or repair a git and confirm 'git --version' prints a version
       (xcode-select --install, or brew install git). Pin one for this job with
       GIT=/path/to/git if several are installed."
        exit 1
    }
    load_gh_token || {
        alert "[pogo-deploy] ABORTED: no GH_TOKEN" \
"The nightly redeploy could not obtain GH_TOKEN from $ZSHENV, so any gh call in
the deploy would fail unauthenticated (the mg-03ea/mg-25fb class). Nothing was
deployed and the running pogod is untouched.

  log: $HOME/Library/Logs/pogo/pogo-deploy.log
  fix: confirm 'export GH_TOKEN=' is present and readable in $ZSHENV"
        exit 1
    }

    # --- gate 5: safe sync --------------------------------------------------
    # The alert names the class the runner ESTABLISHED and prints the underlying
    # error verbatim (mg-0d70). It used to say "dirty or diverged aborts by
    # design" under every failure, which on 08-05 sent Daniel to a `git status`
    # that was clean — an alert that guesses is a detector that lies at exactly
    # the moment it is read.
    if ! sync_with_retry; then
        # Exit 10 when the class established nothing, so a later fire can retry
        # it the way it already retries a stalled drain; exit 1 when the failure
        # established a fact and re-running it would only re-establish it.
        local sync_rc=1
        sync_class_retryable "$SYNC_CLASS" && sync_rc=10
        if retry_will_follow "$sync_rc"; then
            local snxt; snxt="$(next_fire_hour "$(current_hour)")"
            log "sync: exit $sync_rc after $SYNC_TRIES attempts — class ${SYNC_CLASS:-unclassified} established nothing, so the $(printf '%02d' "$snxt"):00 fire will retry. Not alerting yet."
            if [ -n "$POGO_CLI" ]; then
                "$POGO_CLI" events emit --type=deploy_nightly_retry_pending --agent=pogo-deploy \
                    --details="{\"exit\":$sync_rc,\"sync_class\":\"${SYNC_CLASS:-unclassified}\",\"sync_attempts\":$SYNC_TRIES,\"sync_vigil_s\":$SYNC_VIGIL_SPENT,\"retry_hour\":$snxt}" >/dev/null 2>&1 || true
            fi
            exit "$sync_rc"
        fi
        alert "[pogo-deploy] ABORTED: could not sync $SRC — $(describe_sync_class "$SYNC_CLASS")" \
"The nightly redeploy refused to advance its dedicated checkout. Nothing was
deployed and the running pogod is untouched. Daniel's dev tree was NOT touched.

  cause:    $(describe_sync_class "$SYNC_CLASS")
  checkout: $SRC
  attempts: $SYNC_TRIES in this run$([ "$SYNC_RETRY_SPENT" -gt 0 ] && printf ', over %ss of waiting' "$SYNC_RETRY_SPENT")
  vigil:    $([ "$SYNC_VIGIL_SPENT" -gt 0 ] && printf '%ss — a LOWER BOUND on how long the transport was unreachable' "$SYNC_VIGIL_SPENT" || printf 'none (the failure was not one the vigil covers, or the vigil is off)')
  exit:     $sync_rc$([ "$sync_rc" -eq 10 ] && printf ' (retryable class — but no fire is left tonight)')
  log:      $HOME/Library/Logs/pogo/pogo-deploy.log

WHAT THE UNDERLYING TOOL ACTUALLY SAID, verbatim:

${SYNC_DETAIL:-(the failing step produced no output)}

$(remedy_for_sync_class "$SYNC_CLASS")"
        exit "$sync_rc"
    fi
    sync_recovery_notice "$SYNC_TRIES" "$SYNC_RETRY_SPENT"

    # Backoff sleeps come out of the drain's share of the window, so the budget
    # computed at gate 4 is stale by exactly that much. Recompute rather than
    # hand the drain a number derived from a clock that has since moved on.
    if [ "$SYNC_RETRY_SPENT" -gt 0 ]; then
        budget="$(drain_budget "$WINDOW_END" "$RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
        if [ "$budget" -le 0 ]; then
            log "budget: the ${SYNC_RETRY_SPENT}s of sync backoff left under ${MIN_DRAIN}s of usable window — not starting a drain that cannot finish. Exit 0."
            exit 0
        fi
        log "budget: recomputed after ${SYNC_RETRY_SPENT}s of sync backoff — drain gets up to ${budget}s"
    fi

    local DEPLOY="$SRC/scripts/pogo-self-deploy"
    [ -x "$DEPLOY" ] || { err "no executable pogo-self-deploy at $DEPLOY"; exit 1; }

    # --- gate 6: drift ------------------------------------------------------
    local check_out check_rc
    check_out="$(POGO_REPO="$SRC" "$DEPLOY" check 2>&1)"; check_rc=$?
    printf '%s\n' "$check_out"
    if [ "$check_rc" -ne 0 ]; then
        alert "[pogo-deploy] ABORTED: drift check could not classify" \
"'pogo-self-deploy check' exited $check_rc, so the job cannot tell what is owed.
Nothing was deployed.

$check_out"
        exit 1
    fi
    if is_clean_verdict "$check_out"; then
        log "drift: none — running pogod and installed binaries are at $DEPLOY_REF. NOT bouncing the fleet. Exit 0."
        exit 0
    fi
    log "drift: work owed — proceeding to redeploy"

    if $DRY_RUN; then
        log "dry-run: would run '$DEPLOY redeploy --yes --drain-timeout $budget' (never --force). Stopping here."
        exit 0
    fi

    # --- snapshot before the bounce ----------------------------------------
    local pre; pre="$(mail_check_ids)"
    log "pre-bounce mail-check schedules: $(printf '%s' "$pre" | tr '\n' ' ')"

    # --- the redeploy -------------------------------------------------------
    # --yes because confirm() exits 3 without a tty. NEVER --force: the two
    # things --force overrides are killing live polecats and bouncing a fleet
    # whose idleness could not be established, and neither is a decision an
    # unattended 03:00 job gets to make.
    #
    # --drain-timeout is the window-derived budget (mg-8f7e), not the script's
    # own 1800s default. That default is right for a human running the script by
    # hand — thirty minutes is about as long as anybody waits at a terminal — and
    # wrong for the unattended run, which has the whole night and needs it.
    log "redeploy: $DEPLOY redeploy --yes --drain-timeout $budget (repo $SRC)"
    local rc=0 t0 elapsed
    # The reason channel (mg-0155). One file per attempt, named for the attempt,
    # so a retry cannot read the earlier fire's record and describe last hour's
    # failure. Under the log dir rather than /tmp because it is evidence: when an
    # alert says something surprising, the record it was built from should still
    # be there in the morning.
    local reason_file="$HOME/Library/Logs/pogo/pogo-deploy-reason.$today.$(( ATTEMPT_N + 1 ))"
    mkdir -p "$(dirname "$reason_file")" 2>/dev/null || true
    rm -f "$reason_file" 2>/dev/null || true
    t0="$(date +%s)"
    POGO_REPO="$SRC" POGO_DEPLOY_REASON_FILE="$reason_file" \
        "$DEPLOY" redeploy --yes --drain-timeout "$budget" || rc=$?
    elapsed=$(( $(date +%s) - t0 ))
    log "redeploy: exit $rc after ${elapsed}s — $(describe_outcome "$rc" "$reason_file")"

    if [ "$rc" -ne 0 ]; then
        # A stall with another fire still to come is not yet a RED. The retry is
        # the point of the extra fires, and mailing an alert per attempt would
        # make three of them the cost of having them (mg-8f7e). The event is
        # still emitted, so the digest sees every attempt; the mail waits until
        # the night has actually run out of chances.
        if retry_will_follow "$rc"; then
            local nxt; nxt="$(next_fire_hour "$(current_hour)")"
            log "redeploy: exit $rc ($(describe_outcome "$rc" "$reason_file")) after attempt $(( ATTEMPT_N + 1 )) — the $(printf '%02d' "$nxt"):00 fire will retry. Not alerting yet."
            dispatch_freeze_note "$rc" "$elapsed"
            if [ -n "$POGO_CLI" ]; then
                "$POGO_CLI" events emit --type=deploy_nightly_retry_pending --agent=pogo-deploy \
                    --details="{\"exit\":$rc,\"attempt\":$(( ATTEMPT_N + 1 )),\"retry_hour\":$nxt,\"drain_timeout\":$budget,\"dispatch_frozen_s\":$elapsed}" >/dev/null 2>&1 || true
            fi
            exit "$rc"
        fi
        alert "[pogo-deploy] RED: nightly redeploy exited $rc" \
            "$(red_alert_body "$rc" "$elapsed" "$today" "$budget" "$reason_file")"
        exit "$rc"
    fi

    # --- post-bounce verification ------------------------------------------
    log "grace: waiting ${GRACE}s before re-reading schedules"
    sleep "$GRACE"
    local post lost
    post="$(mail_check_ids)"
    lost="$(missing_ids "$pre" "$post")"
    if [ "$lost" = "?" ]; then
        log "mail-check re-check: UNKNOWN (could not read schedules before or after)"
    elif [ -n "$lost" ]; then
        alert "[pogo-deploy] mail-check schedules LOST across the nightly bounce" \
"The redeploy succeeded, but ${GRACE}s later these mail-check schedules that
existed before the bounce are gone:

$lost

The fleet's mail loop is degraded and WILL LOOK HEALTHY — pogod is up, the port
answers, agents are alive. Diagnose:

  pogo schedule list | grep mail-check
  pogo agent diagnose <name>

Restore by nudging the affected agents to re-register."
    else
        log "mail-check re-check: OK — every pre-bounce schedule is back ($(printf '%s' "$post" | grep -c . ) present)"
    fi

    log "pogo-deploy: done — pogod redeployed to $("$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null)"
    exit 0
}

# Run main only when executed directly, so the test harness can source this file
# and exercise the pure helpers without firing a deploy.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
