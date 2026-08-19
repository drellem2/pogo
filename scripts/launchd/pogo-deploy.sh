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
# WHEN THE DEPLOY CANNOT RUN AT ALL, BOUNCE THE FLEET ANYWAY (mg-9fc9)
# ----------------------------------------------------------------------
# Everything above this line makes the deploy more patient about a network it
# needs. None of it helps on the night the network never comes back, and the
# nightly deploy is this box's ONLY automatic recovery path: it restarts the
# fleet, and a restart is what clears a wedged agent. On 2026-08-15..19 that path
# was unavailable for five consecutive nights — `ssh: Could not resolve hostname
# github.com`, 30 retries over 7980s, rc=10, every night — and the fleet sat in a
# 118-hour blackout that any one of those nights would have ended. Nothing
# misbehaved. THE RECOVERY MECHANISM SHARES A DEPENDENCY WITH THE FAILURE IT
# RECOVERS FROM, which is a property of the topology and not a defect in a part.
#
# A restart needs no remote. So after POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER (2)
# consecutive nights lost at the TRANSPORT step, this runner calls
# `pogo-self-deploy bounce`: the same drain gate, the same out-of-band guard, the
# same verifies, and no fetch, no build, no do_prove. It delivers no new code —
# that genuinely needs the remote — and it ends a blackout the agents were only in
# because nothing had restarted them.
#
# Four things about it that are not incidental: it is keyed on the TRANSPORT class
# specifically (a bad TREE must never bounce a fleet), the threshold is config and
# is greater than one, it announces itself through the LOCAL maildir and the LOCAL
# event log because the network is what is broken, and it respects the drain
# absolutely — --force is refused, so a polecat holding unpushed commits stops it
# and gets reported. Section 5c has the whole argument, including what the
# fallback still does not reach.
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
# A STEP THAT NEVER RETURNS IS SCORED AS A NIGHT THAT RAN (mg-56ac)
# ------------------------------------------------------------------
# On 2026-08-08 this job fired on time, wrote nine lines in one second, and then
# said nothing for 31 hours 39 minutes:
#
#     [2026-08-08T02:00:05Z] GH_TOKEN: sourced from ~/.zshenv (present, 40 chars)
#       ... 31h39m ...
#     [2026-08-09T09:39:43Z] sync: ~/.pogo/deploy-src at main 738e322
#     [2026-08-09T09:43:23Z] pogo-deploy: done — pogod redeployed to 738e322
#
# ONE run. It blocked inside sync_src's fetch and then completed the next
# morning. No exit code, no ALERT, no RED mail, and no drain-timeout line either
# — the 7200s drain cap could not bound it because the run never reached the
# drain. The crew had been stopped at 00:44Z and stayed stopped for 33 hours,
# because the run that would have brought it back was still sitting in that gap.
#
# THE CONTRAST IS THE PROOF, and it is the same step three nights earlier:
#
#     08-05  fetch FAILED      -> rc=1, four lines, two mails, night settled loudly
#     08-08  fetch NEVER RETURNED -> silence, and every instrument read GREEN
#
# Same call. The entire difference between a loud recoverable night and a silent
# 33-hour outage is whether the call returned an error or did not return. So
# "no exit code" is not a missing detail in the record — it is the defect.
#
# THREE BOUNDS, AT THREE DIFFERENT LAYERS, because a bound that shares a failure
# mode with the thing it bounds is not a bound:
#
#   1. THE CALL. Every git step runs under run_bounded (POGO_DEPLOY_GIT_TIMEOUT,
#      300s). A step that exceeds it is killed, classified `timeout`, and — like
#      every other class that established NOTHING about the tree — retried by the
#      blip tier and then the vigil. This converts an 08-08 into an 08-05.
#   2. GIT'S OWN TRANSPORT. GIT_HTTP_LOW_SPEED_LIMIT/_TIME and an ssh
#      ConnectTimeout/ServerAliveInterval are exported before the sync, so a
#      half-open connection usually produces a real git ERROR — loud, classified,
#      and verbatim in the alert — before the kill above is needed. Preferring a
#      returned error to a kill is the whole point: a kill loses git's stderr.
#   3. THE RUN. arm_run_deadline bounds the WHOLE run regardless of which stage
#      is stuck, because the next unbounded call will not be a git one. It is a
#      watchdog in a separate process, it alerts by itself rather than asking the
#      wedged run to report its own wedging, and it kills the tree from the
#      leaves so the run's own EXIT trap can still record the night.
#
# AND A TERMINAL LINE ON EVERY PATH. The EXIT trap now writes
# `pogo-deploy: end (rc=N after Ns)` before it does anything else, and it is
# armed at the TOP of main rather than after the lock, so a skipped fire has one
# too. That line is what lets a detector outside this process tell "still going"
# from "stopped forever" — see internal/staleness/nofire.go, which reads it and
# reports a run that started and did not finish as a finding rather than as the
# good branch. The bound above and that witness are deliberately separate: a
# bound whose only evidence that it worked is the bounded thing reporting so has
# no evidence at all.
#
# HOW THESE COULD EXHIBIT THE DEFECT THEY REMEDY — a bound that itself hangs:
#
#   - run_bounded's killer is a `sleep` in a subshell, which cannot block on the
#     network. It kills DESCENDANTS FIRST (git fetch's ssh child holds the
#     socket; killing git alone can leave it), then the child, then SIGKILLs.
#   - arm_run_deadline's watchdog resolves its own `mg` when it fires rather than
#     inheriting one resolved earlier, because the hang may be IN that
#     resolution — and it runs the alert under run_bounded, so an alert path that
#     is itself wedged cannot stop the kill.
#   - the watchdog checks that its target is still THIS script (by command name)
#     before killing anything, so a run that ends between the check and the kill
#     cannot cost an unrelated process that inherited the pid.
#   - and if the watchdog is defeated anyway, the run writes no terminal line,
#     which is exactly the case the witness reports. Nothing here is trusted to
#     report its own silence.
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
#   POGO_DEPLOY_FIRE_HOURS   PIN the fire hours instead of reading them from the
#                            world (unset in production — the hours are derived
#                            from the LOADED launchd job; see section 1c)
#   POGO_DEPLOY_LABEL        the launchd label to read the schedule from (com.pogo.deploy)
#   POGO_DEPLOY_PLIST        the plist file to cross-check it against
#   POGO_DEPLOY_LAUNCHCTL    pin a launchctl (controls only)
#   POGO_DEPLOY_PLISTBUDDY   pin a PlistBuddy (controls only)
#   POGO_DEPLOY_STAMP        the night's attempt record ($POGO_HOME/deploy-attempt.stamp)
#   POGO_DEPLOY_SYNC_ATTEMPTS  total sync tries on a RETRYABLE class (4)
#   POGO_DEPLOY_SYNC_BACKOFF   seconds between them, last value repeats ("15 45 120")
#   POGO_DEPLOY_SYNC_RETRY_BUDGET  ceiling on total blip-tier backoff, seconds (300)
#   POGO_DEPLOY_SYNC_VIGIL     1 to wait a transport outage out inside the window (1)
#   POGO_DEPLOY_SYNC_VIGIL_INTERVAL  seconds between vigil probes (300)
#   POGO_DEPLOY_PROBE_TIMEOUT  seconds to wait for the reachability probe (5)
#   POGO_DEPLOY_GIT_TIMEOUT    seconds any ONE git step may take before it is
#                              killed and classified `timeout` (300; 0 disables)
#   POGO_DEPLOY_RUN_DEADLINE   seconds the WHOLE run may take before the watchdog
#                              alerts and kills it (0 = derive from the window)
#   POGO_DEPLOY_DEADLINE_SLACK seconds past WINDOW_END the derived deadline
#                              allows (1800)
#   POGO_DEPLOY_NC           pin an nc for the probe (still checked by execution)
#   GIT                      pin a specific git (still checked by execution)
#   POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER  after this many CONSECUTIVE nights lost at
#                            the TRANSPORT step, bounce the fleet with no remote
#                            (2; 0 disables the fallback entirely) — see section 5c
#   POGO_DEPLOY_TRANSPORT_STREAK  where that count is kept across nights
#                            ($POGO_HOME/deploy-transport-streak.stamp)
#   POGO_DEPLOY_BOUNCE_RESERVE  seconds of the window the FALLBACK keeps back for
#                            its restart+verify (300). Deliberately not RESERVE:
#                            a bounce owes no build and no do_prove, and charging
#                            it the deploy's 1200s would zero its budget on every
#                            night the vigil used the window — i.e. exactly the
#                            nights it exists for
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
# to a mailbox with no reader. At the time that was INVISIBLE: mg filed mail for
# an unknown name rather than refusing, so the delivery looked fine.
#
# mg-d639 removed that. An unknown recipient is now refused (no_such_mailbox,
# exit 3), which turns a silently-undelivered alert into a loud one — an
# improvement, and the reason register_alert_recipients exists below. A
# deployment whose PM owns deploys sets POGO_DEPLOY_ALERT_TO. `human` is copied
# either way.
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

# The job's fire hours. DERIVED from the world at run time (section 1c), never
# written down here.
#
# This used to be `FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-3 4 5}"`, with a comment
# saying the duplication bought "exactly one thing: the RED alert can say whether
# a retry is coming", and conceding that "a drifted list makes one sentence of
# one alert optimistic". That concession was not a hypothetical — it was a
# prediction, and it came true inside a week. The installed plist carried a
# SINGLE 03:00 fire while this list said 3 4 5, so the runner believed two
# retries were coming when none existed, and the alert delivered the exact
# opposite of the one thing the duplication was bought for (mg-fc99, mg-8dcb).
#
# A value read from the world cannot drift from the world, so the generator is
# gone rather than detected. The override survives for tests and for a manual
# run that wants to pin the list, but it is NOT the source of truth: with it
# unset — which is how launchd runs this — the hours come from the loaded job.
#
# Empty until resolve_fire_hours runs, and empty is a MEANING: "this run does not
# know". next_fire_hour then finds no later fire, retry_will_follow is false, and
# the alert says it could not read the schedule instead of asserting a fact about
# fires it never saw. Failing that way round costs at most one alert that should
# have waited; the opposite default is what mg-fc99 was filed about.
FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-}"
FIRE_HOURS_SOURCE="unresolved"

# The launchd job this script IS, and the two places its schedule can be read
# from. Both overridable so the controls can point them at a fixture.
DEPLOY_LABEL="${POGO_DEPLOY_LABEL:-com.pogo.deploy}"
DEPLOY_PLIST="${POGO_DEPLOY_PLIST:-$HOME/Library/LaunchAgents/$DEPLOY_LABEL.plist}"
LAUNCHCTL="${POGO_DEPLOY_LAUNCHCTL:-/bin/launchctl}"
PLISTBUDDY="${POGO_DEPLOY_PLISTBUDDY:-/usr/libexec/PlistBuddy}"

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

# How long a TOOL identity check may take. These are local execs that print a
# line, so 30s is enormous; the point is that no exec in this file is unbounded,
# because "it is a local call" is exactly the reasoning that left the git queries
# bare (see git_q).
TOOL_PROBE_TIMEOUT="${POGO_DEPLOY_TOOL_PROBE_TIMEOUT:-30}"

# The per-step git bound (mg-56ac). 300s against a fetch that normally takes two
# — the number is not calibrated to how long a fetch takes, it is calibrated to
# being unmistakably longer than one. What it has to separate is "slow" from
# "never", and 08-08 was on the far side of that by four hundred times.
#
# Four attempts of it is 20 minutes of a four-hour window in the worst case, and
# the vigil's per-sleep window test already refuses to start one it cannot
# finish. 0 disables the bound, for a controlled run where a kill would obscure
# what is being measured.
GIT_TIMEOUT="${POGO_DEPLOY_GIT_TIMEOUT:-300}"

# The whole-run deadline (mg-56ac). Zero means DERIVE it: the distance to the
# window's end plus DEADLINE_SLACK, floored at the longest a legitimate run can
# take (MAX_DRAIN + RESERVE + slack).
#
# Derived rather than constant, for mg-8f7e's reason one layer up: the number
# that actually constrains this run is when the window closes, and a constant
# calibrated against today's drain expires the moment the window moves. A 03:00
# fire under the production window is therefore killed at ~06:30 rather than at
# 09:39 the next morning.
#
# The floor matters for the out-of-window controlled run (POGO_DEPLOY_SKIP_WINDOW
# =1 at 14:00), where the distance to the window's end is negative and a naive
# derivation would arm a deadline that fires immediately.
RUN_DEADLINE="${POGO_DEPLOY_RUN_DEADLINE:-0}"
DEADLINE_SLACK="${POGO_DEPLOY_DEADLINE_SLACK:-1800}"

# Set by the EXIT trap's own bookkeeping. RUN_T0 is when main() began, so the
# terminal line can say how long the run took — which is the number that
# separates a deploy from a hang, and the one the 08-08 record could not supply.
RUN_T0=0
WATCHDOG_PID=""

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
# non-zero when tonight has none left, and non-zero when FIRE_HOURS is EMPTY,
# which is how an unreadable schedule reaches the caller as "no claim" rather
# than as a claim. The readers in section 1c sort ascending; a hand-pinned
# POGO_DEPLOY_FIRE_HOURS must too.
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
# 1c. The fire hours, READ FROM THE WORLD (mg-8dcb / mg-fc99)
# ---------------------------------------------------------------------------
# Two readable sources, and they answer different questions:
#
#   the LOADED job   (launchctl print gui/<uid>/com.pogo.deploy) — what will
#                    actually fire tonight. This is the authority.
#   the plist FILE   ($HOME/Library/LaunchAgents/com.pogo.deploy.plist) — what
#                    somebody last wrote down. A file corrected and never
#                    reloaded reads perfectly and does nothing at 04:00.
#
# So the loaded job wins, and a disagreement between the two is reported rather
# than silently resolved: it is exactly the "installed but not bootstrapped"
# state that looks healthy to every file-based check.
#
# SHAPE-AGNOSTIC ON PURPOSE, and this is the whole trap. The defect that
# motivated all of this had StartCalendarInterval as a BARE DICT:
#
#     Dict { Hour = 3, Minute = 0 }        <- what was actually installed
#     Array [ Dict { Hour = 3 } ]          <- NOT what was installed
#
# Those are different plist shapes, and a reader that walks array elements finds
# no mismatching element on the dict because it finds no elements at all — it
# reports GREEN against the very state it exists to catch. Both readers below
# therefore collect `Hour` values from anywhere under StartCalendarInterval and
# never index into an array, which makes the two shapes indistinguishable to
# them. The controls exercise the dict form specifically.

