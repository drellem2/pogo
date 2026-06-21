# Architect design note (mg-d826) — 2026-05-21

**Recommendation: SWITCH** — replace event-based file-watching with a
timer-driven incremental re-index. Daniel's explicit go/no-go is still
required; this note + the pm-pogo digest carry it to him. Implementation
follow-up **mg-5b0d** is filed and SHELVED, held until Daniel confirms.

Re-confirmed against origin/main `80602eb` on 2026-05-21: `internal/watch`,
`internal/search`, `internal/project` are all unchanged since the
investigation, so the code-volume analysis holds. (An earlier draft ran
against a stale local checkout — see the corrected breakage section below.)

This design note is the deliverable for acceptance bar #1 — mg-5b0d's first
commit lands it verbatim as `docs/design/indexing-strategy.md`.

## The question

pogo's search indexer is kept fresh by an event-based filesystem watcher.
Daniel's reminder: is event-watching worth its complexity, or would periodic
re-indexing be simpler and good enough?

## Three options

**A — Event-watching (status quo).** An OS watch backend (FSEvents on darwin,
fsnotify/kqueue elsewhere) fires on every file change; the indexer reacts
within ~250ms.

**B — Periodic full re-index.** A timer re-walks and re-parses every project
on an interval. Simple, but pays the full O(total bytes) cost every tick.

**C — Periodic incremental re-index (RECOMMENDED).** A timer iterates
registered projects on an interval and calls the *existing* incremental
re-index path: `indexRec` skips unchanged files on an mtime match,
`serializeProjectIndex` skips the zoekt rebuild on unchanged hashes, `Load`
short-circuits via a stored `GitTreeHash`. A no-change tick costs one `Lstat`
per file. The incremental machinery already exists and is tested — option C
is a TRIGGER change, not an indexer rewrite.

## Comparison

| Axis | A: Event-watching | B: Periodic full | C: Periodic incremental |
|---|---|---|---|
| New code | — (status quo) | ~60 LOC timer+config | ~60 LOC timer+config |
| Deletable code | — | ~1,100-1,200 LOC | ~1,100-1,200 LOC |
| cgo / platform split | YES (FSEvents+kqueue) | none | none |
| fd cost | high — the whole bug arc | none | none |
| Steady-state CPU | ~0 idle | full re-parse every tick | one Lstat/file/tick |
| Change latency | ~250ms | one interval | one interval |
| Portability | per-OS backends, breaks `CGO_ENABLED=0` | OS-agnostic, pure Go | OS-agnostic, pure Go |
| Maintenance record | cap → excludes → cap rework → FSEvents → cgo → CI break | — | — |

## Findings

**Event-watching is the dominant source of accidental complexity here.**
`internal/watch` is 553 LOC across a darwin FSEvents backend and a
non-darwin fsnotify backend, selected at compile time by build tags. It drags
in two dependencies (`fsnotify/fsevents`, `fsnotify/fsnotify`), the first via
cgo (`-framework CoreServices`). Around it sits a watch-driven `Scanner` (241
LOC) for sibling-repo auto-discovery, watch glue in the search package, a
`ReIndexFile` single-file handler, and a `maxWatchers` fd cap that — by
mg-d205's own analysis — never bounded the right quantity (kqueue spent one
fd per *file*, the cap counted *directories*). The history is a multi-fix
arc: file-watcher cap → auto-ignore node_modules → cap rework → the mg-d205
FSEvents rewrite → the cgo dependency → the mg-6222 CI breakage. Every fix
addressed a symptom of the watch architecture itself.

**No consumer needs sub-second freshness.** The index is read by exactly
three request-driven paths: the `/projects/{id}` and `/status` HTTP
endpoints, and the `pose search` CLI. All are human- or poll-triggered. There
is no code path requiring immediate freshness, and the codebase *already*
embraces staleness — `Status` has a `StatusStale` value and `Load` re-indexes
lazily on a `GitTreeHash` mismatch. Minutes-stale is plainly acceptable.

**The incremental path already exists.** mg-779c added mtime-based change
detection; mg-639a added git-tree-hash invalidation. A periodic loop would
simply call code that is already written and tested. Net new code for the
incremental behaviour: ~0.

**The switch deletes ~1,100-1,200 LOC, two dependencies, and all cgo**, and
costs only ~60 LOC (a `time.Ticker` loop + an `index_interval` config key).
Deletable: both watch backends, the watch API, the `Scanner`, the watch glue,
`ReIndexFile`, the `maxWatchers` cap, `watcher_fd_test.go`, both fsnotify
deps. Removing cgo restores `CGO_ENABLED=0` portability across all platforms.

**KEEP** the `internal/project` registry + `PruneRegistry` GC and the
`MaxFilesPerTree` / `index_roots` / `.pogoignore` scope controls. These bound
index *scope and cost* and are orthogonal to the *trigger* mechanism — they
are wanted under periodic indexing too. So mg-d205's cleanup is partial: its
FSEvents backend and fd cap go; its scope-control half stays.

## What is lost, and the mitigations

1. **Change latency** — an edit appears at the next tick instead of within
   ~250ms. Acceptable: every consumer is request-driven and staleness is
   already a first-class state. Tune via `index_interval` (default 2m).
2. **Live sibling-repo auto-discovery** — the `Scanner` watched parent
   directories to notice new sibling repos instantly. Recovered cheaply by
   scanning `index_roots` once per tick.

## One live breakage found during this investigation

CORRECTION (2026-05-21): an earlier draft of this note claimed two
breakages. The first — "CI red / mg-6222 never merged" — was a
STALE-CHECKOUT ERROR: the investigation initially ran against a local main 8
commits behind origin. mg-6222 IS merged (commit `3ece175`
"ci: build darwin matrix cells on macos-latest with cgo" on origin/main);
`ci.yml` carries the fix. That claim is retracted. One real breakage remains:

**Darwin release builds are broken.** `.goreleaser.yml` builds `pogo` /
`pogod` / `lsp` / `pose` with `CGO_ENABLED=0` for `goos: [linux, darwin]`.
`pogod` / `lsp` / `pose` pull `internal/search → internal/watch → fsevents`
(cgo) and fail to compile for darwin under `CGO_ENABLED=0`. mg-6222 fixed
`ci.yml` only — it never touched `.goreleaser.yml`. Latent (bites only on a
release/tag cut, not day-to-day main CI), real, and not previously tracked.

The recommended switch removes the cgo dep and fixes this for free, with no
`.goreleaser.yml` change. It also lets `ci.yml`'s darwin cells revert from
`macos-latest` back to a pure-Go linux cross-compile — a simplification, not
a fix. If Daniel says no-go on the switch, a standalone `.goreleaser.yml`
fix must be filed.

## Follow-up

- **mg-5b0d** — "Switch pogo indexer from event-based file-watching to
  timer-driven incremental re-indexing." Filed, SHELVED, held pending
  Daniel's go-ahead. Carries the full delete/add checklist, commit sequence,
  acceptance bars, and the CI/release scope. The mg-d205 cleanup is scoped
  into it (the watch half; the scope-control half is retained by design).
- On Daniel's go-ahead: architect unshelves mg-5b0d, tags it dispatchable,
  and notifies mayor.
