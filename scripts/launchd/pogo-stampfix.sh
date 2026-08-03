#!/bin/bash
# pogo-stampfix.sh — the ONE-SHOT stamp-clearing redeploy (mg-3888).
#
# WHAT THIS IS
# ------------
# A single-use launchd job that breaks one specific deadlock: every deployed
# binary carries a vcs stamp from a DIFFERENT repository, and the only tool that
# would rebuild them refuses to run until the stamp is gone.
#
#   installed pogod: c28e26e28921...  <FOREIGN — no such commit in deploy-src>
#   installed pogo : c28e26e28921...  <FOREIGN — no such commit in deploy-src>
#
# THE DEADLOCK, PRECISELY (mg-3888)
# ---------------------------------
# `pogo-self-deploy redeploy` calls classify_drift before it does anything, and
# classify_drift returns 1 on a FOREIGN STAMP — so cmd_redeploy exits 1 at the
# gate, BEFORE reaching the `go install` that is the only thing which would clear
# that stamp. `--force` and `--skip-drain` both act after the gate, which is why
# neither helps. The nightly com.pogo.deploy has aborted on this every night
# since 2026-07-29.
#
# The break is to do the `go install` OUTSIDE the script, so that by the time
# redeploy runs, the stamp it would have refused is already gone. Nothing in
# pogo-self-deploy is modified or bypassed: it is handed a tree it accepts.
#
# WHY THE go install AND THE REDEPLOY ARE IN ONE JOB
# --------------------------------------------------
# Because splitting them opens the mg-49bc protocol-mismatch window. `go install`
# rewrites BOTH binaries on disk but bounces nothing, so between the install and
# the restart the on-disk `pogo` CLI is the new version while the RUNNING pogod is
# still the old one — and new subcommands 404 against it. Doing the install now
# and leaving the restart to the 03:00 nightly would hold that window open for
# hours. They run back to back here, with only the stamp verification between
# them, so the window is seconds.
#
# WHY IT IS A LAUNCHD JOB AND NOT AN AGENT
# ----------------------------------------
# pogo-self-deploy's first line is assert_out_of_band (mg-1bbf). It refuses two
# callers: a descendant of pogod, and anything with POGO_AGENT_NAME set. Every
# crew agent and every polecat is both, so no agent can ever run the redeploy —
# correctly, because the `launchctl kickstart -k` it ends with kills pogod's whole
# process tree, which would include the caller, mid-deploy, with nothing left
# running to report what happened. The script says it itself: "THE REDEPLOY IS
# LEGITIMATE. THE CALLER IS NOT."
#
# A LaunchAgent is parented by launchd and carries no POGO_AGENT_NAME, so it
# passes the guard BY CONSTRUCTION rather than by exemption — and it survives the
# kickstart for the same reason: launchd owns it, pogod does not. An agent
# authors and installs this file; launchd is what runs it.
#
# WHY THE go install MUST RUN AGAINST deploy-src, FROM HERE
# ---------------------------------------------------------
# THIS IS HOW THE FOREIGN STAMP GOT THERE IN THE FIRST PLACE. A polecat worktree's
# `.git` is a FILE (a gitdir pointer), and `go build` does not follow it — it walks
# UP the directory tree looking for a real `.git` directory, finds ~/.pogo, and
# stamps THAT repo's commit into the binary. So a `go install` run from a polecat
# worktree silently produces exactly the artifact this job exists to clean up.
# deploy-src's `.git` is a real directory, so a build there stamps the right repo.
# assert_real_git_dir below enforces that rather than trusting it.
#
# ONE-SHOT, AND FAIL-CLOSED
# -------------------------
# One-shot: a sentinel file is written BEFORE the redeploy, and any later fire
# that sees it exits 0 without acting. Written before rather than after on
# purpose — the redeploy takes minutes and bounces the fleet, and a second
# calendar fire landing mid-bounce must not start a second one. The cost of that
# choice is that a FAILED run does not retry itself, which is the safe direction:
# a repeating fleet-wide bounce is worse than a stalled one, and the alert says so.
#
# Fail-closed: the fleet is only bounced if the stamp is verifiably fixed. If
# `go install` succeeds but the binaries still name a commit deploy-src does not
# have, this exits WITHOUT redeploying — a bounce would cost the fleet and leave
# the deadlock in place anyway.
#
# WHAT A FAILURE AFTER A SUCCESSFUL INSTALL COSTS
# -----------------------------------------------
# It leaves the mismatch window open — but not indefinitely, and this is the one
# reassuring property of the ordering. Once the stamp is cleared, the deadlock is
# broken PERMANENTLY: classify_drift stops returning 1, so the 03:00 nightly
# (which has been aborting at that same gate) reclassifies as "RESTART owed" and
# finishes the restart on its own. A failed redeploy here degrades to "the nightly
# completes it tonight", not "stuck until a human intervenes".
#
# HYGIENE: ABSOLUTE PATHS, NEVER BARE NAMES (mg-dd5f / mg-015f)
# -------------------------------------------------------------
# launchd hands a job a minimal PATH. Two names are actively dangerous bare:
# /usr/bin/mg is the Micro-Emacs EDITOR, which panics headless and delivers no
# alert at all; and `go` is not on launchd's default PATH, which is how the
# 2026-07-23 manual redeploy died on "go: command not found". So `mg` is resolved
# through an IDENTITY check, `go` and `git` by execution, and pogo-self-deploy by
# absolute path.