# fire_hours_from_launchctl [FILE] — hours from the LOADED job. FILE is a
# captured `launchctl print` for the controls; without it, the live job is read.
#
# A calendarinterval descriptor with no Hour key fires EVERY hour, which no hour
# list can express, so that is a refusal to derive rather than a shorter list.
fire_hours_from_launchctl() {
    local out hours
    if [ $# -ge 1 ]; then
        out="$(cat "$1" 2>/dev/null)" || return 1
    else
        [ -x "$LAUNCHCTL" ] || return 1
        # Bounded like every other exec in this script: launchd is exactly the
        # subsystem that can be wedged on the night this matters.
        #
        # The non-zero status is the whole check, and BOUNDED_TIMED_OUT must NOT
        # be consulted here — that is the obvious line to add and it would be
        # worse than nothing. run_bounded sets it in the SUBSHELL the command
        # substitution creates, so what this scope would read is whatever some
        # earlier bounded call left behind: a false negative on a stale `true`,
        # and no signal at all on a real timeout. A timeout already surfaces as
        # rc=BOUNDED_RC, which the `|| return 1` catches.
        out="$(run_bounded "$TOOL_PROBE_TIMEOUT" "$LAUNCHCTL" print "gui/$(id -u)/$DEPLOY_LABEL" 2>/dev/null)" || return 1
    fi
    # awk is the LAST command in this substitution on purpose. Piping it into
    # `sort | tr` here would hand the substitution sort's status instead, and the
    # awk `exit 3` below — the refusal on a descriptor with no Hour — would be
    # thrown away, leaving exactly the shorter-list-than-the-truth this function
    # exists not to produce. The tidying runs as a second step.
    hours="$(printf '%s\n' "$out" | awk '
        # Reset on EVERY stream line, so a descriptor belonging to some other
        # trigger stream cannot inherit a previous block'"'"'s calendarinterval.
        /stream[ \t]*=/ { cal = ($0 ~ /com\.apple\.launchd\.calendarinterval/); next }
        cal && /descriptor[ \t]*=[ \t]*\{/ { desc = 1; hour = ""; next }
        desc && /"Hour"[ \t]*=>[ \t]*[0-9]+/ {
            h = $0; sub(/.*=>[ \t]*/, "", h); sub(/[^0-9].*$/, "", h); hour = h; next
        }
        desc && /^[ \t]*\}/ {
            if (hour == "") { bad = 1 } else { print hour + 0 }
            desc = 0; cal = 0
        }
        END { if (bad) exit 3 }
    ')" || return 1
    hours="$(printf '%s\n' "$hours" | sed '/^$/d' | sort -n -u | tr '\n' ' ')"
    hours="${hours% }"
    [ -n "$hours" ] || return 1
    printf '%s' "$hours"
}

# fire_hours_from_plist [PATH] — hours from the plist FILE, in either shape.
#
# PlistBuddy prints a bare dict as `Dict { ... Hour = 3 ... }` and an array as
# `Array { Dict { ... } ... }`; in BOTH, every `Hour = N` line under the key is
# one fire. Counting `Dict {` gives the number of entries in either shape too —
# one for the bare dict, N for the array — so an entry that carries a Minute and
# no Hour is caught rather than dropped.
fire_hours_from_plist() {
    local path="${1:-$DEPLOY_PLIST}" out hours dicts n
    [ -r "$path" ] || return 1
    if [ -x "$PLISTBUDDY" ]; then
        out="$("$PLISTBUDDY" -c 'Print :StartCalendarInterval' "$path" 2>/dev/null)" || return 1
        hours="$(printf '%s\n' "$out" | sed -n 's/^[[:space:]]*Hour[[:space:]]*=[[:space:]]*\([0-9][0-9]*\)[[:space:]]*$/\1/p')"
        dicts="$(printf '%s\n' "$out" | grep -c 'Dict[[:space:]]*{')"
    else
        # Fallback for a host without PlistBuddy. `plutil -extract` emits the
        # key's value alone, so <dict> counting is scoped the same way, and the
        # XML is one tag per line for both shapes.
        out="$(/usr/bin/plutil -extract StartCalendarInterval xml1 -o - "$path" 2>/dev/null)" || return 1
        hours="$(printf '%s\n' "$out" | awk '
            /<key>Hour<\/key>/ { want = 1; next }
            want && match($0, /<integer>[0-9]+<\/integer>/) {
                h = substr($0, RSTART + 9, RLENGTH - 19); print h + 0; want = 0
            }')"
        dicts="$(printf '%s\n' "$out" | grep -c '<dict>')"
    fi
    n="$(printf '%s\n' "$hours" | sed '/^$/d' | wc -l | tr -d ' ')"
    # Neither more nor fewer: one Hour per entry, whatever the shape.
    [ "$n" -gt 0 ] && [ "$n" -eq "$dicts" ] || return 1
    hours="$(printf '%s\n' "$hours" | sed '/^$/d' | sort -n -u | tr '\n' ' ')"
    printf '%s' "${hours% }"
}

# resolve_fire_hours — set FIRE_HOURS / FIRE_HOURS_SOURCE from the world.
#
# Never fatal. A run that cannot read its own schedule still deploys; what it
# loses is the right to make a claim about later fires, and section 7 says so in
# the alert instead of guessing.
resolve_fire_hours() {
    local loaded file
    if [ -n "${POGO_DEPLOY_FIRE_HOURS:-}" ]; then
        FIRE_HOURS="$POGO_DEPLOY_FIRE_HOURS"
        FIRE_HOURS_SOURCE="override"
        log "fire hours: $FIRE_HOURS (POGO_DEPLOY_FIRE_HOURS override — pinned by hand, NOT read from the world)"
        return 0
    fi
    loaded="$(fire_hours_from_launchctl)" || loaded=""
    file="$(fire_hours_from_plist)" || file=""

    if [ -n "$loaded" ]; then
        FIRE_HOURS="$loaded"
        FIRE_HOURS_SOURCE="launchctl"
        log "fire hours: $FIRE_HOURS — read from the LOADED job ($LAUNCHCTL print gui/$(id -u)/$DEPLOY_LABEL)"
        if [ -n "$file" ] && [ "$file" != "$loaded" ]; then
            err "fire hours: $DEPLOY_PLIST says '$file' but the LOADED job fires '$loaded' — the file was edited and never reloaded, so it is describing a schedule that does not exist. Run 'pogo service install-deploy' (it boots the job out and back in) to make the file real. This run uses the loaded list, which is what will actually fire."
        fi
        return 0
    fi
    if [ -n "$file" ]; then
        FIRE_HOURS="$file"
        FIRE_HOURS_SOURCE="plist"
        log "fire hours: $FIRE_HOURS — read from $DEPLOY_PLIST because the loaded job could not be read. This is what the FILE says; an unreloaded file can differ from what fires."
        return 0
    fi
    FIRE_HOURS=""
    FIRE_HOURS_SOURCE="unknown"
    err "fire hours: neither the loaded job nor $DEPLOY_PLIST could be read — this run cannot tell whether a later fire is coming tonight, and will not claim one either way."
    return 1
}

# fires_left_phrase — the sentence about later fires, and the only reason the old
# hardcoded list existed. It now has three cases, not two, because "I could not
# read the schedule" is not the same claim as "there is no later fire".
fires_left_phrase() {
    if [ "$FIRE_HOURS_SOURCE" = "unknown" ] || [ -z "$FIRE_HOURS" ]; then
        printf '%s' "and this run could not read its own launchd schedule, so it cannot say whether a later fire tonight will retry it — check \`$LAUNCHCTL print gui/$(id -u)/$DEPLOY_LABEL\`"
    else
        printf '%s' "and no fire is left tonight to retry it (the job fires at $FIRE_HOURS, read from ${FIRE_HOURS_SOURCE})"
    fi
}

# ---------------------------------------------------------------------------
# 1d. The night's attempt record (mg-8f7e)
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
        # Bounded like every other exec here: a candidate that is present and
        # does not RETURN is the same outage as one that is present and broken,
        # and the identity check is the first exec this job performs.
        run_bounded "$TOOL_PROBE_TIMEOUT" "$cand" --help 2>/dev/null | grep -q 'macguffin' || continue
        MG="$cand"
        log "mg: resolved macguffin at $MG"
        return 0
    done
    err "mg: no macguffin 'mg' among ${cands[*]} — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR)"
    return 1
}

