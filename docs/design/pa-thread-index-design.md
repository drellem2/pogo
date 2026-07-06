# pa Thread Index — Local-Only Pointer Repo

Status: Shipped (mg-da41; decision mg-9a32, option b — pm-pogo + architect concurrence)
Author: polecat mg-da41
Date: 2026-07-06
Context: Daniel suggestion 2026-07-05 ("does pa use a private git repo or
something to keep track of the ongoing threads?"). This doc records the
mechanism; the repo itself is machine-local state, not part of any code repo.

## Problem

pa (the personal-assistant crew agent) persists its in-flight thread state
across restarts via a deliberately-unread self-mail in its maildir. That works,
and it exists precisely because pa's standing rules keep email content OUT of
files — payloads can contain things like bank account/routing details that must
never land in a committed file (git history makes deletion hard; worse than
plain files for secrets). But a single opaque state mail is not versioned,
not diffable, and not greppable.

## Decision (mg-9a32, option b)

Split state into two stores with different sensitivity levels:

- **Payload store (unchanged):** the deliberately-unread self-mail in pa's
  maildir. All sensitive content stays here and only here.
- **Thread index (new):** a **local-only** git repo at
  `~/.pogo/agents/pa/thread-index/` holding one markdown file per thread with
  *pointers only*. This is structural redaction: nothing sensitive exists in
  the repo to commit, so history-inspection is sufficient to verify cleanliness.

Options (a) redaction-discipline-only single repo and (c) encrypted store were
rejected: (a) relies on per-commit discipline over sensitive material, (c)
sacrifices greppability and adds key management.

## Index format

One file per thread at `threads/<thread-id>.md` (kebab-case slug), plain
`- key: value` lines so grep is the query interface:

- `subject:` one line (generalized if the literal subject would leak detail)
- `status:` active | waiting | done
- `last-touched:` YYYY-MM-DD
- `next-action:` one line, or "none"
- `payload-mail:` mail-id of the current STATE self-mail (`<agent>/<msg-id>`)
- optional repeatable pointers: `mail:` (other mail ids), `hey-thread:`
  (HEY topic ids), `mg:` (work-item ids)

Forbidden content, always: mail bodies, quoted email text, account/routing
numbers, card digits, phone numbers, personal addresses, any other PII.

## Invariants

1. **No remote, ever.** The repo must never get a remote — no `git remote add`,
   no push — without Daniel's explicit sign-off. Stated in the repo README and
   as a hard rule in pa's prompt.
2. **Commit per thread-state change.** pa commits on every open/update/close
   with a one-line message (e.g. `close nvb-closure: wire confirmed`).
3. **Pointers only.** See forbidden-content list above.

## Rollout (done under mg-da41)

- Repo created with fresh history; README states the no-remote rule and format.
- Seeded from pa's then-current STATE self-mail: 12 threads as pointer entries,
  no bodies copied.
- pa's prompt (machine-local at `~/.pogo/agents/crew/pa.md`, no repo source
  since the personal-assistant example moved out of this repo) gained a
  "Thread index" section, tool examples, and carve-outs to its
  never-commit-files rules scoped to this one repo.