# `set -u` only, matching pogo-deploy.sh and pogo-self-deploy — deliberately NOT
# `-o pipefail`. Every pipeline whose status this script actually cares about
# reads PIPESTATUS explicitly (see the redeploy call), so pipefail buys nothing
# here and actively breaks the identity checks: `tool --help | grep -q pattern`
# makes grep exit at the FIRST match and close the pipe, the tool takes SIGPIPE,
# and pipefail turns that 141 into a failed check — silently rejecting the very
# binary it just matched. The checks below avoid the pipe entirely instead of
# relying on that, but the shell option stays off for the same reason upstream
# leaves it off.
set -u

LABEL="${POGO_STAMPFIX_LABEL:-com.pogo.stampfix}"
SRC="${POGO_DEPLOY_SRC:-$HOME/.pogo/deploy-src}"
POGO_HOME="${POGO_HOME:-$HOME/.pogo}"
SENTINEL="${POGO_STAMPFIX_SENTINEL:-$POGO_HOME/stampfix-mg-3888.ran}"
PLIST="${POGO_STAMPFIX_PLIST:-$HOME/Library/LaunchAgents/$LABEL.plist}"
ALERT_TO="${POGO_STAMPFIX_ALERT_TO:-mayor}"
WORK_ITEM="mg-3888"

# POGO_STAMPFIX_DRY_RUN — run everything EXCEPT the fleet-wide bounce.
#
# This exists because a one-shot job is otherwise a script whose first execution
# is also its only test, and this one's untested half ends in `launchctl kickstart
# -k`. Dry-run exercises the whole path that can be exercised — tool resolution,
# the worktree and dirty-tree refusals, the `go install`, and the fail-closed
# stamp verification — and stops at the bounce, printing the command it would
# have run. Point GOBIN at a scratch directory and the rehearsal touches nothing
# the fleet uses.
#
# It deliberately writes NO sentinel, sends NO mail and removes NO plist: a
# rehearsal that armed the one-shot guard would block the real run it was meant
# to de-risk.
DRY_RUN="${POGO_STAMPFIX_DRY_RUN:-}"

# Set once the sentinel has been written, so the exit reporter can distinguish
# "never touched the binaries" from "installed, then something went wrong".
INSTALLED_OK=false
MG=""
GO=""
GIT=""