# register_alert_recipients — make the alert path DELIVERABLE before it is needed
# (mg-7dc1).
#
# Until mg-d639, `mg mail send` filed mail for any name at all, so an ALERT_TO
# nobody had ever mailed still reported Delivered — and the comment on ALERT_TO
# above said so outright. mg-d639 made an unknown recipient a refusal. That is
# the right change; what it exposed is that this job never provisioned the two
# names it mails, it just relied on somebody having mailed them first.
#
# Registration, not `--create` on the sends. The two are one word apart and the
# difference is the entire value of mg-d639: --create on a send says "deliver to
# this name whether or not anyone meant it", so POGO_DEPLOY_ALERT_TO=mayro would
# quietly mint `mayro` and report success at 03:00 on the one night it mattered.
# Registering up front leaves the send's refusal meaning what it should — the
# name is wrong — while guaranteeing the two recipients this job actually has
# both exist.
#
# NEVER FATAL, deliberately. This runs before any deploy work and its failure
# says nothing about whether the deploy can proceed; the sends are attempted
# regardless, and alert() already reports each one that fails. Aborting the
# nightly because a mailbox could not be pre-created would let a provisioning
# hiccup do what no alert failure does — stop the deploy.
#
# `mg mail register` is IDEMPOTENT (exit 0 and no change for a box that exists,
# and it never touches mail), so this is safe to run on every fire. On this host
# both boxes already exist and it is a no-op; the case it is for is a fresh
# install, where nothing else creates them.
register_alert_recipients() {
    local who
    for who in "$ALERT_TO" human; do
        if "$MG" mail register "$who" >/dev/null 2>&1; then
            continue
        fi
        # Old mg (pre-mg-d639) has no `mail register` subcommand and exits
        # non-zero on the unknown verb. That build also still files mail for an
        # unknown name, so the alert path works there without this — say so
        # rather than implying the alert is broken.
        err "alert: could not register mailbox '$who' — alerts to it may be refused (mg mail register unavailable or failed); 'human' and '$ALERT_TO' are mailed independently, so the other copy is unaffected"
    done
    return 0
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
        run_bounded "$TOOL_PROBE_TIMEOUT" "$cand" --version 2>/dev/null | grep -q '^git version' || continue
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
#
# $3 is optional extra JSON fields for the EVENT (no braces, leading comma
# omitted). The mail is read by a person and carries its facts in prose; the
# event is read by code, and a subject string is not something a detector can
# filter on. mg-6d2f's outcomes are the first that need to be machine-separable
# from an ordinary failed deploy — "the fleet is not dispatching" and "tonight's
# deploy did not land" want different reactions, and the subject line is the
# wrong place for a consumer to look for the difference.
# The fourth argument is the EVENT TYPE, and it defaults to the one this function
# was written for. A detector filters on the type, not on a subject string, so the
# mg-9fc9 fallback — which is an action taken, not a deploy that failed — must not
# arrive under `deploy_nightly_failed`. Defaulted rather than required so the
# existing callers, all of which really are reporting a failed deploy, are
# unchanged.
alert() {
    local subject="$1" body="$2" extra="${3:-}" etype="${4:-deploy_nightly_failed}" bf rc=0
    err "ALERT: $subject"
    printf '%s\n' "$body" >&2

    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type="$etype" --agent=pogo-deploy \
            --details="{\"subject\":\"$subject\"${extra:+,$extra}}" >/dev/null 2>&1 || true
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
    # A step this run KILLED is classified from that observation and not from a
    # probe (mg-56ac). The probe measures a later instant — the transport may
    # well be up by the time it runs — and the direct fact that the call did not
    # return in GIT_TIMEOUT is both stronger and about the right moment. Calling
    # a killed step `remote` would send the reader to ssh keys for a hang.
    if ${GIT_STEP_TIMED_OUT:-false}; then
        SYNC_CLASS=timeout
        log "sync: the git step did not return within ${GIT_TIMEOUT}s and was killed — classified TIMEOUT from that observation, not from a probe taken afterwards"
        return 0
    fi
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

# ---------------------------------------------------------------------------
# 5a-ter. THE POSITIVE CONTROL (mg-db96)
# ---------------------------------------------------------------------------
# Everything above this line probes ONE endpoint — the deploy remote — and then
# describes the result to a human. So "this box is off the network" and "this
# one remote is blackholed" have, until now, arrived at the reader as the same
# sentence, and a couple of them have arrived carrying a number the reader was
# told to treat as a measurement (remedy_for_sync_class still says READ THE
# VIGIL DURATION AS A MEASUREMENT, and it is a measurement of the wrong thing:
# a lower bound on "one host:port did not answer").
#
# scripts/lib/net-control.sh is the missing half. It probes a reference set that
# has nothing to do with the deploy remote and proves its own instrument in both
# directions on every run, so a failure here finally carries information. Read
# that file for what the control can and cannot establish before quoting it.
#
# THIS DOES NOT CHANGE CLASSIFICATION, deliberately. Making `network` conditional
# on the control is a real improvement and it belongs with the drellem2/pogo#130
# fix (mg-0218), which is the change that makes the classification honest in the
# first place. What lands here is the EVIDENCE: the control runs once per run, on
# the paths where a human or an event will read the outcome, and its verdict goes
# into the log, the alert and the event details. A later change can gate on
# NET_CONTROL_VERDICT; nothing has to be re-derived for it.
#
# WHERE IT COMES FROM, and why the failure to find it is loud. The nightly does
# not run out of the repo — `pogo service install-deploy` copies this script to
# ~/.pogo/bin/pogo-deploy.sh and the library alongside it, precisely so the job
# keeps working while the checkout it builds from is mid-fetch or broken. If the
# library is missing anyway, the runner does NOT fall back to a probe with no
# control and it does not go quiet: net_control reports `unknown` naming the
# paths it tried, and that string travels into the alert like any other verdict.
NET_CONTROL_LIB=""
NET_CONTROL_RAN=false
NET_CONTROL_TRIED=""

load_net_control() {
    local d cand
    # `:-` is not decoration. Under `set -u`, bash 3.2 leaves BASH_SOURCE unset
    # in some sourced-from-a-subshell contexts, and an unguarded expansion here
    # aborts the whole runner while looking for an optional library.
    d="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || d=""
    local -a cands=()
    [ -n "${POGO_NET_CONTROL_LIB:-}" ] && cands+=("$POGO_NET_CONTROL_LIB")
    # Installed layout first (~/.pogo/bin/net-control.sh, a sibling of this
    # script), then the in-repo layout (scripts/launchd/.. -> scripts/lib).
    [ -n "$d" ] && cands+=("$d/net-control.sh" "$d/../lib/net-control.sh")
    for cand in ${cands[@]+"${cands[@]}"}; do
        if [ -r "$cand" ]; then
            # shellcheck source=/dev/null
            if source "$cand" 2>/dev/null; then
                NET_CONTROL_LIB="$cand"
                log "net-control: loaded the positive control from $cand"
                return 0
            fi
            err "net-control: $cand exists but could not be sourced"
        fi
    done
    # The stub. It is a function with the same name and the same contract, and
    # the one thing it must never do is return a verdict it cannot back.
    #
    # The path list is a GLOBAL and not a local of this function, because a bash
    # function body is not a closure: the stub below is parsed here but RUNS
    # later, by which time a `local` would be long out of scope and the
    # expansion would abort the runner under `set -u`. Found by running it.
    NET_CONTROL_TRIED="${cands[*]:-(no candidate paths could be derived)}"
    net_control() {
        NET_CONTROL_VERDICT="unknown"
        NET_CONTROL_REASON="the positive-control library was not found, so NOTHING independent of the deploy remote was probed and this run cannot tell 'this box is off the network' from 'this one remote is blackholed'. Tried: $NET_CONTROL_TRIED. Fix: pogo service install-deploy"
        NET_CONTROL_SELFTEST="not run (library missing)"
        NET_CONTROL_EVIDENCE=""
        NET_CONTROL_DNS="not run"
        NET_CONTROL_PROBED=0
        NET_CONTROL_ANSWERED=0
        return 2
    }
    net_control_line() { printf 'net-control: %s — %s\n' "$NET_CONTROL_VERDICT" "$NET_CONTROL_REASON"; }
    net_control_report() {
        printf 'NETWORK POSITIVE CONTROL: UNAVAILABLE\n  %s\n' "$NET_CONTROL_REASON"
    }
    NET_CONTROL_VERDICT="unknown"
    NET_CONTROL_REASON="not run"
    NET_CONTROL_SELFTEST="not run"
    NET_CONTROL_EVIDENCE=""
    NET_CONTROL_DNS="not run"
    NET_CONTROL_PROBED=0
    NET_CONTROL_ANSWERED=0
    err "net-control: no positive-control library found (tried: $NET_CONTROL_TRIED) — a per-remote failure tonight will be UNINTERPRETABLE. Run: pogo service install-deploy"
    return 1
}

# run_net_control — once per run, memoized. The control costs one bounded sweep
# of a handful of TCP connects; the reason it is memoized is not cost but
# meaning. Two verdicts from two instants in the same alert is a thing a reader
# has to reconcile, and there is no question here that a second sweep answers.
run_net_control() {
    $NET_CONTROL_RAN && return 0
    NET_CONTROL_RAN=true
    net_control >/dev/null 2>&1 || true
    log "$(net_control_line)"
    return 0
}

# net_control_bridge — the sentence that says what the control CHANGES about the
# remedy printed under it.
#
# Without this the alert would carry two blocks that do not refer to each other,
# and the reader would have to do the join. The join is the whole point of the
# ticket, so it is written down.
#
# It also qualifies the one claim in remedy_for_sync_class that this control
# proves too strong. That paragraph tells the reader to READ THE VIGIL DURATION
# AS A MEASUREMENT and offers it to mg-5515 as a lower bound on how long the
# transport was unreachable. The vigil re-probes ONE endpoint, so it is a lower
# bound on how long THAT endpoint did not answer — which becomes a statement
# about this box only if the box was off the network, and that is exactly the
# question the control is here to answer. The remedy paragraphs themselves are
# left alone; the correction is placed where the number is read, and is stated
# in whichever of the three directions the control actually established.
net_control_bridge() {
    case "$NET_CONTROL_VERDICT" in
        up)
            cat <<EOF
WHAT THAT MEANS FOR THE REMEDY BELOW: this box reached other hosts, so the
failure is specific to the deploy remote and not to your connectivity. The vigil
duration above is a lower bound on how long THAT ONE ENDPOINT did not answer —
not on how long this box was off the network, which it demonstrably was not.
Do not quote it as an outage duration.
EOF
            ;;
        down)
            cat <<EOF
WHAT THAT MEANS FOR THE REMEDY BELOW: this box could not reach ANYTHING, so this
is not about the deploy remote. Start at the link — DHCP lease, Wi-Fi, VPN — and
not at ssh keys or the repository. This is the mg-964e shape. The vigil duration
above is a lower bound on the outage for once, because the control establishes
that the endpoint's silence was the box's silence.
EOF
            ;;
        *)
            cat <<EOF
WHAT THAT MEANS FOR THE REMEDY BELOW: the control could not establish either
state, so the remedy below is the runner's best reading of ONE endpoint's
behaviour and nothing corroborates it. In particular the vigil duration is a
lower bound on how long that one endpoint did not answer, and NOT on how long
this box was off the network — do not quote it as an outage duration without
checking the link yourself.
EOF
            ;;
    esac
}

# ---------------------------------------------------------------------------
# 5a-bis. Bounding a call that may never return (mg-56ac)
# ---------------------------------------------------------------------------
# kill_tree PID [SIG] — signal a process and its descendants, LEAVES FIRST.
#
# Leaves first is the whole content of this function. `git fetch` execs an ssh
# or git-remote-https child and it is the CHILD that holds the half-open socket;
# killing git alone can leave that child running and the socket held, which on
# this box is indistinguishable from the hang we were trying to end. pgrep is
# used when present and its absence degrades to killing the named process only,
# which is still strictly better than the unbounded wait it replaces.
# KILL_TREE_SKIP is a pid the walk must not signal or descend into. The run
# deadline's watchdog is itself a descendant of the run it is killing, so without
# this it kills itself partway through and never reaches the SIGKILL.
KILL_TREE_SKIP=""

kill_tree() {
    local pid="${1:-}" sig="${2:-TERM}" child
    [ -n "$pid" ] || return 0
    [ "$pid" = "${KILL_TREE_SKIP:-}" ] && return 0
    for child in $(pgrep -P "$pid" 2>/dev/null); do
        kill_tree "$child" "$sig"
    done
    kill -"$sig" "$pid" 2>/dev/null || true
}

# self_pid — this process's OWN pid, which `$$` does not give inside a subshell:
# bash 3.2 (what macOS ships) has no BASHPID, and `$$` stays the parent's. The
# watchdog needs it to exclude itself from the kill.
self_pid() { sh -c 'echo $PPID'; }

# run_bounded SECONDS CMD... — run CMD with a wall-clock bound.
#
# Returns CMD's own status, or BOUNDED_RC (124, `timeout`'s convention) when the
# bound expired; BOUNDED_TIMED_OUT says which, because 124 is also a status a
# command may legitimately return.
#
# This is a subshell and a `sleep`, not a `timeout` binary: coreutils is not on a
# stock macOS, and adding a binary that must be resolved at 03:00 to the path
# that has to work when everything else is broken is the mg-015f mistake. A
# `sleep` cannot block on the network, which is the property being bought.
#
# SECONDS <= 0 runs the command unbounded, deliberately — a controlled run that
# wants to observe a hang rather than end it says POGO_DEPLOY_GIT_TIMEOUT=0.
BOUNDED_RC=124
BOUNDED_TIMED_OUT=false
run_bounded() {
    local secs="${1:-0}"; shift
    BOUNDED_TIMED_OUT=false
    if [ "$(( 10#$secs ))" -le 0 ]; then "$@"; return $?; fi

    local mark p k rc
    mark="$(mktemp)" || { "$@"; return $?; }
    rm -f "$mark"

    "$@" &
    p=$!
    (
        sleep "$secs"
        kill -0 "$p" 2>/dev/null || exit 0
        # The mark is written BEFORE the kill, so a race between the command
        # finishing and the killer firing resolves as "timed out" rather than as
        # a mystery exit status. Over-reporting a timeout costs a retry; under-
        # reporting one costs the night.
        : > "$mark"
        kill_tree "$p" TERM
        sleep 5
        kill_tree "$p" KILL
    ) &
    k=$!

    wait "$p" 2>/dev/null; rc=$?
    # kill_tree, not `kill`: the killer is a subshell blocked in `sleep`, and a
    # plain TERM to it leaves that sleep ORPHANED — still running, and still
    # holding every file descriptor it inherited. Measured while writing this:
    # the test suite's own stdout pipe was held open by leaked `sleep 300`
    # processes long after the suite had finished, so the run looked hung. In
    # production it would leave one such process per bounded call for up to
    # GIT_TIMEOUT seconds. A bound that leaks the process implementing it is not
    # tidy enough to be trusted with the rest of this.
    kill_tree "$k" TERM
    wait "$k" 2>/dev/null

    if [ -f "$mark" ]; then
        BOUNDED_TIMED_OUT=true
        rc="$BOUNDED_RC"
    fi
    rm -f "$mark"
    return "$rc"
}

# harden_git_transport — make git bound its OWN transport (mg-56ac).
#
# Preferred over run_bounded's kill, and layered under it rather than instead of
# it. A git that gives up by itself returns a status and an error message, which
# classify_transport can read and the alert can print verbatim; a killed git
# leaves nothing but the kill. So the killer is the backstop and this is the
# first line — the 08-05 night, which was loud and recoverable, is what a
# transport that times out ITSELF looks like.
#
# Not exported to the whole run: pogo-self-deploy does its own git work and
# these values are chosen for a fetch of one small repo at 03:00, not for
# whatever it does.
harden_git_transport() {
    # Below 1000 bytes/s for 60s is a dead transfer, not a slow one. This is the
    # only knob git offers for the HTTP transport and it covers the shape that
    # hung here — bytes stop arriving and the socket is never closed.
    export GIT_HTTP_LOW_SPEED_LIMIT="${GIT_HTTP_LOW_SPEED_LIMIT:-1000}"
    export GIT_HTTP_LOW_SPEED_TIME="${GIT_HTTP_LOW_SPEED_TIME:-60}"
    # ssh has no equivalent knob for a stalled session, so it gets a connect
    # bound plus keepalives: three unanswered 15s probes end the session with an
    # error instead of holding it open forever.
    export GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o BatchMode=yes}"
    log "git: transport bounded — http low-speed ${GIT_HTTP_LOW_SPEED_LIMIT}B/s over ${GIT_HTTP_LOW_SPEED_TIME}s, ssh ConnectTimeout=15 + keepalives, and every step capped at ${GIT_TIMEOUT}s"
}

# git_step CMD... — run a git step, keep its stderr in SYNC_DETAIL, and still
# print it. The log is the operator's primary artifact and losing the ssh line
# from it to gain it in the mail would be a straight trade, not an improvement.
#
# BOUNDED since mg-56ac. A step that exceeds GIT_TIMEOUT is killed and reported
# as class `timeout`, which is retryable for the same reason `network` is: a call
# that never returned established NOTHING about the tree. Without this the fetch
# on 2026-08-08 ran for 31h39m and the run's own drain cap never applied, because
# the run never got as far as the drain.
git_step() {
    local f rc
    # Cleared on ENTRY, not only on the paths that set it. The mktemp fallback
    # below returns early, and a stale `true` left by a previous step would make
    # classify_transport report the NEXT failure as a timeout it did not observe
    # — a detector asserting something it did not measure, which is the defect
    # this whole file is about.
    GIT_STEP_TIMED_OUT=false
    f="$(mktemp)" || {
        SYNC_DETAIL=""
        run_bounded "$GIT_TIMEOUT" "$@"; rc=$?
        $BOUNDED_TIMED_OUT && GIT_STEP_TIMED_OUT=true
        return "$rc"
    }
    run_bounded "$GIT_TIMEOUT" "$@" 2>"$f"; rc=$?
    SYNC_DETAIL="$(cat "$f" 2>/dev/null)"
    [ -s "$f" ] && cat "$f" >&2
    rm -f "$f"
    if $BOUNDED_TIMED_OUT; then
        GIT_STEP_TIMED_OUT=true
        SYNC_DETAIL="the step did not return within ${GIT_TIMEOUT}s and was killed: $*${SYNC_DETAIL:+
$SYNC_DETAIL}"
        err "git: step exceeded ${GIT_TIMEOUT}s and was killed — $*"
    else
        GIT_STEP_TIMED_OUT=false
    fi
    return "$rc"
}

