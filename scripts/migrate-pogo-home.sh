#!/usr/bin/env bash
# scripts/migrate-pogo-home.sh
#
# Idempotent migration from the legacy POGO_HOME=$HOME convention to the
# canonical POGO_HOME=$HOME/.pogo layout (mg-ff8b).
#
# Background. mg-55d1 installed the recovery agent with POGO_HOME=$HOME
# (matching the running pogod plist at the time), which scattered pogo
# state across $HOME root: ~/recovery/{queue,processed,failed}/,
# ~/projects.json, ~/bin/pogo-recovery.sh. The PM call (2026-05-02) was
# to migrate to ~/.pogo/. The bulk of pogo state — agents/, polecats/,
# refinery/, schedules.json, events.log — already lives at ~/.pogo/
# regardless of POGO_HOME (those paths are hardcoded in code), so this
# migration only has to relocate the three POGO_HOME-driven artifacts
# and reinstall the launchd plists with the new env.
#
# Safe to re-run. Each step checks current state and skips when the
# target already matches.
#
# Usage:
#   ./scripts/migrate-pogo-home.sh            # do the migration
#   ./scripts/migrate-pogo-home.sh --dry-run  # preview without changes
#
# Requires: pogo (with `service install` and `service install-recovery`).

set -euo pipefail

DRY_RUN=false
for arg in "$@"; do
  case "$arg" in
    --dry-run|-n) DRY_RUN=true ;;
    -h|--help)
      sed -n '2,/^$/p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
  esac
done

NEW_POGO_HOME="${HOME}/.pogo"

run() {
  if [ "$DRY_RUN" = true ]; then
    echo "  [dry-run] $*"
  else
    "$@"
  fi
}

echo "Migrating POGO_HOME → ${NEW_POGO_HOME}"
echo "  HOME=${HOME}"
echo "  current POGO_HOME env=${POGO_HOME:-(unset)}"
echo ""

if [ "$DRY_RUN" = true ]; then
  echo "DRY RUN — no changes will be made."
  echo ""
fi

run mkdir -p "${NEW_POGO_HOME}"

###############################################################################
# 1. Recovery state (queue/, processed/, failed/, last_restart)
#
# This is the only piece of in-flight state we move. Refinery, agent state,
# and schedules.json already live at ~/.pogo/ — they're not touched.
###############################################################################
LEGACY_RECOVERY="${HOME}/recovery"
NEW_RECOVERY="${NEW_POGO_HOME}/recovery"

if [ -d "${LEGACY_RECOVERY}" ]; then
  if [ -d "${NEW_RECOVERY}" ]; then
    echo "Both ${LEGACY_RECOVERY} and ${NEW_RECOVERY} exist — merging legacy into canonical (newer wins per file)."
    # rsync -u: skip files that are newer on the receiver. Preserves any
    # in-flight .req files queued at the legacy path while keeping the
    # canonical processed/failed history intact.
    if command -v rsync >/dev/null 2>&1; then
      run rsync -a -u "${LEGACY_RECOVERY}/" "${NEW_RECOVERY}/"
    else
      # rsync isn't on macOS by default in older releases, but it ships
      # with the developer tools. Fallback to cp -n (no clobber) which
      # preserves whatever is at the canonical location.
      run cp -Rn "${LEGACY_RECOVERY}/." "${NEW_RECOVERY}/"
    fi
    run rm -rf "${LEGACY_RECOVERY}"
  else
    echo "Moving ${LEGACY_RECOVERY} → ${NEW_RECOVERY}"
    run mv "${LEGACY_RECOVERY}" "${NEW_RECOVERY}"
  fi
else
  echo "  ${LEGACY_RECOVERY} not present — nothing to move for recovery state."
fi

###############################################################################
# 2. projects.json
###############################################################################
LEGACY_PROJECTS="${HOME}/projects.json"
NEW_PROJECTS="${NEW_POGO_HOME}/projects.json"

if [ -f "${LEGACY_PROJECTS}" ]; then
  if [ -f "${NEW_PROJECTS}" ]; then
    # Both exist. Pogod under canonical POGO_HOME will use the canonical
    # one going forward, so just remove the legacy file.
    echo "Both ${LEGACY_PROJECTS} and ${NEW_PROJECTS} exist — keeping canonical, removing legacy."
    run rm -f "${LEGACY_PROJECTS}"
  else
    echo "Moving ${LEGACY_PROJECTS} → ${NEW_PROJECTS}"
    run mv "${LEGACY_PROJECTS}" "${NEW_PROJECTS}"
  fi
else
  echo "  ${LEGACY_PROJECTS} not present — nothing to move for projects.json."
fi

###############################################################################
# 3. ~/bin/pogo-recovery.sh
#
# `pogo service install-recovery` re-copies the script to
# $POGO_HOME/bin/pogo-recovery.sh, so the legacy copy is just dead weight.
# Don't try to remove $HOME/bin itself — users may keep their own things there.
###############################################################################
LEGACY_RECOVERY_SCRIPT="${HOME}/bin/pogo-recovery.sh"

if [ -f "${LEGACY_RECOVERY_SCRIPT}" ]; then
  echo "Removing legacy ${LEGACY_RECOVERY_SCRIPT} (will be re-installed under ${NEW_POGO_HOME}/bin/)"
  run rm -f "${LEGACY_RECOVERY_SCRIPT}"
  # Try to remove $HOME/bin only if it's empty — never force.
  if [ -d "${HOME}/bin" ] && [ -z "$(ls -A "${HOME}/bin" 2>/dev/null)" ]; then
    echo "  ${HOME}/bin is empty — removing."
    run rmdir "${HOME}/bin" 2>/dev/null || true
  fi
else
  echo "  ${LEGACY_RECOVERY_SCRIPT} not present — nothing to clean."
fi

###############################################################################
# 4. Reinstall pogod plist with the new POGO_HOME env.
#
# pogo service install is idempotent (canSkipInstall): if the rendered
# plist matches what's on disk and pogod is healthy, it no-ops. So this
# step only does work the first time it sees the new POGO_HOME.
###############################################################################
echo ""
echo "Reinstalling launchd plists with POGO_HOME=${NEW_POGO_HOME}..."

if command -v pogo >/dev/null 2>&1; then
  if [ "$DRY_RUN" = true ]; then
    echo "  [dry-run] POGO_HOME=${NEW_POGO_HOME} pogo service install"
    echo "  [dry-run] POGO_HOME=${NEW_POGO_HOME} pogo service install-recovery"
  else
    POGO_HOME="${NEW_POGO_HOME}" pogo service install
    POGO_HOME="${NEW_POGO_HOME}" pogo service install-recovery
  fi
else
  echo "  pogo not on PATH — skipping plist reinstall. Run manually:" >&2
  echo "    POGO_HOME=${NEW_POGO_HOME} pogo service install" >&2
  echo "    POGO_HOME=${NEW_POGO_HOME} pogo service install-recovery" >&2
fi

echo ""
echo "Done. Verify with:"
echo "  launchctl print gui/\$(id -u)/com.pogo.daemon | grep POGO_HOME"
echo "  ls ${NEW_POGO_HOME}/recovery/queue ${NEW_POGO_HOME}/bin"
echo ""
echo "If your shell rc still sets POGO_HOME=\$HOME, update it to \$HOME/.pogo"
echo "(see docs/{zsh,bash,fish}.md)."
