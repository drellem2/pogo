# Bridget fork — addendum

**Status:** done · **Owner:** polecat pc4ef (mg-c4ef) · **Tracks:** mg-7921 follow-up
**Date:** 2026-05-09

## Context

Architect ruling on mg-7921 was to fork `cloverross/bridget` rather than push upstream
(constraint: read-only on the upstream — other bridget operators may migrate to our
fork). Three downstream tickets (mg-e166, mg-5845, mg-f1be) need a public fork in
place before they can land work. This polecat (mg-c4ef) stands the fork up and wires
it into pm-pogo's repos list. No bridget code changes — that's later.

The bulk of this work happens outside the pogo repo (in `~/.pogo/agents/pm/pogo.toml`
and on GitHub). Per the polecat protocol, this addendum exists so the refinery has a
real diff to merge and the change is recorded in pogo's git history.

## What was done

### 1. Fork created

- **Fork URL:** https://github.com/drellem2/bridget
- **Upstream:** https://github.com/CloverRoss/bridget
- **Visibility:** PUBLIC (verified via `gh repo view drellem2/bridget --json visibility`)
- **Default branch:** `main`
- **Pristine:** `origin/main` and `upstream/main` point at the same commit
  (`2d4cc9d`); `git rev-list --count upstream/main..origin/main` returned `0`.

### 2. Local clone

- **Path:** `/Users/daniel/dev/bridget`
- **Remotes:**
  - `origin` → `https://github.com/drellem2/bridget.git` (fetch+push)
  - `upstream` → `https://github.com/CloverRoss/bridget.git` (fetch only;
    push URL deliberately set to `DISABLE_PUSH` to enforce the read-only
    constraint defensively)

### 3. pm-pogo repos config updated

`/Users/daniel/.pogo/agents/pm/pogo.toml` — appended `"bridget"` to the `repos`
field. Next pm-pogo sweep will include the bridget fork in its scope so commit
activity, polecat transcripts, and merge history under `~/dev/bridget` get
surfaced through the normal pm-pogo digest path.

The `tags_any` field was left alone — bridget tickets so far also carry the
`pogo` tag, so they're already in scope through tag matching. If future bridget
work is filed under just the `bridget` tag, pm-pogo can extend `tags_any` itself.

## Why this addendum exists

The polecat refinery wants a diff against the pogo repo. The fork action is a
GitHub-side operation; the config edit is in `~/.pogo/agents/pm/`, outside any
pogo checkout. This file is the in-repo audit trail so future readers can find
what happened from `git log` alone.