ts() { date -u '+%Y-%m-%dT%H:%M:%SZ'; }
log() { printf '%s  %s\n' "$(ts)" "$*"; }
err() { printf '%s  ERROR: %s\n' "$(ts)" "$*" >&2; }

# ---------------------------------------------------------------------------
# Tool resolution — identity, not existence (mg-015f / mg-dd5f)
# ---------------------------------------------------------------------------
# /usr/bin/mg satisfies both -x and `command -v mg`; it is the Micro-Emacs editor.
# Locating a candidate is never the same as trusting it, so each must
# self-identify as macguffin before it is accepted. GOPATH candidates are tried
# FIRST so the production run — launchd PATH, /usr/bin ahead of ~/go/bin —
# resolves the real binary without consulting PATH at all.
resolve_mg() {
    local cand gobin gopath pathmg
    local -a cands=()
    gobin="$("${GO:-go}" env GOBIN 2>/dev/null)"
    [ -n "$gobin" ] && cands+=("$gobin/mg")
    gopath="$("${GO:-go}" env GOPATH 2>/dev/null)"
    [ -n "$gopath" ] && cands+=("$gopath/bin/mg")
    cands+=("$HOME/go/bin/mg")
    pathmg="$(command -v mg 2>/dev/null)"
    [ -n "$pathmg" ] && cands+=("$pathmg")

    local out
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        # Captured, not piped: see the note on `set -u` above.
        out="$("$cand" --help 2>/dev/null)"
        case "$out" in *macguffin*) ;; *) continue ;; esac
        MG="$cand"
        log "mg: resolved macguffin at $MG"
        return 0
    done
    err "mg: no macguffin 'mg' among ${cands[*]} — refusing bare 'mg' (that is /usr/bin/mg, the EDITOR)"
    return 1
}

# `go`, resolved by EXECUTION. launchd's default PATH contains neither
# /opt/homebrew/bin nor /usr/local/go/bin, and "go: command not found" is the
# documented way the 2026-07-23 manual redeploy died.
resolve_go() {
    local cand
    for cand in "$(command -v go 2>/dev/null)" /opt/homebrew/bin/go /usr/local/go/bin/go /usr/local/bin/go; do
        [ -n "$cand" ] && [ -x "$cand" ] || continue
        "$cand" version >/dev/null 2>&1 || continue
        GO="$cand"
        log "go: resolved at $GO ($("$cand" version 2>/dev/null))"
        return 0
    done
    err "go: no working go toolchain found; cannot rebuild the binaries"
    return 1
}

# `git`, resolved by EXECUTION rather than existence: /usr/bin/git is the Command
# Line Tools shim, and a damaged CLT leaves it executable, on PATH, and unable to
# complete a single call.
resolve_git() {
    local cand out
    for cand in /opt/homebrew/bin/git /usr/local/bin/git "$(command -v git 2>/dev/null)" /usr/bin/git; do
        [ -n "$cand" ] && [ -x "$cand" ] || continue
        out="$("$cand" --version 2>/dev/null)"
        case "$out" in 'git version'*) ;; *) continue ;; esac
        GIT="$cand"
        log "git: resolved at $GIT"
        return 0
    done
    err "git: no working git found"
    return 1
}