# Set by git_step on every call. Read by the classifier: a killed step must be
# reported as a timeout and not as whatever the reachability probe happens to
# say a moment later, because the probe measures a LATER instant and the run has
# a direct observation of its own.
GIT_STEP_TIMED_OUT=false

# git_q CMD... — a bounded git call whose OUTPUT is what is wanted.
#
# THIS EXISTS BECAUSE THE FIRST VERSION OF THIS FIX HAD THE DEFECT IT WAS
# FIXING. git_step bounds the four steps that go through it — clone, fetch,
# checkout, ff-merge — and every OTHER git call in this file was left bare:
# `remote get-url origin`, `status --porcelain`, `rev-parse --short HEAD`. Two of
# those run on the failure path immediately after a fetch that has just been
# killed for hanging, against the same unreachable remote. The suite caught it by
# hanging: the runner was killed for a hung fetch and then blocked forever in the
# `remote get-url` that classifies the failure.
#
# `remote get-url` reads .git/config and is local — which is exactly the reasoning
# that leaves a call unbounded, and it was wrong here: the process was measured
# sitting in it. Whether the block was the call or the shell around it, an
# unbounded call in the failure path of a timeout is not defensible, so every git
# call in this file now goes through one of these two helpers and a test enforces
# it. A timed-out query yields the empty string, which every caller already
# handles because they all pass `2>/dev/null` and treat a blank as absent.
git_q() {
    run_bounded "$GIT_TIMEOUT" "$@"
}

sync_src() {
    SYNC_CLASS=""; SYNC_DETAIL=""
    local remote=""
    if [ ! -d "$SRC/.git" ]; then
        remote="$DEPLOY_REMOTE"
        if [ -z "$remote" ]; then
            remote="$(git_q "$GIT" -C "$BOOTSTRAP_REPO" remote get-url origin 2>/dev/null)"
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
        remote="$(git_q "$GIT" -C "$SRC" remote get-url origin 2>/dev/null)"
        [ -n "$remote" ] || remote="$DEPLOY_REMOTE"
        classify_transport "$remote"
        return 1
    fi

    if [ -n "$(git_q "$GIT" -C "$SRC" status --porcelain 2>/dev/null)" ]; then
        SYNC_CLASS=dirty
        SYNC_DETAIL="$(git_q "$GIT" -C "$SRC" status --short 2>&1)"
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
    log "sync: $SRC at $DEPLOY_REF $(git_q "$GIT" -C "$SRC" rev-parse --short HEAD)"
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
#
#   `timeout` (mg-56ac) joins the retryable side, and it is the clearest case in
#     the list: a call that never returned established nothing at all, not even
#     that the far end is reachable. It is what the 2026-08-08 fetch would have
#     been classified as had anything been bounding it, and treating it as
#     settled would keep the night lost for the one class where re-asking is
#     most obviously worth it.
sync_class_retryable() {
    case "${1:-}" in
        network|remote|unclassified|timeout) return 0 ;;
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
        timeout)      echo "TIMEOUT — a git step did not return within ${GIT_TIMEOUT}s and was killed" ;;
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
        timeout)
            cat <<EOF
A git step DID NOT RETURN within ${GIT_TIMEOUT}s and this runner killed it. That is a
direct observation of this run, not an inference from a probe taken afterwards,
so do not go looking for a dirty or diverged tree: the sync never got far enough
to have an opinion about the repository.

THIS IS THE 2026-08-08 SHAPE, BOUNDED. That night the same step never returned
and nothing killed it: the run wrote nine lines, went silent for 31h39m, held the
deploy lock and the quiesced fleet for all of it, and produced no exit code and
no alert. You are reading this mail because that no longer happens quietly.

The runner retried — $SYNC_TRIES attempts over ${SYNC_RETRY_SPENT}s, ${SYNC_VIGIL_SPENT}s of it vigil — so the
condition outlasted all of it rather than being a single unlucky socket.

  ssh -T git@github.com          # 'successfully authenticated' is the good answer
  curl -sS -o /dev/null -w '%{http_code}\\n' https://api.github.com
  git -C ~/.pogo/deploy-src fetch origin      # by hand, and time it

A half-open connection on a flaky link presents exactly this way and this box's
network is independently known to be intermittent (mg-0ffc, mg-dd22). If the
manual fetch is fast, nothing needs fixing here; the bound did its job and the
next fire will carry the deploy.
EOF
            ;;
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
# 5c. THE FALLBACK THAT NEEDS NO REMOTE (mg-9fc9)
# ---------------------------------------------------------------------------
# THE PROPERTY, MEASURED. The nightly deploy restarts the fleet, and it is this
# box's only AUTOMATIC recovery path. On any of the five nights 2026-08-15..19 it
# would have ended a 118-hour blackout. It could not, because it needs the same
# network the fault had taken out: `ssh: Could not resolve hostname github.com`,
# 30 retries over 7980s, rc=10. Nothing above misbehaved — every tier retried,
# the classifier refused to guess a cause the transport never let it establish,
# and both mayor and human were paged. It is a property of the topology, not a
# defect in a component: THE RECOVERY MECHANISM SHARES A DEPENDENCY WITH THE
# FAILURE IT RECOVERS FROM. Recovery, when it came, came from outside the system
# entirely — Daniel typed a message.
#
# So: after N consecutive nights lost at the TRANSPORT step, do the half of a
# deploy that needs no remote. Restart the fleet. It delivers no new code — that
# genuinely needs the network — but the agents were not broken by anything a
# restart could not clear, so it ends the blackout on night N instead of on
# whichever night a human notices.
#
# FOUR CONSTRAINTS, each from something the incident showed:
#
# 1. KEYED ON THE TRANSPORT, NOT ON "THE DEPLOY FAILED". A night that failed
#    because the TREE is bad has a different fault and a different remedy, and
#    bouncing the fleet over it is destructive noise. transport_streak_verdict
#    below is that discriminator, and it has THREE answers rather than two —
#    `config` fails before any network call at all, so it is evidence in neither
#    direction and must not be counted as either.
#
# 2. N > 1, AND IN THE CONFIG. One failed night is a bad night; the signal is the
#    run. POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER defaults to 2 — the earliest
#    defensible threshold, because the cost of being wrong is one drained,
#    announced restart of a fleet that had already gone two nights without one,
#    and the cost of waiting is a night of blackout per increment. 0 disables it.
#
# 3. IT ANNOUNCES ITSELF OUT OF BAND. If the network is down the announcement
#    cannot go through the network, so it goes to the LOCAL maildir (mayor and
#    human, via alert) and to the LOCAL event log. Both survive exactly this
#    fault; a webhook or an issue comment would not.
#
# 4. A BOUNCE IS NOT FREE, SO THE DRAIN STILL RULES. `pogo-self-deploy bounce`
#    runs the same drain gate a redeploy does and REFUSES --force. If the drain
#    refuses — polecats holding commits that exist only in their worktree, or a
#    fleet whose state could not be established — this reports and does not
#    bounce. That refusal is correct and is not something a 03:00 job overrides.
#
# HOW THIS FIX COULD EXHIBIT THE DEFECT IT REMEDIES. It is a recovery path, so
# the question is which of ITS dependencies the fault it recovers from can take:
#
#   - THE WINDOW. The vigil probes until drain_budget hits zero (~05:30 under the
#     production window), which is the deploy's budget — RESERVE is 1200s of
#     build, do_prove, kickstart and verification. A bounce owes none of the
#     build and none of the prove, so charging it the deploy's reserve would have
#     left ZERO window on precisely the nights the vigil ran long: the fallback
#     would have been disabled by the patience that discovered the outage. Hence
#     BOUNCE_RESERVE (300s) and a budget recomputed against it.
#   - THE SCRIPT. `bounce` lives in $SRC/scripts/pogo-self-deploy — the checkout
#     the sync just failed to advance. Stale is fine (a bounce reads no tree),
#     ABSENT is not: a first-ever run whose clone never happened has no script
#     there at all. So the resolver tries the bootstrap checkout too, and each
#     candidate must PROVE it has a `bounce` by running --help, the same
#     execution-not-existence check `mg` and `git` get here (mg-b72a).
#   - THE ANNOUNCEMENT. mg writes to a local maildir; `pogo events emit` talks to
#     pogod over loopback — and pogod is the process being killed. So the event
#     is emitted BEFORE the kickstart and re-emitted after with the outcome, both
#     best-effort, and the mail is the channel that is actually relied on.
#   - THE STREAK RECORD ITSELF. It lives under POGO_HOME, is read with the same
#     tolerance as the attempt stamp, and an unreadable one reads as "no streak"
#     — so a corrupt file costs a delayed bounce, never a spurious one.
#
# WHAT IT STILL DOES NOT REACH, stated because it would otherwise read as solved.
# Two of these are the ticket's own structure surviving inside its fix, and they
# are named rather than quietly accepted:
#
#   - THE DRAIN COUPLING, and it is the sharp one. The fallback fires because the
#     fleet has gone N nights without a restart, which is a state in which agents
#     are MORE likely to be wedged — and a wedged polecat that committed and never
#     pushed is exactly what makes the drain refuse. So the condition the fallback
#     exists for can produce the condition that blocks it. `--force` is the lever
#     that would cut through it and it is refused, deliberately and on the
#     ticket's instruction: the work in that worktree exists nowhere else, and an
#     unattended 03:00 job is the last caller that should decide to destroy it.
#     What this fix owes that case is not an override but VISIBILITY — the refusal
#     is mailed, names the holders, and says the fleet is still owed a restart.
#   - THE HOST. This fallback lives inside the nightly job. A fault that stops
#     the job from firing at all — the mac asleep through the window, launchd not
#     loading the plist — takes the fallback with it, which is mg-f867's shape one
#     level up. It is not the fault being handled here (a transport outage does
#     not stop launchd), and the detector for a fire that did not happen is
#     internal/staleness/nofire.go, which is a different mechanism on purpose.
#   - AND A BOUNCE CANNOT HELP A FAULT A RESTART CANNOT CLEAR. It delivers no
#     code, so a box that needs a merge to become healthy stays unhealthy.

# The night's transport-failure streak: "<date> <count> <last_bounce_date>".
#
# Its own line, not a field on the attempt stamp: the attempt stamp answers "what
# happened TONIGHT" and is meaningless the moment the date rolls, while this one
# exists only to survive that roll. Fusing them would mean a stamp read by
# attempt_disposition carrying a field only this cares about, and the count would
# be lost the first time a night wrote a stamp without it.
TRANSPORT_STREAK="${POGO_DEPLOY_TRANSPORT_STREAK:-${POGO_HOME:-$HOME/.pogo}/deploy-transport-streak.stamp}"
TRANSPORT_BOUNCE_AFTER="${POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER:-2}"

# What the fallback owes after its drain returns: the kickstart, verify_running's
# 60s, verify_orchestration's 60s, the mail-check post-check and some slack. Not
# RESERVE's 1200s — that number is mostly `go install` and do_prove, and a bounce
# runs neither. Charging the deploy's reserve here is the failure mode described
# in the header: it zeroes the budget on every night the vigil used the window.
BOUNCE_RESERVE="${POGO_DEPLOY_BOUNCE_RESERVE:-300}"

# transport_streak_verdict CLASS -> bump | clear | leave. Pure.
#
#   bump  — the transport was the fault. `network`, `remote`, `unclassified` and
#           `timeout` all mean the sync never reached the tree, so this night
#           delivered nothing AND restarted nothing. Exactly the class of night
#           that costs a blackout. (It is the same set sync_class_retryable
#           returns true for, and for the same underlying reason — but it is
#           asked here as its own question, because "retry in ten minutes" and
#           "the fleet has now gone N nights without a restart" are different
#           decisions that happen to share a discriminator today.)
#   clear — the sync DID reach the tree. `dirty`, `diverged` and `checkout` are
#           all read AFTER a successful fetch, so the transport worked tonight
#           whatever else went wrong. The streak is broken, and a tree fault must
#           never accumulate toward a fleet bounce.
#   leave — the run never got as far as the transport. `config` fails before any
#           network call is made. A night that never asked the question is
#           evidence in neither direction, and counting it either way would be
#           this file's oldest defect: asserting something that was not
#           established.
transport_streak_verdict() {
    case "${1:-}" in
        network|remote|unclassified|timeout) echo bump ;;
        dirty|diverged|checkout)             echo clear ;;
        config)                              echo leave ;;
        # An empty or unknown class reaching here means sync_src failed in a way
        # nothing has classified. `leave` rather than `bump`: an unrecognised
        # class is not a measured transport failure, and the asymmetry is the
        # right way round — a missed bounce costs a night, a wrong one costs a
        # fleet-wide restart nobody asked for.
        *)                                   echo leave ;;
    esac
}

# transport_streak_field LINE N — field N of the record, with the defaults a
# missing or malformed record must degrade to. Same tolerance as the attempt
# stamp (section 1d): unreadable reads as "nothing recorded", so a corrupt file
# delays a bounce rather than inventing one.
transport_streak_field() {
    local line="$1" n="$2" d c b
    read -r d c b <<<"${line:-}"
    case "${c:-}" in ''|*[!0-9]*) c=0 ;; esac
    case "$n" in
        1) printf '%s' "${d:--}" ;;
        2) printf '%s' "$c" ;;
        3) printf '%s' "${b:--}" ;;
    esac
}

