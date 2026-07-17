# Changelog fragments (`changelog.d/`)

**Add your changelog entry as a NEW FILE here — do not append to `CHANGELOG.md`.**

Every change that appended to the shared `## [Unreleased]` tail of
`CHANGELOG.md` touched the same lines, so any two concurrent branches collided
there when the refinery rebased onto `main`. That single file was the dominant
recorded merge-conflict cause (mg-d917). Writing one file per change makes the
collision **structurally impossible** — two authors never touch the same path —
rather than merely rarer. Prior art: towncrier, changesets, reno.

## How to add an entry

Create `changelog.d/<slug>.<category>.md` — one file per change, named by your
work-item id:

```bash
cat > changelog.d/mg-1234.fixed.md <<'EOF'
- **Short headline (mg-1234).** Longer prose explaining the change, why it
  matters, and any user-visible effect. Multi-line bullets are fine.
EOF
git add changelog.d/mg-1234.fixed.md
```

- **`<slug>`** — your work-item id (`mg-1234`). Any unique string works; the id
  keeps two branches from ever choosing the same filename.
- **`<category>`** — the last dot-segment, one of: `added`, `changed`,
  `deprecated`, `removed`, `fixed`, `security`, `documentation` (`docs`/`doc`
  alias `documentation`). Omit it and the entry files under **Changed** with a
  note on stderr.
- **Body** — Keep-a-Changelog markdown bullets, exactly as they should appear
  under the section header. Start each top-level entry with `- `.

## What happens at release

`scripts/bump-version.sh` runs `scripts/assemble-changelog.sh`, which folds every
fragment into the `## [Unreleased]` section of `CHANGELOG.md` (grouped by
category, canonical order), deletes the consumed fragments, and then rolls
`[Unreleased]` to the new version. If assembly would produce an **empty**
`[Unreleased]`, it exits non-zero and the release aborts — an empty
`changelog.d/` never silently ships an empty changelog.

This `README.md` is documentation, not a fragment; the assembler always skips it.