# ---------------------------------------------------------------------------
# Alerting
# ---------------------------------------------------------------------------
# Mails BOTH the coordinator and `human`: this job bounces the entire fleet, and
# the operator has to be able to find out what happened to it from outside the
# fleet it just restarted.
alert() {
    local subject="$1" body="$2" bf
    log "ALERT: $subject"
    [ -n "$MG" ] || { err "no alert path (mg unresolved) — alert not sent: $subject"; return 1; }
    bf="$(mktemp -t pogo-stampfix-alert)" || return 1
    printf '%s\n' "$body" >"$bf"
    "$MG" mail send "$ALERT_TO" --from=pogo-stampfix --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || err "alert: mg mail send to '$ALERT_TO' failed"
    "$MG" mail send human --from=pogo-stampfix --subject="$subject" --body-file "$bf" >/dev/null 2>&1 \
        || err "alert: mg mail send to 'human' failed"
    rm -f "$bf"
}

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------
# The guard that would have prevented this ticket. A polecat worktree's .git is a
# FILE; go build does not follow it and stamps whatever real repo lies above.
assert_real_git_dir() {
    if [ -f "$SRC/.git" ]; then
        err "$SRC/.git is a FILE (a gitdir pointer), so this is a worktree, not a checkout."
        err "go build does not follow that pointer — it would walk UP and stamp the PARENT repo's"
        err "commit into the binaries, which is exactly the foreign stamp this job exists to clear."
        return 1
    fi
    if [ ! -d "$SRC/.git" ]; then
        err "$SRC/.git is not a directory — $SRC is not a standalone git checkout"
        return 1
    fi
    log "provenance: $SRC/.git is a real directory — a build here stamps this repo"
    return 0
}

# ps_parent PID — this pid's PARENT pid and this pid's OWN comm, on one line.
ps_parent() {
    ps -o ppid=,comm= -p "$1" 2>/dev/null | head -1
}

# pogod_ancestor — mirrors assert_out_of_band's own walk (pogo-self-deploy:183).
# Echoes "<pid> <comm>" for the nearest pogod ANCESTOR and returns 0; returns 1
# when the walk reaches the root without finding one. Matches on the BASENAME of
# comm, because ps reports pogod as the absolute path it was exec'd from, which
# varies per machine. The depth cap is a termination guarantee, not a policy.
pogod_ancestor() {
    local pid="${1:-$$}" depth=0 parent comm self=true
    while [ "$depth" -lt 64 ]; do
        depth=$((depth + 1))
        read -r parent comm <<<"$(ps_parent "$pid")"
        # The starting process is stepped over without being tested: the question
        # is ancestry, not identity.
        if ! $self && [ -n "${comm:-}" ] && [ "$(basename "$comm")" = "pogod" ]; then
            printf '%s %s' "$pid" "$comm"
            return 0
        fi
        self=false
        case "${parent:-}" in ''|*[!0-9]*) return 1 ;; esac
        [ "$parent" -gt 1 ] || return 1
        pid="$parent"
    done
    return 1
}

# Refuse from inside pogod's tree BEFORE touching any binary.
#
# BOTH of assert_out_of_band's refusals are re-checked here, not just the cheap
# one. pogo-self-deploy would refuse either caller anyway — but it would do so
# AFTER our `go install` had already rewritten both binaries, which leaves the
# mg-49bc mismatch window open with no restart coming to close it. A caller that
# cannot complete the second step must not be allowed to perform the first.
#
# Checking POGO_AGENT_NAME alone would miss the case the upstream guard puts
# FIRST: a shell that pogod spawned but which carries no agent name. Refusing up
# front means a wrong caller changes nothing at all.
assert_not_agent() {
    local ancestor
    if ancestor="$(pogod_ancestor)"; then
        err "this process is a descendant of pogod (pid ${ancestor%% *}, ${ancestor#* })."
        err "The redeploy ends in 'launchctl kickstart -k', which kills pogod's ENTIRE process"
        err "tree — this process with it, mid-deploy, with nothing left to report what happened."
        err "assert_out_of_band would refuse it AFTER the go install had already rewritten the"
        err "binaries, opening the mg-49bc mismatch window with no restart coming."
        err "This job must be run by launchd, which pogod does not own."
        return 1
    fi
    if [ -n "${POGO_AGENT_NAME:-}" ]; then
        err "POGO_AGENT_NAME=${POGO_AGENT_NAME} is set — this is a pogod-spawned agent"
        err "(its parent chain no longer shows pogod, which is what a detached agent looks like)."
        err "assert_out_of_band would refuse it AFTER the go install had already rewritten the"
        err "binaries, opening the mg-49bc mismatch window with no restart coming."
        err "This job must be run by launchd, not by an agent."
        return 1
    fi
    return 0
}