# transport_streak_next TODAY LINE — the count after recording a transport-lost
# night for TODAY. IDEMPOTENT PER DATE, and that is load-bearing: a night can
# reach the settling path more than once (three fires all reopened by rc=10 with
# a schedule this run could not read), and a streak that counted fires instead of
# nights would cross any threshold in a single night.
transport_streak_next() {
    local today="$1" line="$2" d c
    d="$(transport_streak_field "$line" 1)"
    c="$(transport_streak_field "$line" 2)"
    if [ "$d" = "$today" ]; then printf '%s' "$c"; else printf '%s' "$(( c + 1 ))"; fi
}

# transport_streak_save FILE DATE COUNT LAST_BOUNCE
transport_streak_save() {
    mkdir -p "$(dirname "$1")" 2>/dev/null
    printf '%s %s %s\n' "$2" "$3" "$4" > "$1"
}

# transport_bounce_due COUNT [THRESHOLD] — is the fallback owed? A threshold of
# 0 (or one that is not a number) disables it, and disabled is reported by the
# caller rather than passed over silently.
transport_bounce_due() {
    # ${2-...}, NOT ${2:-...}: an explicitly EMPTY threshold means "there is no
    # threshold", and reading it as "use the default" would turn an unset config
    # value into a fleet bounce. Only an ABSENT second argument falls back.
    local count="$1" after="${2-$TRANSPORT_BOUNCE_AFTER}"
    case "${after:-}" in ''|*[!0-9]*) return 1 ;; esac
    [ "$after" -gt 0 ] || return 1
    [ "$count" -ge "$after" ]
}

# The script that can bounce, resolved by EXECUTION (mg-b72a's rule, applied to a
# third primitive). Two candidates, both LOCAL:
#
#   $SRC/scripts/pogo-self-deploy         the deploy checkout. Stale is fine — a
#                                         bounce reads no tree and no ref.
#   $BOOTSTRAP_REPO/scripts/pogo-self-deploy
#                                         the dev checkout, for the one case the
#                                         first cannot cover: a box whose clone
#                                         never happened, which is the `clone`
#                                         arm of the very failure being handled.
#
# The dev tree is otherwise never touched by this job, and the exception is
# narrow and specific: a bounce runs no `go install`, reads no tree and cannot
# deploy anything from it, so a dirty or half-rebased dev checkout is harmless
# here in a way it would never be for a build.
#
# Each candidate must ADVERTISE the subcommand. A pogo-self-deploy older than
# mg-9fc9 exits 2 on `bounce` with "unknown subcommand", which is a correct
# refusal reported in the wrong voice — the fallback would read as broken rather
# than as absent. Asking --help first turns that into a named, reported cause.
BOUNCE_SCRIPT=""
resolve_bounce_script() {
    local cand
    local -a cands=("$SRC/scripts/pogo-self-deploy" "$BOOTSTRAP_REPO/scripts/pogo-self-deploy")
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        if ! run_bounded "$TOOL_PROBE_TIMEOUT" "$cand" --help 2>/dev/null | grep -q 'pogo-self-deploy bounce'; then
            log "fallback: $cand runs but its --help does not advertise a 'bounce' subcommand — it predates mg-9fc9, so it is not a usable fallback"
            continue
        fi
        BOUNCE_SCRIPT="$cand"
        log "fallback: bounce script resolved at $BOUNCE_SCRIPT"
        return 0
    done
    err "fallback: no local pogo-self-deploy with a 'bounce' subcommand among ${cands[*]}"
    return 1
}

# Set by fallback_bounce so the sync alert can cross-reference what the fallback
# did without re-deriving it. `not-considered` is the value on every path that
# never reached the fallback at all, which is most of them.
FALLBACK_STATUS="not-considered"
FALLBACK_DETAIL=""

# fallback_bounce TODAY STREAK — decide, act, and announce.
#
# It owns the announcement rather than leaving it to the caller's alert, because
# a fleet-wide restart is a DIFFERENT event from "tonight's deploy did not land"
# and a reader has to be able to tell them apart in a subject line. The sync
# alert gets one cross-reference line; this gets its own mail.
fallback_bounce() {
    local today="$1" streak="$2" rc=0 bbudget
    if ! transport_bounce_due "$streak"; then
        case "${TRANSPORT_BOUNCE_AFTER:-}" in
            ''|*[!0-9]*|0)
                FALLBACK_STATUS="disabled"
                FALLBACK_DETAIL="POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER=${TRANSPORT_BOUNCE_AFTER:-} disables the fallback" ;;
            *)
                FALLBACK_STATUS="not-due"
                FALLBACK_DETAIL="$streak consecutive transport-lost night(s); the fallback fires at $TRANSPORT_BOUNCE_AFTER" ;;
        esac
        log "fallback: $FALLBACK_STATUS — $FALLBACK_DETAIL"
        return 0
    fi

    log "fallback: $streak consecutive nights lost at the TRANSPORT step (threshold $TRANSPORT_BOUNCE_AFTER) — the fleet has gone that long without the restart the nightly would have given it. Bouncing it WITHOUT a remote (mg-9fc9)."

    # The bounce's own budget, on the bounce's own reserve. See BOUNCE_RESERVE.
    bbudget="$(drain_budget "$WINDOW_END" "$BOUNCE_RESERVE" "$MAX_DRAIN" "$MIN_DRAIN")"
    if [ "$bbudget" -le 0 ]; then
        FALLBACK_STATUS="refused-window"
        FALLBACK_DETAIL="under ${MIN_DRAIN}s of usable window is left before ${WINDOW_END}:00 (bounce reserve ${BOUNCE_RESERVE}s), and a drain that cannot finish stops dispatch for its whole length and delivers nothing"
        err "fallback: NOT bouncing — $FALLBACK_DETAIL"
        fallback_announce "$streak" 0
        return 0
    fi

    if ! resolve_bounce_script; then
        FALLBACK_STATUS="refused-noscript"
        FALLBACK_DETAIL="no local pogo-self-deploy advertising a 'bounce' subcommand could be found (tried $SRC and $BOOTSTRAP_REPO)"
        fallback_announce "$streak" 0
        return 0
    fi

    # BEFORE the kickstart: the emit goes to pogod over loopback and the bounce
    # kills pogod. An event emitted after would be racing the daemon's boot.
    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type=deploy_transport_fallback_bounce --agent=pogo-deploy \
            --details="{\"phase\":\"start\",\"streak\":$streak,\"threshold\":$TRANSPORT_BOUNCE_AFTER,\"sync_class\":\"${SYNC_CLASS:-unclassified}\",\"sync_vigil_s\":$SYNC_VIGIL_SPENT,\"drain_timeout\":$bbudget}" >/dev/null 2>&1 || true
    fi

    local reason_file="$HOME/Library/Logs/pogo/pogo-bounce-reason.$today"
    mkdir -p "$(dirname "$reason_file")" 2>/dev/null || true
    rm -f "$reason_file" 2>/dev/null || true
    # NOT under run_bounded, and for the same reason the redeploy call below is
    # not: this is a long-running orchestration step whose legitimate duration IS
    # the drain budget, and a second bound would either duplicate that number or
    # contradict it. What bounds it is arm_run_deadline — armed at the top of the
    # run, in a separate process, covering whichever stage the run is wedged in
    # (mg-56ac). The fallback is downstream of that arming, so it inherits it.
    log "fallback: $BOUNCE_SCRIPT bounce --yes --drain-timeout $bbudget"
    POGO_DEPLOY_REASON_FILE="$reason_file" "$BOUNCE_SCRIPT" bounce --yes --drain-timeout "$bbudget" || rc=$?

    if [ "$rc" -eq 0 ]; then
        FALLBACK_STATUS="bounced"
        FALLBACK_DETAIL="the fleet was drained and restarted onto the binaries already installed; no code was delivered"
        log "fallback: BOUNCED — $FALLBACK_DETAIL"
        # The streak resets on a bounce, so a prolonged outage bounces once every
        # N nights rather than every night. Re-arming on the same evidence is what
        # makes a fleet that re-wedges recoverable a second time; not resetting
        # would make every remaining night of an outage a fleet restart.
        transport_streak_save "$TRANSPORT_STREAK" "$today" 0 "$today"
    else
        # exit 7 (drain stalled), 6/12 (drain precondition), 5/8/11 (the restart
        # itself). Each already wrote its own sentence into the reason record and,
        # for 7, mailed the deploy-stalled sink. What this adds is the ONE thing
        # only the caller knows: that this was the mg-9fc9 fallback firing, and
        # that the fleet therefore did NOT get the restart it is owed.
        FALLBACK_STATUS="failed"
        FALLBACK_DETAIL="$(bounce_reason_line "$reason_file" "$rc")"
        err "fallback: the bounce exited $rc — $FALLBACK_DETAIL"
        # The streak is NOT reset. Nothing was bounced, so the count still
        # measures how long the fleet has gone without a restart — and tomorrow
        # night is a fresh chance at a drain that may no longer refuse.
    fi
    fallback_announce "$streak" "$rc"
    if [ -n "$POGO_CLI" ]; then
        "$POGO_CLI" events emit --type=deploy_transport_fallback_bounce --agent=pogo-deploy \
            --details="{\"phase\":\"end\",\"status\":\"$FALLBACK_STATUS\",\"exit\":$rc,\"streak\":$streak,\"threshold\":$TRANSPORT_BOUNCE_AFTER}" >/dev/null 2>&1 || true
    fi
    return 0
}

# bounce_reason_line FILE RC — the sentence the bounce itself wrote, or an honest
# statement that it left none. Same channel and same reader as the deploy's
# reason record (mg-0155): the caller does not re-derive a story from an integer.
bounce_reason_line() {
    local f="$1" rc="$2" reason=""
    [ -f "$f" ] && reason="$(sed -n 's/^reason=//p' "$f" | head -1)"
    if [ -n "$reason" ]; then
        printf 'exit %s at stage %s: %s' "$rc" "$(sed -n 's/^stage=//p' "$f" | head -1)" "$reason"
    else
        printf 'exit %s (%s) — the bounce left no reason record at %s' "$rc" "$(describe_exit "$rc")" "$f"
    fi
}

# fallback_subject STREAK — the part that travels. A reader skimming a phone at
# 07:00 has to be able to tell "the fleet was restarted" from "the fallback could
# not restart it", because those need opposite reactions, so the status is in the
# subject and not only in the body.
fallback_subject() {
    local streak="$1"
    case "$FALLBACK_STATUS" in
        bounced) echo "[pogo-deploy] FLEET BOUNCED by the no-remote fallback — $streak nights lost to the transport" ;;
        failed)  echo "[pogo-deploy] no-remote fallback COULD NOT bounce the fleet — $streak nights lost to the transport" ;;
        *)       echo "[pogo-deploy] no-remote fallback DECLINED to bounce — $streak nights lost to the transport" ;;
    esac
}

# fallback_body STREAK — the announcement's body.
#
# A FUNCTION that cats a heredoc, not a heredoc inside the callsite's `$( )`.
# macOS ships bash 3.2 and its command-substitution scanner does not reliably
# survive a heredoc whose text contains apostrophes and parentheses — `$(cat
# <<EOF ...)` with this body in it fails to parse outright, which is a whole
# runner that will not start. Every other long body in this file is built the
# same way for the same reason (red_alert_body, lost_schedule_body).
fallback_body() {
    local streak="$1" headline vigil
    case "$FALLBACK_STATUS" in
        bounced)
            headline="THE FLEET WAS RESTARTED. Not deployed — restarted. No new code was
delivered, because delivering code needs the remote and the remote is what has
been unreachable." ;;
        failed)
            headline="THE FALLBACK FIRED AND DID NOT COMPLETE. The fleet has now gone $streak
nights without the restart the nightly deploy would have given it, and this run
could not supply one either." ;;
        *)
            headline="THE FALLBACK WAS DUE AND DID NOT RUN. The fleet has gone $streak nights
without the restart the nightly deploy would have given it." ;;
    esac
    if [ "${SYNC_VIGIL_SPENT:-0}" -gt 0 ]; then
        vigil="${SYNC_VIGIL_SPENT}s — a LOWER BOUND on how long the transport was unreachable tonight"
    else
        vigil="none tonight (the failure was not one the vigil covers, or the vigil is off)"
    fi
    cat <<EOF
$headline

  nights lost:  $streak consecutive, at the TRANSPORT step (threshold $TRANSPORT_BOUNCE_AFTER)
  tonight:      sync class ${SYNC_CLASS:-unclassified} — $(describe_sync_class "$SYNC_CLASS")
  vigil:        $vigil
  outcome:      $FALLBACK_STATUS — $FALLBACK_DETAIL
  streak file:  $TRANSPORT_STREAK
  log:          $HOME/Library/Logs/pogo/pogo-deploy.log

WHY THIS EXISTS. The nightly deploy is this box's only automatic recovery path
and it needs the same network a network fault takes away. Between 2026-08-15 and
2026-08-19 that cost a 118-hour blackout: five nights, five correct refusals, and
recovery only when a human typed a message. A restart needs no remote, so after
$TRANSPORT_BOUNCE_AFTER consecutive nights lost at the transport step the fleet gets one anyway.

WHAT A BOUNCE DOES AND DOES NOT FIX. It clears anything a restart clears — wedged
agents, stale sessions, a scheduler that stopped firing. It delivers NO code, so
this box is no more current than it was, and every merge waiting on main is still
waiting. The deploy is still owed and will be attempted again tomorrow night.

