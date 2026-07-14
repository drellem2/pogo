# Investigations

Point-in-time investigation, validation, and calibration reports. Each captures
what was found at a specific date for a specific work item; they are records, not
living documentation. Newer code may have moved on — the report's date and work
item are the anchor.

| Doc | Covers | Outcome |
|-----|--------|---------|
| [attach-detach-2026-07-06.md](attach-detach-2026-07-06.md) | Root cause: `pogo attach` Ctrl-\ detach never worked (raw mode clears ISIG, no byte scan) | Root-cause trace + fix (mg-5be3) |
| [bridget-fork-2026-05-09.md](bridget-fork-2026-05-09.md) | Forking `cloverross/bridget` for the Discord integration | Done — fork follow-up to the [bridget design](../design/bridget-integration-design.md) (mg-7921) |
| [claude-explore-integration.md](claude-explore-integration.md) | Whether pogo's index needs special config for Claude Code's "Explore" sub-agent | Scoped, deferred (mg-39b6) |
| [codex-e2e-validation.md](codex-e2e-validation.md) | Phase 3D end-to-end validation of the Codex CLI provider | Passed — validation record (mg-6599) |
| [codex-nudge-calibration.md](codex-nudge-calibration.md) | Empirical nudge timing for the Codex provider | Calibration record backing `internal/codex/provider.go` (mg-7f76) |
| [cursor-nudge-calibration.md](cursor-nudge-calibration.md) | Empirical nudge timing + the `.cursor/rules` persona-injection escape hatch for the Cursor provider | Calibration record backing `internal/cursor/provider.go` (mg-c146) |
| [investigation-mg-06f2.md](investigation-mg-06f2.md) | Root cause: tickets archived "done" before the refinery confirmed the merge | Root-cause trace (mg-06f2) |
| [launch-readiness-audit-2026-03-21.md](launch-readiness-audit-2026-03-21.md) | v0.2 launch-readiness audit across install, agents, refinery, release | Point-in-time audit, 2026-03-21 — no hard blockers |
| [nudge-claude-code-workaround.md](nudge-claude-code-workaround.md) | Workarounds for nudging Claude Code through mid-session modals | Investigation; the modal watcher it scopes since shipped (mg-4421, `internal/claude/modal_hook.go`) |
| [pi-nudge-calibration.md](pi-nudge-calibration.md) | Empirical nudge timing + persona/trust integration for the pi provider | Calibration record backing `internal/pi/provider.go` (mg-9829) |
| [pty-investigation-2026-05-09.md](pty-investigation-2026-05-09.md) | PTY rendering glitches on `pogo agent attach` | Read-only investigation; fix carried by a follow-up ticket (mg-098c) |
| [rating-dialog-match-2026-07-13.md](rating-dialog-match-2026-07-13.md) | Root cause: rating-dialog marker never matched the real TUI footer (column-move escapes collapse spaces under `StripANSI`) | Root-cause trace + fix — whitespace-insensitive matching (mg-f36b) |
| [renudge-efficacy-2026-07-14.md](renudge-efficacy-2026-07-14.md) | Efficacy of the bare-CR auto-renudge against a real Claude Code paste-buffered kickoff wedge; head-to-head vs. the field-confirmed `"1"`+CR | Verified — bare CR recovers 8/8, `"1"`+CR 5/5; bare CR preferred (no stray char), no `"1"` fallback needed (mg-feb3) |