# The revision baked into an installed binary, or the empty string.
installed_rev() {
    local bin="$1"
    [ -x "$bin" ] || return 1
    "$GO" version -m "$bin" 2>/dev/null \
        | sed -n 's/.*vcs\.revision=\([0-9a-f]*\).*/\1/p' | head -1
}

# The whole foreign-stamp test, re-derivable by hand:
#   git -C $SRC cat-file -e <rev>^{commit}
rev_in_repo() {
    [ -n "$1" ] || return 1
    "$GIT" -C "$SRC" cat-file -e "${1}^{commit}" 2>/dev/null
}

gobin_dir() {
    local gobin
    gobin="$("$GO" env GOBIN 2>/dev/null)"
    [ -n "$gobin" ] && { echo "$gobin"; return; }
    echo "$("$GO" env GOPATH 2>/dev/null)/bin"
}

# The directory the LIVE pogod was exec'd from, or empty if none is running.
# ps reports comm as the absolute path, which is what makes this answerable.
running_pogod_dir() {
    local comm
    comm="$(ps -eo comm= 2>/dev/null | grep -E '/pogod$' | head -1)"
    [ -n "$comm" ] && dirname "$comm"
}

# A rehearsal must not overwrite the binaries the fleet is running. Dry-run skips
# the bounce, but it still performs a real `go install`, and with a default GOBIN
# that install lands on top of the live pogod/pogo — a "rehearsal" that silently
# performs the riskiest half of the real thing. Docs telling the operator to set
# GOBIN are not a mechanism; this is.
assert_scratch_gobin() {
    local target="$1" live
    live="$(running_pogod_dir)"
    [ -n "$live" ] || return 0
    [ "$target" != "$live" ] && return 0
    if [ -n "${POGO_STAMPFIX_ALLOW_LIVE_GOBIN:-}" ]; then
        err "DRY RUN: install target IS the live $live, allowed by POGO_STAMPFIX_ALLOW_LIVE_GOBIN"
        return 0
    fi
    err "DRY RUN refusing: the install target is $target, which is where the RUNNING pogod lives."
    err "A rehearsal that rewrites the live binaries has performed the riskiest half of the real"
    err "run while reporting that it did nothing. Point GOBIN at a scratch directory:"
    err "    GOBIN=/tmp/stampfix-gobin POGO_STAMPFIX_DRY_RUN=1 $0"
    err "(or set POGO_STAMPFIX_ALLOW_LIVE_GOBIN=1 if overwriting them is genuinely intended)"
    return 1
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
main() {
    log "=== pogo-stampfix ($WORK_ITEM) starting: label=$LABEL src=$SRC ==="
    log "context: uid=$(id -u) user=$(id -un) ppid=$PPID pwd=$PWD"
    log "PATH=$PATH"

    # 1. One-shot guard. A repeat calendar fire is a no-op, not a second bounce.
    if [ -e "$SENTINEL" ]; then
        log "sentinel $SENTINEL exists — this job has already run:"
        sed 's/^/    /' "$SENTINEL" 2>/dev/null
        log "nothing to do (one-shot). Exiting 0."
        return 0
    fi

    # 2. Caller check, before anything is written.
    #
    # A rehearsal is exempt, and only a rehearsal. The check exists because a
    # caller inside pogod's tree cannot survive the bounce to complete the second
    # step — but dry-run performs no bounce, so there is no second step to be cut
    # off from, and the agent authoring this job is exactly who needs to rehearse
    # it. assert_scratch_gobin below is what keeps that exemption honest.
    if [ -n "$DRY_RUN" ]; then
        if ! assert_not_agent 2>/dev/null; then
            log "DRY RUN: caller is inside pogod's tree; permitted because no bounce will happen"
        fi
    else
        assert_not_agent || return 2
    fi

    # 3. Tools.
    resolve_go  || return 3
    resolve_git || return 3
    # A run with no alert path is a run whose outcome nobody learns; but the
    # deadlock is worth clearing even then, so this warns rather than refuses.
    resolve_mg  || err "continuing WITHOUT an alert path — the outcome will only be in this log"

    # 4. Source tree preconditions.
    [ -d "$SRC" ] || { err "deploy source $SRC does not exist"; return 4; }
    assert_real_git_dir || return 4

    local deploy_script="$SRC/scripts/pogo-self-deploy"
    [ -x "$deploy_script" ] || { err "$deploy_script is not executable"; return 4; }

    # A dirty tree would be silently baked into the binaries (as vcs.modified=true
    # over uncommitted code). Abort rather than reset — the tree is not ours.
    local dirty
    dirty="$("$GIT" -C "$SRC" status --porcelain=v1 2>/dev/null)"
    if [ -n "$dirty" ]; then
        err "$SRC has uncommitted changes — refusing to install a build of a dirty tree:"
        printf '%s\n' "$dirty" | sed 's/^/    /' >&2
        return 4
    fi

    local head
    head="$("$GIT" -C "$SRC" rev-parse HEAD 2>/dev/null)"
    log "source: $SRC at HEAD $head ($("$GIT" -C "$SRC" log --oneline -1 2>/dev/null))"

    local gobin; gobin="$(gobin_dir)"
    log "install target: $gobin"
    if [ -n "$DRY_RUN" ]; then
        assert_scratch_gobin "$gobin" || return 4
    fi

    # 5. Record the BEFORE state, so the log can be read on its own later.
    local before_pogod before_pogo
    before_pogod="$(installed_rev "$gobin/pogod")"
    before_pogo="$(installed_rev "$gobin/pogo")"
    log "before: installed pogod=${before_pogod:-<unstamped/missing>} pogo=${before_pogo:-<unstamped/missing>}"
    if rev_in_repo "$before_pogod"; then
        log "before: pogod's stamp is already native to $SRC (the stamp may already be fixed)"
    else
        log "before: pogod's stamp is FOREIGN to $SRC — this is what we are clearing"
    fi

    # 6. THE FIX. Run from $SRC, whose .git is a real directory (checked above).
    log "step 1/2: go install ./cmd/pogod ./cmd/pogo (from $SRC)"
    local install_log install_rc=0
    install_log="$(cd "$SRC" && "$GO" install ./cmd/pogod ./cmd/pogo 2>&1)" || install_rc=$?
    [ -n "$install_log" ] && printf '%s\n' "$install_log" | sed 's/^/    /'
    if [ "$install_rc" -ne 0 ]; then
        err "go install failed (exit $install_rc) — nothing was redeployed, the fleet is untouched"
        alert "pogo-stampfix FAILED: go install (exit $install_rc)" \
"The one-shot stamp fix ($WORK_ITEM) could not rebuild the binaries.

  source : $SRC at $head
  go     : $GO
  exit   : $install_rc

go install output:
$install_log

NOTHING WAS DEPLOYED. The running fleet is untouched and the foreign stamp is
still in place, so the nightly com.pogo.deploy will keep aborting at
classify_drift until this is resolved.

Log: ~/Library/Logs/pogo/pogo-stampfix.log"
        return 5
    fi
    log "go install completed"

    # 7. FAIL-CLOSED verification. Only bounce the fleet if the stamp is fixed —
    #    a bounce that leaves the deadlock in place costs the fleet for nothing.
    local after_pogod after_pogo
    after_pogod="$(installed_rev "$gobin/pogod")"
    after_pogo="$(installed_rev "$gobin/pogo")"
    log "after: installed pogod=${after_pogod:-<unstamped/missing>} pogo=${after_pogo:-<unstamped/missing>}"

    local bad=""
    if [ -z "$after_pogod" ]; then bad="pogod is unstamped or missing"
    elif ! rev_in_repo "$after_pogod"; then bad="pogod still claims $after_pogod, which $SRC does not have"
    fi
    if [ -z "$bad" ]; then
        if [ -z "$after_pogo" ]; then bad="pogo is unstamped or missing"
        elif ! rev_in_repo "$after_pogo"; then bad="pogo still claims $after_pogo, which $SRC does not have"
        fi
    fi
    if [ -n "$bad" ]; then
        err "stamp NOT cleared after go install: $bad"
        err "refusing to redeploy — a fleet-wide bounce would not fix this and would cost the fleet"
        alert "pogo-stampfix FAILED: stamp not cleared, fleet NOT bounced" \
"The one-shot stamp fix ($WORK_ITEM) rebuilt the binaries but the stamp is still wrong,
so it did NOT redeploy.

  problem: $bad
  source : $SRC at $head
  before : pogod=${before_pogod:-<none>} pogo=${before_pogo:-<none>}
  after  : pogod=${after_pogod:-<none>} pogo=${after_pogo:-<none>}

THE FLEET WAS NOT BOUNCED. Note that the binaries on disk HAVE been rewritten,
so if the running daemon is older than they are, new CLI subcommands may 404
against it (the mg-49bc mismatch) until a restart happens.

Rollback pair (the previously running 0.5.0 binaries):
  ~/.pogo/rollback-pogo-2026-07-30/{pogo,pogod}

Log: ~/Library/Logs/pogo/pogo-stampfix.log"
        return 6
    fi
    log "stamp verified native to $SRC for both binaries"
    if [ "$after_pogod" = "$head" ] && [ "$after_pogo" = "$head" ]; then
        log "both binaries match HEAD — redeploy should classify as RESTART owed"
    else
        log "note: installed revs differ from HEAD; redeploy will decide whether a build is owed"
    fi

    # 8. The rehearsal stops here — everything below this line either bounces the
    #    fleet or records that we are about to.
    if [ -n "$DRY_RUN" ]; then
        log "DRY RUN: stopping before the bounce. No sentinel, no mail, no plist removal."
        log "DRY RUN: would now run: $deploy_script redeploy --yes --skip-drain --repo $SRC"
        return 0
    fi

    # Sentinel BEFORE the redeploy — see the header. A second fire landing
    # mid-bounce must not start a second bounce.
    INSTALLED_OK=true
    mkdir -p "$(dirname "$SENTINEL")" 2>/dev/null
    {
        echo "work_item=$WORK_ITEM"
        echo "ran_at=$(ts)"
        echo "source=$SRC"
        echo "head=$head"
        echo "installed_pogod=$after_pogod"
        echo "installed_pogo=$after_pogo"
        echo "note=go install succeeded; redeploy attempted immediately after"
    } >"$SENTINEL" 2>/dev/null
    log "sentinel written: $SENTINEL"

    # 9. The redeploy. --skip-drain is REQUIRED, not a convenience: the running
    #    daemon predates /agents/drain, and pogo-self-deploy's drain precondition
    #    turns that answer into a hard exit 6 ("bootstrap: this pogod predates the
    #    /agents/drain endpoint ... re-run with --skip-drain"). Draining is also
    #    moot here — the kickstart bounces whatever is running either way.
    log "step 2/2: $deploy_script redeploy --yes --skip-drain --repo $SRC"
    log "(this bounces the ENTIRE fleet, including the coordinator; do_prove runs first and costs ~2min)"
    local rc=0
    "$deploy_script" redeploy --yes --skip-drain --repo "$SRC" 2>&1 | sed 's/^/    | /'
    rc="${PIPESTATUS[0]}"
    log "redeploy exited $rc"

    if [ "$rc" -ne 0 ]; then
        err "redeploy failed (exit $rc)"
        alert "pogo-stampfix: stamp CLEARED but redeploy failed (exit $rc)" \
"The one-shot stamp fix ($WORK_ITEM) cleared the foreign stamp, then the redeploy failed.

  installed: pogod=$after_pogod pogo=$after_pogo  (both native to $SRC)
  source   : $SRC at $head
  redeploy : exit $rc

WHAT THIS COSTS, AND WHAT IT DOES NOT
The binaries on disk are new; the running daemon may still be the old one, which
is the mg-49bc mismatch (new CLI subcommands 404 against an old daemon).

But the DEADLOCK IS BROKEN. classify_drift no longer aborts on FOREIGN STAMP, so
the 03:00 nightly com.pogo.deploy — which has been failing at exactly that gate
since 2026-07-29 — will now reclassify as 'RESTART owed' and finish the restart
on its own tonight. No further authoring is needed for that to happen.

Exit-code guide (scripts/pogo-self-deploy):
  6 = a drain/daemon precondition refused    7 = drain stalled
  9 = do_prove RED — the control suite failed against the new artifact; the OLD
      pogod is deliberately left running, which is the correct resting state.

Rollback pair: ~/.pogo/rollback-pogo-2026-07-30/{pogo,pogod}
Log: ~/Library/Logs/pogo/pogo-stampfix.log"
        return "$rc"
    fi

    # 10. Post-verification. The kickstart replaced pogod, so give the new one a
    #     moment to bind before asking it anything.
    log "redeploy reported success — verifying the replacement"
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
        sleep 3
        curl -sf --max-time 5 "http://127.0.0.1:${POGO_PORT:-10000}/version" >/dev/null 2>&1 && break
    done
    local ver_body
    ver_body="$(curl -sf --max-time 5 "http://127.0.0.1:${POGO_PORT:-10000}/version" 2>/dev/null)"
    log "GET /version -> ${ver_body:-<no answer>}"
    log "installed now: pogod=$(installed_rev "$gobin/pogod") pogo=$(installed_rev "$gobin/pogo")"
    log "--- pogo-self-deploy check ---"
    "$deploy_script" check --repo "$SRC" 2>&1 | sed 's/^/    | /'
    log "--- end check ---"

    alert "pogo-stampfix: foreign stamp cleared and fleet redeployed" \
"The one-shot stamp fix ($WORK_ITEM) completed.

  before : pogod=${before_pogod:-<none>} pogo=${before_pogo:-<none>}   (FOREIGN)
  after  : pogod=$after_pogod pogo=$after_pogo
  source : $SRC at $head
  /version: ${ver_body:-<no answer yet>}

The fleet was bounced, so every agent was restarted; pogod's own supervision
brings the coordinator back. The nightly com.pogo.deploy should now find either
'clean' or an ordinary build/restart drift instead of aborting on FOREIGN STAMP.

Worth confirming on the next cycle:
  pogo version                                   (off 0.5.0)
  go version -m ~/go/bin/pogod | grep vcs.revision
  $deploy_script check --repo $SRC   (no FOREIGN STAMP)

Log: ~/Library/Logs/pogo/pogo-stampfix.log"
    return 0
}

main "$@"
rc=$?
log "=== pogo-stampfix finished: exit $rc (installed=$INSTALLED_OK) ==="

# Self-removal, LAST, and in this order on purpose. The plist is deleted first so
# that removal is guaranteed to happen; the bootout is the final statement
# because launchd answers it by SIGTERMing this very process, and anything after
# it would be unreliable. Both are best-effort: a job that fixed the stamp but
# could not tidy itself up is still a job that fixed the stamp, and the sentinel
# already makes a repeat fire a no-op.
if [ -n "${POGO_STAMPFIX_NO_SELF_REMOVE:-}" ]; then
    log "POGO_STAMPFIX_NO_SELF_REMOVE set — leaving $LABEL loaded"
    exit "$rc"
fi
log "removing $PLIST and booting out $LABEL (one-shot cleanup)"
rm -f "$PLIST" 2>/dev/null || err "could not remove $PLIST — remove it by hand"
launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null
exit "$rc"