THE DRAIN STILL RULES. The bounce runs the same drain gate a redeploy does and it
refuses --force, so a polecat holding commits that exist only in its worktree
stops it — reported, not overridden. If the outcome above is a refusal, it is
either that or the window running out, and both are the designed answer rather
than a malfunction.

Worth knowing when you read a refusal: the state this fallback fires in makes
that refusal MORE likely, not less. A fleet that has gone $streak nights without
a restart is a fleet more likely to hold a wedged polecat, and a wedged polecat
that committed without pushing is precisely what the drain refuses to orphan. So
a drain refusal here is not noise — it is naming the agent that has to be dealt
with by hand before anything can restart the fleet.

WHAT TO DO
  - outcome 'bounced': nothing, unless the fleet is still not working. Check with
    pogo agent list, and curl -s http://127.0.0.1:10000/server/mode
  - a refusal or a failure: read the outcome line above, then the deploy log. A
    drain refusal names the polecats holding unpushed work.
  - Either way the NETWORK is the ticket, not the deploy: $streak consecutive
    nights unable to reach the remote is the fault to chase.
  - To change the threshold, set POGO_DEPLOY_TRANSPORT_BOUNCE_AFTER. 0 disables
    the fallback entirely.
EOF
}

# fallback_announce STREAK RC — the out-of-band announcement, constraint 3.
#
# Through alert(), which mails ALERT_TO and human out of the LOCAL maildir and
# writes the body to the deploy log. Nothing here goes near the network, which is
# the point: on the night this fires, the network is what is broken.
fallback_announce() {
    local streak="$1" rc="$2"
    alert "$(fallback_subject "$streak")" "$(fallback_body "$streak")" "\"fallback\":\"$FALLBACK_STATUS\",\"streak\":$streak,\"threshold\":$TRANSPORT_BOUNCE_AFTER,\"bounce_exit\":$rc,\"sync_class\":\"${SYNC_CLASS:-unclassified}\"" deploy_transport_fallback
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
        # 11 and 12 are mg-6d2f's, and they name the two halves of "the restart
        # step did not restart the server" — the failure Daniel reported by hand
        # after the 08-07 night, having found the fleet stopped and started it
        # himself. Before them, 11's state passed verification silently (nothing
        # read the run mode at all) and 12's shared exit 6 with the bootstrap and
        # not-answering cases.
        11) echo "FLEET DOWN: pogod restarted but orchestration did NOT start — /agents, /refinery, /scheduler are all 503" ;;
        12) echo "FLEET DOWN: orchestration was ALREADY stopped, so the deploy refused before the restart — nothing was bounced and nothing was fixed" ;;
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

# fleet_is_down RC — does this outcome leave the fleet not dispatching?
#
# THE SUBJECT LINE IS THE PART THAT TRAVELS. On 2026-08-07 the alert subject was
# "[pogo-deploy] RED: nightly redeploy exited 6" — indistinguishable at a glance
# from a build failure, which is a thing that can wait until morning. A stopped
# fleet cannot: it cost 10h39m of a dead fleet, ended by the user noticing by
# hand. Whatever the body says, a reader who skims one line has to come away
# knowing the difference between "tonight's deploy did not land" and "nothing is
# running right now."
#
# Scoped to the outcomes where the fleet is provably not dispatching when the
# script exits:
#   5   the kickstart itself failed after a successful install
#   8   the restart landed but no pogod came back at main's revision
#   11  a pogod came back, in index-only mode, dispatching nothing
#   12  orchestration was already off and this run did not restart it
#
# NOT 6, 7 or 9. Those exit with the old pogod alive and dispatching — a missed
# deploy, not an outage — and putting them under the same banner would spend the
# banner. A subject that shouts on every failure is the generic subject again.
fleet_is_down() {
    case "$1" in
        5|8|11|12) return 0 ;;
        *)         return 1 ;;
    esac
}

# alert_subject RC — the one line a skim-reader gets.
alert_subject() {
    if fleet_is_down "$1"; then
        echo "[pogo-deploy] FLEET DOWN: nightly redeploy exited $1 and pogod is NOT serving the fleet"
    else
        echo "[pogo-deploy] RED: nightly redeploy exited $1"
    fi
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
        11)
            cat <<'EOF'
THE FLEET IS DOWN RIGHT NOW. The kickstart landed and a pogod came back at main's
revision — /version answers and every ordinary liveness check on this box reads
GREEN — but it came back in index-only mode. /agents, /refinery and /scheduler
are all returning HTTP 503, which means no polecat is being dispatched and no
merge is running, and none will be until orchestration is started.

This is not a deploy to retry tomorrow; it is an outage to end now:

  curl -s http://127.0.0.1:10000/server/mode      # expect {"mode":"full"}
  pogo server start
  curl -s http://127.0.0.1:10000/server/mode      # confirm it took

The binary on disk and the running process are both the NEW one, so once
orchestration is up the deploy is complete — do not roll back or re-run it.
EOF
            ;;
        12)
            cat <<'EOF'
THE FLEET WAS ALREADY DOWN when this deploy started, and this deploy did not fix
it. pogod is up and answering, but orchestration is stopped: POST /agents/drain
returned 503 from RequireOrchestration, so the deploy could not drain, refused to
kickstart -k an unquiesced fleet, and exited before building or restarting
anything.

The refusal is correct — bouncing without a drain mints polecats that survive the
kill and hold their claims and worktrees forever. What is NOT correct is the
state it leaves: no dispatch, no refinery, no scheduler, and a box on which every
health signal still reads green.

Fix the mode first, then redeploy:

  curl -s http://127.0.0.1:10000/server/mode      # expect {"mode":"full"}
  pogo server start
  scripts/pogo-self-deploy redeploy --yes

Nothing was built and nothing was bounced, so the running pogod is untouched and
still behind main. The drain flag is a red herring here: it was never enabled,
because the same guard refused the request that would have enabled it.
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
#
# ABOVE ALL OF IT, when the fleet is down: the banner (mg-6d2f). A mail client
# shows the first line in its preview and a reader who opens it starts there;
# burying "the fleet is stopped" under six lines of attempt/drain/elapsed
# bookkeeping is how the 08-07 alert managed to be sent, delivered, and still
# cost 10h39m.
red_alert_body() {
    local rc="$1" elapsed="$2" today="$3" budget="$4" file="${5:-}"
    local changed detail
    changed="$(what_the_run_changed "$file")"
    detail="$(deploy_reason_detail "$file")"
    if fleet_is_down "$rc"; then
        printf '%s\n\n' 'THE FLEET IS NOT DISPATCHING RIGHT NOW. This is an outage, not a missed deploy —
no polecat will start and no merge will run until pogod is serving the fleet
again. The remedy is below; everything between here and it is context.'
    fi
    printf '%s\n' "The unattended nightly redeploy FAILED, $(fires_left_phrase).

  exit $rc:  $(describe_outcome "$rc" "$file")
  attempt:   $(( ATTEMPT_N + 1 )) tonight ($today)
  drain:     up to ${budget}s were allowed
  elapsed:   ${elapsed}s
  checkout:  $SRC ($(git_q "$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null))
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
# 8b. Whose schedule was it, and is that agent still there? (mg-6d7b)
# ---------------------------------------------------------------------------
# A missing mail-check has (at least) two causes with OPPOSITE remedies:
#
#   (a) the agent is alive and lost its schedule  -> nudge it to re-register
#   (b) the agent is GONE and the schedule was reaped with it -> START the agent
#
# One observation — "present before, absent after" — cannot tell them apart, and
# for eight months this check printed (a)'s remedy unconditionally. On 2026-08-10
# it fired on case (b): doctor was absent from `pogo agent list` entirely, and
# `pogo nudge doctor` had nothing to nudge. A nudge into the void returns no
# error worth noticing, so an agent following the printed remedy literally would
# have concluded the fleet was restored.
#
# So: read the registry before composing the mail, and branch. And where the
# registry cannot be read at all, say THAT — an alert that does not know must not
# print a confident remedy, because a confident wrong remedy is what this fixes.

# mail_check_owners — "<schedule-id> <agent> <type>" per line, for the same
# schedules mail_check_ids lists, read at the same moment.
#
# The owner is NOT derivable from the id and must never be guessed from it. A
# polecat's schedule is keyed on its WORK ITEM (mail-check-mg-6d7b) while the
# agent is named after something else entirely (c6d7b); a remedy that strips the
# "mail-check-" prefix names an agent that does not exist, which is the same
# class of unusable remedy this section exists to remove. The type is captured
# here, pre-bounce, for the same reason: a gone agent is gone from the registry,
# so afterwards there is nothing left to ask what kind it was.
mail_check_owners() {
    [ -n "$POGO_CLI" ] || { echo "?"; return 1; }
    local sched agents
    sched="$("$POGO_CLI" schedule list --json 2>/dev/null)" || { echo "?"; return 1; }
    # An unreadable agent list is not fatal here: the id->agent mapping is still
    # worth having, and an empty type just costs the remedy its polecat branch.
    agents="$("$POGO_CLI" agent list --json 2>/dev/null)" || agents=""
    # One awk over both documents, separated by a sentinel that cannot occur in
    # either: the agents pass builds name->type, the schedules pass consumes it.
    # RS="{" makes each record one JSON object, so a field is read from the
    # object it belongs to rather than from whatever line happens to be nearby.
    { printf '%s' "$agents"; printf '\nPOGODEPLOYSPLIT\n'; printf '%s' "$sched"; } | awk '
        function field(name,   s) {
            if (!match($0, "\"" name "\"[ \t]*:[ \t]*\"[^\"]*\"")) return ""
            s = substr($0, RSTART, RLENGTH)
            sub("^\"" name "\"[ \t]*:[ \t]*\"", "", s)
            sub(/"$/, "", s)
            return s
        }
        BEGIN { RS = "{"; phase = 0 }
        # The sentinel shares a record with the LAST agent object (records break
        # at "{", so an object body runs up to the next one). Read that record
        # as an agent FIRST, then switch — flipping on sight would drop it.
        phase == 0 {
            n = field("name"); if (n != "") type[n] = field("type")
            if ($0 ~ /POGODEPLOYSPLIT/) phase = 1
            next
        }
        {
            id = field("id"); agent = field("agent")
            if (id !~ /^mail-check-/ || agent == "") next
            print id, agent, (agent in type ? type[agent] : "")
        }
    ' | sort -u
}

# schedule_owner ID OWNERS — the agent a schedule belonged to, "" if unmapped.
schedule_owner() {
    printf '%s\n' "$2" | awk -v id="$1" '$1 == id { print $2; exit }'
}

# schedule_owner_type ID OWNERS — that agent's pre-bounce type, "" if unknown.
schedule_owner_type() {
    printf '%s\n' "$2" | awk -v id="$1" '$1 == id { print $3; exit }'
}

# agent_states — "<name> <status>" per line, from the same registry
# `pogo agent list` reads. That command's own help says presence here is NOT
# liveness, and it means it: a PARKED agent is listed, with pid 0 and
# status "parked". Reading presence alone would call it alive and prescribe a
# nudge for a process that is not there — the very defect this section removes,
# reintroduced by the fix for it. So carry the status, and let the caller decide.
#
# Exits 1 with NO output when the registry cannot be read at all. That is a
# distinct answer from "the registry is empty": collapsing them would make an
# unreachable pogod report every agent as gone and prescribe a start for a fleet
# that is fine.
agent_states() {
    [ -n "$POGO_CLI" ] || return 1
    local out
    out="$("$POGO_CLI" agent list --json 2>/dev/null)" || return 1
    [ -n "$out" ] || return 1
    printf '%s' "$out" | awk '
        function field(name,   s) {
            if (!match($0, "\"" name "\"[ \t]*:[ \t]*\"[^\"]*\"")) return ""
            s = substr($0, RSTART, RLENGTH)
            sub("^\"" name "\"[ \t]*:[ \t]*\"", "", s)
            sub(/"$/, "", s)
            return s
        }
        BEGIN { RS = "{" }
        { n = field("name"); if (n != "") print n, field("status") }' | sort -u
    return 0
}

# agent_state NAME STATES — one agent's registry status, "" if not listed.
agent_state() {
    printf '%s\n' "$2" | awk -v n="$1" '$1 == n { print $2; exit }'
}

# lost_schedule_verdict ID OWNERS STATES — "<verdict> <agent> <status>", where
# verdict is alive | gone | parked | odd | unknown. STATES is the newline-
# separated "<name> <status>" registry, or the single token "?" when it could
# not be read.
lost_schedule_verdict() {
    local id="$1" owners="$2" states="$3" agent state
    agent="$(schedule_owner "$id" "$owners")"
    if [ -z "$agent" ]; then echo "unknown ? ?"; return 0; fi
    if [ "$states" = "?" ]; then echo "unknown $agent ?"; return 0; fi
    state="$(agent_state "$agent" "$states")"
    case "$state" in
        # Not in the registry at all: the agent went and took the schedule.
        "")        echo "gone $agent absent" ;;
        running)   echo "alive $agent running" ;;
        # Listed, deliberately dormant. Losing its mail-check is the reap
        # working, and BOTH other remedies are wrong for it.
        parked)    echo "parked $agent parked" ;;
        # Listed in some other state (exited, restarting, ...). Not running, and
        # not obviously startable either — report the state rather than pick.
        *)         echo "odd $agent $state" ;;
    esac
}

# lost_schedule_remedy ID VERDICT AGENT TYPE STATE — the paragraph for one id.
lost_schedule_remedy() {
    local id="$1" verdict="$2" agent="$3" type="$4" state="${5:-}"
    case "$verdict" in
        alive)
            printf '%s\n' "  $id — $agent IS RUNNING. The agent survived the bounce and its
    schedule did not, so it can put the schedule back itself:

        pogo nudge $agent

    'pogo agent list' reports presence, not liveness — but the nudge checks: it
    confirms delivery from the agent's own submission receipts and FAILS if none
    arrives, so a nudge that reports success really did land."
            ;;
        parked)
            printf '%s\n' "  $id — $agent is PARKED. A parked agent is dormant on purpose and stays
    dormant across restarts, so its mail-check being reaped is the reap working.
    Neither other remedy applies: there is no process to nudge, and starting it
    would undo the parking. Act only if it should NOT be parked:

        pogo agent wake $agent"
            ;;
        odd)
            printf '%s\n' "  $id — $agent is in the registry with status '$state', which is neither
    running nor parked, so this alert will not choose a remedy for it. If that
    says 'restarting', pogod is already bringing it back and the schedule should
    return on its own — re-read before acting:

        pogo agent list
        pogo agent diagnose $agent"
            ;;
        gone)
            if [ "$type" = "polecat" ]; then
                printf '%s\n' "  $id — $agent is NOT RUNNING, and it was a POLECAT before the bounce.
    Its schedule was reaped with it. A polecat that finished during the bounce
    taking its mail-check with it is the reap working, not a fault — and
    'pogo agent start' does not apply to polecats. Check whether the work is
    still owed before doing anything:

        mg show ${id#mail-check-}"
            else
                printf '%s\n' "  $id — $agent is NOT RUNNING; its schedule was reaped with it. Nudging
    it CANNOT work — there is no process to nudge, and a nudge into the void
    reports nothing worth noticing. Start the agent instead:

        pogo agent start $agent

    If $agent goes missing on EVERY nightly bounce, it is not declared with
    auto_start = true in a prompt file pogod discovers ('pogo agent prompt
    list' shows what it discovers, and 'pogo agent start' reads
    ~/.pogo/agents/crew/$agent.md), so it will not come back on its own and
    somebody has to run the line above each time.

    DO NOT flip auto_start on to silence this mail. For some agents the false
    setting is deliberate and load-bearing — doctor's mitigates mg-8677, where
    the reap lets auto_start override a corpse (mg-d9d1, mg-d6ac). This alert
    is the instrument for an agent that cannot restart itself; a quieter mail
    bought with a live reap bug is a bad trade."
            fi
            ;;
        *)
            if [ "$agent" = "?" ]; then
                printf '%s\n' "  $id — the schedule list did not name an owner for this id, so this
    alert CANNOT say which agent to act on. Do not guess it from the id: a
    polecat's schedule is keyed on its work item, not on its agent name.

        pogo schedule list --json | grep -A2 $id"
            else
                printf '%s\n' "  $id — could NOT determine whether $agent is running (the agent registry
    was unreadable). The two remedies are opposites and this alert does not
    know which applies. Look first, then act:

        pogo agent list          # is $agent there?
        pogo nudge $agent        # ONLY if it is
        pogo agent start $agent  # ONLY if it is not"
            fi
            ;;
    esac
}

# lost_schedule_body LOST OWNERS STATES [GRACE] — the whole mail body.
lost_schedule_body() {
    local lost="$1" owners="$2" states="$3" grace="${4:-$GRACE}"
    printf '%s\n' "The redeploy succeeded, but ${grace}s later these mail-check schedules that
existed before the bounce are gone:
"
    local id v verdict agent state type
    while IFS= read -r id; do
        [ -n "$id" ] || continue
        # "<verdict> <agent> <state>", split positionally rather than re-derived,
        # so the paragraph is written about the classification that was made.
        v="$(lost_schedule_verdict "$id" "$owners" "$states")"
        verdict="${v%% *}"; v="${v#* }"
        agent="${v%% *}"; state="${v#* }"
        type="$(schedule_owner_type "$id" "$owners")"
        lost_schedule_remedy "$id" "$verdict" "$agent" "$type" "$state"
        printf '\n'
    done <<EOF
$lost
EOF
    printf '%s\n' "The fleet's mail loop is degraded and WILL LOOK HEALTHY — pogod is up, the port
answers, the agents that are still there are alive. Diagnose:

  pogo schedule list | grep mail-check
  pogo agent list
  pogo agent diagnose <name>"
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

# ---------------------------------------------------------------------------
# 9b. The whole-run deadline (mg-56ac)
# ---------------------------------------------------------------------------
# WHY THE DRAIN CAP WAS NOT ENOUGH, said plainly because it is the thing most
# likely to be re-derived wrongly: on 2026-08-08 the run was allowed 7200s of
# drain and ran for 113 598s, and there is no contradiction there. The cap bounds
# ONE stage. The run hung before reaching it. Any bound that names a stage can be
# defeated by hanging in a different one, so this one names none: it is wall
# clock against the whole run, from the first line to the last.
#
# DEADLINE_RC is `timeout`'s conventional 124, so an operator reading an exit
# status has a fighting chance of recognising it without this file.
DEADLINE_RC=124
DEADLINE_GRACE="${POGO_DEPLOY_DEADLINE_GRACE:-30}"
# How long the watchdog's own alert may take before the kill proceeds without
# it. Two minutes is generous for two `mg mail send` calls and short enough that
# a wedged mail path cannot turn the bound back into an unbounded run.
DEADLINE_ALERT_BOUND="${POGO_DEPLOY_DEADLINE_ALERT_BOUND:-120}"

# run_deadline END SLACK MAX_DRAIN RESERVE [H M S] — how long this run may take.
#
# Derived from the window for mg-8f7e's reason: the number that constrains this
# run is when the window closes, and a constant calibrated against today's drain
# stops being derived from anything the first time the window moves. SLACK is
# what the run is allowed to overrun the window by before it is a hang rather
# than a slow night — generous, because killing a live deploy mid-kickstart is
# worse than one that finishes at 06:20.
#
# The FLOOR is the longest a legitimate run can take at all (a full drain, plus
# the reserve the build and bounce need, plus slack). It is what makes the
# out-of-window controlled run — POGO_DEPLOY_SKIP_WINDOW=1 at 14:00, where the
# distance to the window's end is deeply negative — get a usable deadline
# instead of an immediate kill.
run_deadline() {
    local end=$(( 10#$1 )) slack=$(( 10#$2 )) max=$(( 10#$3 )) reserve=$(( 10#$4 ))
    local h m s left floor
    if [ $# -ge 7 ]; then h="$5"; m="$6"; s="$7"; else read -r h m s <<<"$(now_hms)"; fi
    left=$(( end * 3600 - (10#$h * 3600 + 10#$m * 60 + 10#$s) + slack ))
    floor=$(( max + reserve + slack ))
    [ "$left" -lt "$floor" ] && left="$floor"
    printf '%s' "$left"
}

# still_this_script PID — is that pid still THIS runner?
#
# A guard against the one way a watchdog can do real damage: the run ends, its
# pid is recycled, and hours later the watchdog kills whatever inherited it.
# on_exit disarms the watchdog synchronously, so the window is tiny — but "tiny"
# is not "closed", and the cost of being wrong is killing an unrelated process
# on Daniel's machine at 06:30.
still_this_script() {
    local pid="${1:-}" cmd
    [ -n "$pid" ] || return 1
    cmd="$(ps -o command= -p "$pid" 2>/dev/null)" || return 1
    case "$cmd" in
        *pogo-deploy.sh*) return 0 ;;
        *) return 1 ;;
    esac
}

# on_deadline SECS TARGET — what the watchdog does when the bound expires.
#
# ORDER IS THE ARGUMENT HERE TOO. The log lines come first because they are the
# one channel that cannot fail; the alert is attempted next but bounded, so an
# alert path that is itself wedged delays the kill by at most two minutes; and
# the kill happens whether or not either worked. A watchdog that can be stopped
# by the same fault it is watching for is not a watchdog.
#
# It kills the tree from the LEAVES so the run's own EXIT trap gets a chance to
# run: bash defers a trapped signal until the foreground command returns, so
# TERMing only the shell would leave a run blocked in `git fetch` exactly as
# wedged as before. Killing the fetch first unblocks the shell, the TERM trap
# fires, and the run records its own night and drops its own lock. The SIGKILL
# after DEADLINE_GRACE is for when that does not happen — and then the run writes
# no terminal line, which is precisely the case the did-not-run witness reports.
on_deadline() {
    local secs="$1" target="$2" backstop
    KILL_TREE_SKIP="$(self_pid)"

    err "DEADLINE EXCEEDED: this run has been alive for ${secs}s without finishing, and is being KILLED."
    err "A run that STOPS is not a run that FAILS: it has no exit code, sends no alert and sets no stamp, so on 2026-08-08 a run of this shape was scored as a night that ran (mg-56ac). This line is that night, bounded."
    log "deadline: killing pid $target and its descendants (grace ${DEADLINE_GRACE}s, then SIGKILL)"

    # THE KILL IS ARMED BEFORE THE ALERT, in a process of its own, so that
    # nothing below — a wedged mg, a hung `go env`, a mail server that accepts
    # and stalls — can prevent the run from ending. This is the same argument
    # one level down that the watchdog itself is: the thing that guarantees the
    # bound must not be reachable from the fault.
    (
        sleep "$DEADLINE_ALERT_BOUND"
        kill -0 "$target" 2>/dev/null || exit 0
        kill_tree "$target" TERM
        sleep "$DEADLINE_GRACE"
        kill_tree "$target" KILL
    ) &
    backstop=$!

    if [ -n "$POGO_CLI" ]; then
        run_bounded 30 "$POGO_CLI" events emit --type=deploy_nightly_deadline --agent=pogo-deploy \
            --details="{\"deadline_seconds\":$secs,\"pid\":$target}" >/dev/null 2>&1 || true
    fi

    # The watchdog resolves its OWN macguffin. It forked before the run reached
    # resolve_mg, so it did not inherit one — and that is the right way round
    # anyway: the run may have hung IN that resolution, and a watchdog holding a
    # value copied from the run it is killing is trusting the wedged process.
    if [ -z "$MG" ]; then
        resolve_mg >/dev/null 2>&1 || true
    fi

    if [ -n "$MG" ]; then
        # Bounded, and its status deliberately unread — the runner's standing
        # rule is that no alert callsite branches on delivery (mg-7dc1), and it
        # is doubly right here: whether the mail went out changes nothing about
        # whether this run must be killed. The two log lines bracket it, so a
        # bound that expired mid-send is visible in the record without any
        # control flow depending on it.
        log "deadline: mailing the RED before the kill (bounded at ${DEADLINE_ALERT_BOUND}s, with the kill already armed behind it)"
        run_bounded "$DEADLINE_ALERT_BOUND" alert "[pogo-deploy] KILLED: the nightly run exceeded its ${secs}s deadline" \
"$(deadline_alert_body "$secs" "$target")" "\"exit\":$DEADLINE_RC,\"deadline_seconds\":$secs"
        log "deadline: alert path returned — proceeding to the kill"
    else
        err "deadline: NO macguffin could be resolved, so NOTHING HAS BEEN MAILED. The kill still happens and the log line above is the only record — treat a log with a DEADLINE line and no mail as an alert path that is itself broken."
    fi

    kill_tree "$backstop" TERM
    kill_tree "$target" TERM
    sleep "$DEADLINE_GRACE"
    if kill -0 "$target" 2>/dev/null; then
        err "deadline: pid $target survived SIGTERM for ${DEADLINE_GRACE}s — SIGKILL. The run will write no terminal line, so this night reads as a hang to the did-not-run witness, which is what it was."
        kill_tree "$target" KILL
    fi
}

# deadline_alert_body SECS TARGET — the mail. It leads with what is TRUE RIGHT
# NOW about the fleet, not with the deploy, because a run killed mid-drain
# leaves dispatch frozen and that is the fact worth waking up to.
deadline_alert_body() {
    local secs="$1" target="$2"
    cat <<EOF
The unattended nightly redeploy was KILLED after ${secs}s. It did not fail — it
STOPPED, which until mg-56ac produced no exit code, no alert and no stamp at all.

  deadline: ${secs}s (window ends ${WINDOW_END:-?}:00, slack ${DEADLINE_SLACK}s)
  pid:      $target
  log:      $HOME/Library/Logs/pogo/pogo-deploy.log

CHECK THE FLEET BEFORE THE DEPLOY. A run killed after it quiesced the fleet
leaves it quiesced, and every ordinary liveness signal on this box still reads
green while it is: the agents exited cleanly and on request, so restart_on_crash
is correct not to fire. On 2026-08-08 that was 33 hours of no crew.

  curl -s http://127.0.0.1:10000/version
  curl -s http://127.0.0.1:10000/server/mode      # expect {"mode":"full"}
  pogo agent list                                 # expect a crew, not an empty list
  curl -s -X POST http://127.0.0.1:10000/agents/drain -d '{"draining":false}'

THEN READ WHERE IT STOPPED. The last timestamped line before the silence bounds
the stage; the gap in the timestamps is the stall:

  grep -n 'pogo-deploy:' $HOME/Library/Logs/pogo/pogo-deploy.log | tail -20

Nothing here diagnoses WHY, and the kill destroys some of the evidence for it —
that is the trade this deadline makes deliberately. An unbounded run keeps its
evidence and costs a day; a bounded one costs a night and tells you the same
morning.
EOF
}

# arm_run_deadline SECS TARGET — start the watchdog.
#
# A separate PROCESS, not a trap or an alarm inside the run: the thing being
# bounded may be unable to run anything at all, and the whole lesson of this
# lineage is that a component asked to report its own silence never does.
arm_run_deadline() {
    local secs="${1:-0}" target="${2:-}"
    if [ "$(( 10#$secs ))" -le 0 ] || [ -z "$target" ]; then
        log "deadline: NOT ARMED — this run is unbounded (POGO_DEPLOY_RUN_DEADLINE=$secs). A run that hangs will be silent until the did-not-run witness reports it."
        return 0
    fi
    (
        sleep "$secs"
        kill -0 "$target" 2>/dev/null || exit 0
        still_this_script "$target" || exit 0
        on_deadline "$secs" "$target"
    ) &
    WATCHDOG_PID=$!
    log "deadline: armed — the WHOLE run is bounded at ${secs}s regardless of which stage is stuck (watchdog pid $WATCHDOG_PID, run pid $target)"
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
# It now also writes the run's TERMINAL LINE and disarms the deadline watchdog,
# and it is armed at the TOP of main rather than after the lock (mg-56ac).
#
# THE TERMINAL LINE IS THE POINT. Before it, a run that stopped forever and a run
# that finished normally produced the same thing in the log — nothing further —
# and no reader inside or outside this process could tell them apart. `end` is
# written LAST, after the stamp and the lock, so it means what it says: every
# other thing this run had to do is already done.
#
# It covers the SKIP paths too, which is why the trap moved up. A fire that was
# late, locked out or already settled exits in milliseconds and is perfectly
# healthy; without a terminal line of its own it would be indistinguishable from
# a fire that started and never came back. LOCK_HELD is what makes the earlier
# arming safe: an unlocked fire must not rmdir the lock the RUNNING deploy holds.
LOCK_HELD=false

on_exit() {
    local rc="$1" elapsed=0
    if [ "$RUN_T0" -gt 0 ]; then
        elapsed=$(( $(date +%s) - RUN_T0 ))
    fi

    # First, because everything below is bookkeeping and the watchdog must not
    # fire against a pid this run is about to stop owning.
    if [ -n "$WATCHDOG_PID" ]; then
        kill_tree "$WATCHDOG_PID" TERM
        WATCHDOG_PID=""
    fi

    if $ATTEMPT_ARMED; then
        ATTEMPT_N=$(( ATTEMPT_N + 1 ))
        if stamp_write "$STAMP" "$(deploy_date)" "$ATTEMPT_N" "$rc"; then
            log "attempt recorded: $(deploy_date) attempt=$ATTEMPT_N rc=$rc ($STAMP)"
        else
            err "could not write the attempt record at $STAMP — a later fire tonight may repeat this attempt"
        fi
    fi
    if $LOCK_HELD; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
    fi
    log "pogo-deploy: end (rc=$rc after ${elapsed}s)"
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

    RUN_T0="$(date +%s)"
    log "pogo-deploy: start (src=$SRC window=$WINDOW dry_run=$DRY_RUN)"

    # Armed HERE, before any gate, so every path out of this run writes a
    # terminal line — including the skips, which exit in milliseconds and are
    # healthy, and which without one look exactly like a run that never came
    # back (mg-56ac). It does not touch the lock until LOCK_HELD says this run
    # owns it.
    trap 'on_exit $?' EXIT
    # So a TERM — from the deadline watchdog, or from an operator — leaves
    # through the trap above with a status of its own, instead of killing the
    # shell outright and leaving the night unrecorded and the lock held.
    trap 'err "terminated (SIGTERM) — the run deadline or an operator ended this run"; exit '"$DEADLINE_RC" TERM

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

    # The deadline is armed as soon as the window is known, because everything
    # after this point can hang: the lock probe, the stamp read, the tool
    # resolution and the sync all touch the filesystem or the network. Arming it
    # later would leave the earliest stages — the ones the 08-08 run died in —
    # outside the only bound that does not name a stage.
    if [ "$(( 10#$RUN_DEADLINE ))" -gt 0 ]; then
        DEADLINE_S="$RUN_DEADLINE"
    else
        DEADLINE_S="$(run_deadline "$WINDOW_END" "$DEADLINE_SLACK" "$MAX_DRAIN" "$RESERVE")"
    fi
    arm_run_deadline "$DEADLINE_S" "$$"

    # Read the schedule from the world, once, now that the run is bounded (the
    # `launchctl print` is itself under run_bounded, but arming first means a
    # wedge here is caught by the same watchdog as everything else). Never
    # fatal: a run that cannot read its own schedule still deploys, it just
    # stops claiming things about later fires — see section 1c.
    resolve_fire_hours || true

    # --- gate 2: one at a time ----------------------------------------------
    if ! acquire_lock; then
        log "lock: another pogo-deploy run holds $LOCK_DIR — exiting 0"
        exit 0
    fi
    LOCK_HELD=true

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
    # Resolving mg proves the alert path can RUN; registering proves it can be
    # DELIVERED. Since mg-d639 those are different questions, and this job mails
    # two fixed names that nothing else provisions (mg-7dc1). Non-fatal.
    register_alert_recipients
    resolve_pogo || log "pogo CLI unresolved — the post-bounce schedule check will be skipped"
    # Never fatal. Without it the sync-failure classifier loses only its ability
    # to assert that a host is DOWN; it can still confirm one is up, and it
    # reports `unclassified` rather than guessing (mg-0d70).
    resolve_nc || true
    # Also never fatal, and loaded HERE rather than lazily at the alert site so
    # a missing library is on the record from the top of the run instead of
    # first appearing in the alert that needed it (mg-db96).
    load_net_control || true
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
    # Before the first network call, and after the git that will make it is
    # known (mg-56ac). A transport that gives up by itself returns an error the
    # classifier can read; the per-step kill below it is the backstop for when
    # it does not.
    harden_git_transport
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
        # The control runs on BOTH exits from here, not just the alerting one.
        # This branch is the quiet one — it writes an event and no mail — and it
        # is the branch that fires on a night the transport was out for the whole
        # window. An event stream recording `sync_class` with no independent
        # reading of the box's connectivity is the same uninterpretable number
        # this control exists to stop producing, just in a machine-readable form.
        run_net_control
        if retry_will_follow "$sync_rc"; then
            local snxt; snxt="$(next_fire_hour "$(current_hour)")"
            log "sync: exit $sync_rc after $SYNC_TRIES attempts — class ${SYNC_CLASS:-unclassified} established nothing, so the $(printf '%02d' "$snxt"):00 fire will retry. Not alerting yet."
            if [ -n "$POGO_CLI" ]; then
                "$POGO_CLI" events emit --type=deploy_nightly_retry_pending --agent=pogo-deploy \
                    --details="{\"exit\":$sync_rc,\"sync_class\":\"${SYNC_CLASS:-unclassified}\",\"sync_attempts\":$SYNC_TRIES,\"sync_vigil_s\":$SYNC_VIGIL_SPENT,\"retry_hour\":$snxt,\"net_control\":\"$NET_CONTROL_VERDICT\"}" >/dev/null 2>&1 || true
            fi
            exit "$sync_rc"
        fi
        # THE NIGHT IS OUT OF FIRES AND THE TRANSPORT NEVER LET THIS RUN REACH
        # THE TREE (mg-9fc9). That is the condition the no-remote fallback is
        # keyed on, and this is the only place in the run where it is true: past
        # the blip tier, past the vigil, and past retry_will_follow, so no later
        # fire tonight can still deploy. See section 5c.
        #
        # $ATTEMPT_ARMED, not "we got here": a dry run reaches this line and must
        # neither count a night nor bounce a fleet.
        local tv streak=0
        tv="$(transport_streak_verdict "$SYNC_CLASS")"
        if $ATTEMPT_ARMED; then
            local sline; sline="$(stamp_read "$TRANSPORT_STREAK")"
            case "$tv" in
                bump)
                    streak="$(transport_streak_next "$today" "$sline")"
                    transport_streak_save "$TRANSPORT_STREAK" "$today" "$streak" \
                        "$(transport_streak_field "$sline" 3)"
                    log "transport streak: $streak consecutive night(s) lost at the transport step (class ${SYNC_CLASS:-unclassified}); the fallback fires at ${TRANSPORT_BOUNCE_AFTER:-0}"
                    fallback_bounce "$today" "$streak" ;;
                clear)
                    transport_streak_save "$TRANSPORT_STREAK" "$today" 0 \
                        "$(transport_streak_field "$sline" 3)"
                    log "transport streak: CLEARED — class ${SYNC_CLASS} is read after a successful fetch, so the transport worked tonight and this is a tree fault, which must never accumulate toward a fleet bounce" ;;
                *)
                    log "transport streak: left at $(transport_streak_field "$sline" 2) — class ${SYNC_CLASS:-unclassified} failed before any network call, so tonight is evidence in neither direction" ;;
            esac
        else
            # Logged rather than silent, on this file's own rule: a skip that says
            # nothing is indistinguishable from a mechanism that is not there.
            log "transport streak: not recorded — this fire never armed an attempt (dry run, or it skipped a gate), so it decides nothing about tonight"
        fi
        alert "[pogo-deploy] ABORTED: could not sync $SRC — $(describe_sync_class "$SYNC_CLASS")" \
"The nightly redeploy refused to advance its dedicated checkout. Nothing was
deployed and the running pogod is untouched. Daniel's dev tree was NOT touched.

  cause:    $(describe_sync_class "$SYNC_CLASS")
  checkout: $SRC
  attempts: $SYNC_TRIES in this run$([ "$SYNC_RETRY_SPENT" -gt 0 ] && printf ', over %ss of waiting' "$SYNC_RETRY_SPENT")
  vigil:    $([ "$SYNC_VIGIL_SPENT" -gt 0 ] && printf '%ss — a LOWER BOUND on how long the transport was unreachable' "$SYNC_VIGIL_SPENT" || printf 'none (the failure was not one the vigil covers, or the vigil is off)')
  exit:     $sync_rc$([ "$sync_rc" -eq 10 ] && printf ' (retryable class — but %s)' "$(fires_left_phrase | sed 's/^and //')")
  log:      $HOME/Library/Logs/pogo/pogo-deploy.log
  fallback: $FALLBACK_STATUS$([ -n "$FALLBACK_DETAIL" ] && printf ' — %s' "$FALLBACK_DETAIL") (the no-remote fleet bounce, mg-9fc9 — mailed separately when it fires)

WHAT THE UNDERLYING TOOL ACTUALLY SAID, verbatim:

${SYNC_DETAIL:-(the failing step produced no output)}

$(net_control_report)
$(net_control_bridge)

$(remedy_for_sync_class "$SYNC_CLASS")"
        exit "$sync_rc"
    fi
    sync_recovery_notice "$SYNC_TRIES" "$SYNC_RETRY_SPENT"

    # THE STREAK IS CLEARED HERE, on the observation and not on an exit code
    # (mg-9fc9). This line is the one place in the run where "the transport
    # worked tonight" is a measured fact: the fetch returned and the tree was
    # read. Everything after it can still fail — a build, a do_prove RED, a
    # stalled drain — and none of those is evidence about the network, so keying
    # the reset on the run's final rc would let a build failure look like a
    # transport recovery, and a transport recovery followed by a build failure
    # look like another lost night.
    if $ATTEMPT_ARMED; then
        local sline0 prev0
        sline0="$(stamp_read "$TRANSPORT_STREAK")"
        prev0="$(transport_streak_field "$sline0" 2)"
        transport_streak_save "$TRANSPORT_STREAK" "$today" 0 "$(transport_streak_field "$sline0" 3)"
        [ "$prev0" -gt 0 ] && log "transport streak: CLEARED (was $prev0) — the sync reached the tree, so the transport is working from this box tonight"
    fi

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
    # Who owns each of them, captured NOW because a schedule that is reaped with
    # its agent leaves nothing behind to ask afterwards (mg-6d7b).
    local pre_owners; pre_owners="$(mail_check_owners)"

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
        # Subject and event field, not only the body (mg-6d2f). The subject is
        # the part that travels — a skim-reader has to be able to tell "tonight's
        # deploy did not land" from "nothing is running right now" — and the
        # event field is what a detector can filter on, which a subject string
        # is not. The banner itself lives at the top of red_alert_body.
        # Hoisted, not inlined into the call. The `&&` an inline
        # `$(fleet_is_down ... && echo true)` would put on the callsite's last
        # line is invisible to a reader and indistinguishable, to the probe that
        # guards this file, from an `|| rc=1` cascade on alert's return value.
        local fleet_down=false
        fleet_is_down "$rc" && fleet_down=true
        alert "$(alert_subject "$rc")" \
            "$(red_alert_body "$rc" "$elapsed" "$today" "$budget" "$reason_file")" \
            "\"exit\":$rc,\"fleet_down\":$fleet_down"
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
        # Hoisted, not inlined: `states="$(agent_states)" || states="?"` on one
        # line reads as an error cascade, and the sentinel matters too much to
        # hide. An unreadable registry becomes "?" — the third answer — and never
        # silently becomes "nothing is running" (mg-6d7b).
        local states
        states="$(agent_states)" || states="?"
        if [ "$states" = "?" ]; then
            log "post-bounce liveness: UNKNOWN — the agent registry could not be read, so the mail will not prescribe a remedy"
        else
            log "post-bounce liveness: $(printf '%s' "$states" | grep -c .) agents in the registry ($(printf '%s\n' "$states" | awk '$2 == "running"' | grep -c .) running)"
        fi
        alert "[pogo-deploy] mail-check schedules LOST across the nightly bounce" \
            "$(lost_schedule_body "$lost" "$pre_owners" "$states" "$GRACE")"
    else
        log "mail-check re-check: OK — every pre-bounce schedule is back ($(printf '%s' "$post" | grep -c . ) present)"
    fi

    log "pogo-deploy: done — pogod redeployed to $(git_q "$GIT" -C "$SRC" rev-parse --short HEAD 2>/dev/null)"
    exit 0
}

# Run main only when executed directly, so the test harness can source this file
# and exercise the pure helpers without firing a deploy.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    main "$@"
fi
