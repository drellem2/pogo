# Bridget integration design

**Status:** proposal · **Owner:** architect · **Tracks:** mg-7921
**Date:** 2026-05-09

## Context

Daniel directive 2026-05-09 (via reminders → pm-pogo): *"Discord is broken on this machine but I am reinstalling it. Repo we need to investigate: https://github.com/cloverross/bridget. It is a discord integration similar to the reminders we have now. Have architect do analysis on the config to make sure it can be configured to match my workflows, and to flag anything that is not generic. … decide if it needs to be forked or a PR created. … I also have a vision that it is similar to the way people use the discord integration for open claw, where different agents can be talked to in different channels."*

Bridget is a one-file Python service (`cloverross/bridget`, GPL-3.0, v1.0.0 dated 2026-05-09) that acts as a Pogo↔Discord bridge: it tails the human maildir and DMs a single Discord user, and listens to DMs back from that same user, routing recognised verbs through `mg`. It is the same shape as `pogo-reminders` (an Apple-Reminders bridge) but with Discord as the front end. The repo is shared with at least one other operator (`cloverross`, the author), so the analysis is read-only — no pushes, fork-vs-PR decision belongs to Daniel.

This is design-only. No production code lands in this ticket.

## Diagnosis

### What bridget already exposes as configuration

`~/.pogo/bridget.env` is read at startup; process env wins so launchd/systemd units can override. The env keys, all string-typed:

- **Required.** `DISCORD_BOT_TOKEN`, `DISCORD_USER_ID`, `DISCORD_SERVER_ID`.
- **Path overrides.** `MG_BIN`, `POGO_BIN`, `POGO_MAIL_DIR` (default `~/.macguffin/mail/human`), `POGO_DESIGNS_DIR`, `POGO_INBOX_REPO`, `BRIDGET_REPO_DIR`.
- **Behavioural.** `POGO_MAIL_RECIPIENT` (default `mayor`) — only consulted by the generic `mail <subject>` verb.

That covers the cross-machine portability story: paths, binary lookup, mail layout. For Daniel's setup specifically, the load-bearing keys are the three Discord IDs plus `POGO_DESIGNS_DIR` and `POGO_INBOX_REPO`; the rest can stay defaulted.

### What bridget hard-codes (and what that means for Daniel's workflows)

The hard-coding clusters into five buckets, ordered by how likely they are to bite a Daniel-shaped install.

1. **All workflow verbs route to `architect`.** Five DM commands (`approve`, `reject`, `revise`, `explain`, `next mg-XXXX`) call `mg mail send architect`; three filing commands (`idea:`, `bug:`, `next`) pass `--assignee=architect` to `mg new`. There is no env knob. `POGO_MAIL_RECIPIENT` only governs the catch-all `mail <subject>` verb. Today this happens to match the existing pogo convention (architect owns design ideas), but it pins Daniel into that one shape — he cannot DM the bot to send `revise` to a specific PM, route `idea:` to mayor, or scope a bug to a polecat without dropping back to the generic `mail` verb.

2. **The `pogo-inbox` tag is hard-coded** on every `idea:`, `bug:`, and `next` invocation (`bridget` lines 788, 968, 998). Daniel's setup uses tags like `daniel-creator`, `daniel-reminder`, and per-PM scope tags; bridget can only stamp `pogo-inbox`. Extra tags can be added inline (`idea: [scope] body`) but the base tag is fixed.

3. **Single user, DM-only — no channel awareness.** `on_message` (lines 1058–1072) ignores anything that isn't a `discord.DMChannel` from the configured `DISCORD_USER_ID`. The bot reads `intents.dm_messages` and `intents.message_content` only; guild channel listening is structurally absent. This is the gap that blocks Daniel's per-channel-routing vision, not a config knob.

4. **Notification fan-out is a single firehose.** `watch_mailbox`, `watch_task_transitions`, `watch_idea_claims` all `await user.send(msg)` to the same Discord user. There is no per-class routing, no quiet-hours filtering on outbound (only the shared `quiet.json` *signal* — agents read it, but bridget itself never consults it before DMing). Format strings (`🚀 claimed`, `✅ done`, `📦 shelved`, `🧠 architect claimed`) are inline literals.

5. **A few smaller shape assumptions.** Polling interval `POLL_INTERVAL = 5` seconds is a module constant. The "non-polecat assignees" set `{'architect', 'mayor', 'human', ''}` (line 283) is hard-coded, so a new crew agent name silently breaks the `claimed by …` formatter into `by polecat-foo`. The approval-mail prefix `Subject: approval needed ` (line 602) is a literal substring match — change pogo's approval-subject convention and the `status` view goes blind. `restart` runs `bash build.sh` from the bridget checkout root; non-bash hosts or a renamed script break it.

### What is *not* a problem

Worth naming so we don't fix what isn't broken:

- The maildir layout assumption (`new/` and `cur/`) is the canonical pogo shape and matches what `pogo-reminders` consumes. Diverging would create a new gap, not close one.
- The launchd/systemd template instructions in the README are platform-aware and accurate.
- `quiet.json` as a *shared* signal between bridget and crew agents is the right shape for a shared resource — the bug is that bridget itself doesn't honour the signal on outbound, not that the file format is wrong.
- The `restart` self-update via `git pull --ff-only` + `bash build.sh` syntax-check + `os._exit(0)` is the right minimal upgrade flow; it is rough but it does its job.

## Proposal

Two work-streams, separable: **(P1) generic config-flexibility** that closes the hard-coded gaps from buckets 1, 2, 4, 5 above, and **(P2) per-channel agent routing** that closes bucket 3 and unlocks Daniel's vision. P1 is small, mechanical, and uncontroversial; P2 is real new design and should land second. Both compose with the existing env-file convention so users who don't want them can ignore them.

### P1 — Generic config-flexibility (the easy half)

Add a small number of new env keys, all optional with sensible defaults that preserve today's behaviour for existing users. The shape mirrors the `POGO_MAIL_RECIPIENT` precedent.

- **`POGO_WORKFLOW_AGENT`** (default: `architect`) — replaces the hard-coded `architect` recipient/assignee in the five workflow verbs and three filing verbs. Daniel can set this to a per-PM agent if his approval flow ever moves; the default keeps the architect-as-design-coordinator model that already works.
- **`POGO_INBOX_TAG`** (default: `pogo-inbox`) — the base tag stamped on `idea:`, `bug:`, and `next`-filed items. User-supplied `[tag]` extras keep stacking on top, same as today.
- **`BRIDGET_POLL_INTERVAL`** (default: `5`) — purely a tuning knob; let operators slow it down on flaky networks or speed it up on dev. No design weight, but it costs ~3 lines and prevents the next "I want it slower" patch from being hard-coded.
- **`BRIDGET_QUIET_RESPECTS_OUTBOUND`** (default: `false` for back-compat) — when true, `watch_mailbox` / `watch_task_transitions` / `watch_idea_claims` consult `quiet.json` and suppress notifications inside the configured window. Inbound DMs are still processed (a user typing during quiet hours is consenting). False by default because changing existing operators' behaviour silently is hostile; the README should explain why a Daniel-shaped install probably wants `true`.

The "non-polecat assignees" set should not become an env list — that's a code cleanup, not a config one. Replace the literal set with a pattern: any assignee equal to `architect`/`mayor`/`human`/empty *or* matching `^pm-` is treated as crew; everything else is a polecat. Costs nothing and absorbs the next pm-* / crew-* name without an edit.

The approval-prefix subject scan (`Subject: approval needed `) should be a small `re.compile` against a configurable pattern (`BRIDGET_APPROVAL_RE`, default `^Subject: approval needed `). Same shape, same default behaviour, room for future approval verbs.

None of P1 is interesting on its own. The point is that after P1 lands, every Daniel-vs-cloverross divergence about *who handles what* is an env-file edit, not a fork.

### P2 — Per-channel agent routing (the open-claw shape)

The vision is one Discord channel per agent: `#mayor` reaches mayor, `#architect` reaches architect, `#pm-pogo` reaches pm-pogo, etc. Messages typed in a channel arrive in that agent's mailbox; notifications relevant to that agent fan out to that channel. DMs continue to work as today for the human-mail firehose.

The shape that fits pogo's existing primitives:

```
~/.pogo/bridget.channels.toml         # user-owned, install-untouched
```
```toml
# Default: messages typed in the mapped channel are mg-mailed to the agent;
# the agent's own activity (assigned mg items, mailbox posts to it) fans out
# to the same channel. Both directions, or one, per entry.

[[channel]]
id = "1234567890123456789"   # Discord channel snowflake
agent = "mayor"
inbound = true               # channel → mg mail send mayor
outbound = ["mail"]          # mayor's mailbox → channel
                             # ("mail" | "task-transitions" | "idea-claims")

[[channel]]
id = "9876543210987654321"
agent = "pm-pogo"
inbound = true
outbound = ["mail", "task-transitions"]

[[channel]]
id = "5555555555555555555"
agent = "architect"
inbound = true
outbound = ["mail", "idea-claims"]
```

The change to bridget is bounded:

- **Discord intents.** Add `intents.guilds = True` and `intents.guild_messages = True`. Today only DM intents are on.
- **`on_message` widens.** Currently rejects anything not a `DMChannel`; new branch matches `discord.TextChannel` whose `.id` is in the loaded channel map. Author check still pins to `DISCORD_USER_ID` so the bot only responds to Daniel.
- **Inbound routing.** A channel-routed message is conceptually `mail <subject>` to the channel's mapped agent. The verb parsing reuses today's `handle_command`, but the `architect` literal in the workflow verbs is replaced by either the channel-mapped agent (if the verb is workflow-shaped) or `POGO_WORKFLOW_AGENT` (if the message arrives in a DM, today's behaviour). Plain text in a mapped channel becomes `mg mail send <agent> --from=human --subject=<first-line> --body=<rest>`.
- **Outbound fan-out.** Today's three watchers (`watch_mailbox`, `watch_task_transitions`, `watch_idea_claims`) gain a per-event routing decision: for each subscribed channel, does this event match? `watch_mailbox` becomes per-agent — bridget already polls only `human/new/`; for `outbound = ["mail"]` channels it must additionally poll `<agent>/new/` and DM into that channel. (One new poller per *agent* with at least one subscriber, not one per channel — agents with multiple channel subscribers fan out from the single poller.) `task-transitions` and `idea-claims` already enumerate per-item; filtering them by the channel's agent is a one-liner per loop.
- **DMs stay.** The `human` firehose continues to DM the configured user — no breaking change to today's setup. A user who never writes `bridget.channels.toml` sees identical behaviour.

The mapping file is TOML and lives in `~/.pogo/`, not `~/.pogo/bridget/`, so it is naturally drop-in compatible with the same conventions as the rest of pogo's user-side state. It is *user-owned* — `install.sh` should never touch it — but the example file (`bridget.channels.toml.example`) ships in the repo with a single commented-out entry to copy-paste from.

### What this gives Daniel

- One channel per agent he wants to talk to. Type "what's the queue?" in `#mayor`, mayor sees the mail, replies via its own response loop into the same channel.
- Architect's claims fan out to `#architect`, not lost in the human DM stream.
- pm-pogo's twice-daily digest arrives in `#pm-pogo` instead of competing with task-transition pings.
- DMs remain the catch-all for human-mail and ad-hoc commands.

### What this design deliberately does *not* do

- **No Discord-side ACL.** The bot still only listens to `DISCORD_USER_ID`. A second collaborator typing in a mapped channel is ignored. Multi-user is a separate design.
- **No reverse-routing.** An agent saying something in pogo (e.g. mayor mailing pm-pogo) does not get promoted to a Discord channel by virtue of being intra-pogo traffic. Only the per-agent mailbox watch fans out.
- **No threading.** Discord threads are not modelled. One channel per agent, flat. Threading is a strict superset that can be added later if Daniel asks.
- **No per-channel command set.** A channel either *is* an agent target or *is not* — there is no "this channel only allows `idea:` commands" mode. That's a permissions matrix; not justified yet.

## Fork vs upstream PR

Recommendation: **upstream PR for P1, fork-then-PR for P2, in that order.**

Reasoning:

- **P1 is uncontroversial generalisation.** The hard-codings it removes (`architect`, `pogo-inbox`, the polling interval, the approval-prefix pattern) are real config gaps for *anyone* whose pogo install names things differently. cloverross is unlikely to refuse a PR that adds optional env keys with their current values as defaults — there is no behaviour change for cloverross, and the diff is small enough to review.
- **P2 is a meaningful new feature.** It costs cloverross design review and an ongoing maintenance burden for a feature cloverross may not run. Better to land the implementation in a Daniel-side fork first (`drellem2/bridget`), let it bake on Daniel's machine for a week or two, then propose the upstream PR with the operational evidence. If cloverross declines, Daniel keeps running off his fork; if cloverross accepts, the fork can re-converge.
- **Forking too eagerly is wasted divergence.** Bridget is small (~1k LOC, one file) but it is also young (v1.0.0 was tagged the same day Daniel asked for this analysis). Catching the upstream while it's still a single author and still close to its origin is the cheapest time to land a generic-config PR; the longer Daniel runs off a fork, the more manual cherry-picks compound.

A specific landing order avoids gratuitous churn:

1. **PR #1 to `cloverross/bridget`:** P1 env keys + the `^pm-` widening + the approval-prefix `re.compile`. Self-contained; no behaviour change for existing users. ~150 LOC including README updates.
2. **Fork at `drellem2/bridget` rebased on PR #1's branch:** implement P2 (per-channel routing). Run on Daniel's machine for ~2 weeks.
3. **PR #2 to `cloverross/bridget`** once Daniel is happy: rebase on the now-merged PR #1 main and propose the channel-routing feature with a real-world operator citation.

If cloverross rejects PR #2, Daniel's fork keeps the feature; the cost is ongoing cherry-picks of upstream bug fixes, which is tractable for a 1k-LOC repo.

The bridget repo's GPL-3.0-or-later license permits both forking and PRs without further negotiation; the only constraint is that Daniel's fork must remain GPL-compatible. That's already pogo's posture.

## How this slots into pogo's existing model

P2 should not invent new pogo abstractions. It composes with what is already there:

- **mailbox.** `mg mail send <agent>` already exists; bridget writes to it. The mailbox *is* the queue for inbound channel-routed messages.
- **nudge.** `pogo nudge <agent>` already exists and is exposed to bridget. P2 inherits it for free — typing `nudge` in a mapped channel could be a sugar for `pogo nudge <channel-agent>`.
- **agent-status.** `~/.pogo/agent-status/<name>.json` already feeds the `agents` command's cycle data; P2 needs nothing new from this layer.
- **quiet.json.** Already a shared signal that crew agents consume; P2's optional outbound-respects-quiet flag (P1 above) is the bridget-side pickup.

What P2 does *not* require: no new pogo CLI, no new `mg` flag, no new pogod endpoint. The whole feature lives inside bridget, reading `~/.pogo/bridget.channels.toml` and consuming existing pogo primitives.

This is the same shape as `pogo-reminders` — a single-purpose bridge that listens to one external system, talks to pogo via the public CLI, and stays in `~/.pogo/<state>` for its own files.

## Out of scope

- Multi-collaborator / multi-tenant Discord. Today's bot is one-human-one-server; opening that up is a separate design with separate ACL questions.
- Two-way message threading. Discord threads modelled as pogo conversations is a real shape but a far larger ticket.
- Replacing the maildir polling with an event source (e.g. an inotify/FSEvents-driven watcher in pogod itself). Adjacent infra; not bridget's problem.
- Refactoring bridget into a package layout. It's one file by design and at 1k LOC is still readable; a refactor is premature.
- Changing pogo to *push* notifications to bridget rather than have bridget poll. Larger pogod-side change; out of scope for a bridge-side analysis.

## Roadmap (follow-up implementation tickets)

Filed once Daniel picks. Sized in rough working days. (a) and (b) are the upstream PR; (c)–(f) are the fork.

(a) `bridget`: add `POGO_WORKFLOW_AGENT`, `POGO_INBOX_TAG`, `BRIDGET_POLL_INTERVAL`, `BRIDGET_QUIET_RESPECTS_OUTBOUND` env keys; thread through `handle_command` and the three watchers; widen the non-polecat assignees set to `^pm-`; lift approval-prefix to `BRIDGET_APPROVAL_RE`; doc updates — 1d.
(b) Open PR #1 to `cloverross/bridget`. Wait. — 0d coding, calendar.
(c) Stand up `drellem2/bridget` fork rebased on PR #1's branch — 0.5d.
(d) `bridget`: `~/.pogo/bridget.channels.toml` loader; `intents.guilds` + `intents.guild_messages`; `on_message` channel branch; per-agent fan-out in `watch_mailbox` / `watch_task_transitions` / `watch_idea_claims` — 2–3d.
(e) `bridget.channels.toml.example` + README "Per-channel routing" section — 0.5d.
(f) Operate for ~2 weeks on Daniel's machine; collect issues — 0d coding, calendar.
(g) Open PR #2 to `cloverross/bridget` with operational evidence — 0.5d.

## Design rationale

Three things drove the shape:

1. **Don't fork what a small PR can fix.** Bridget is young, single-author, and the hard-codings it has are the kind any operator with non-default agent naming would hit. Generalising via env keys with current values as defaults is the cheapest path to "Daniel's machine works without divergence" *and* is the friendliest contribution upstream.
2. **One channel per agent is a routing problem, not a protocol problem.** Daniel's open-claw reference points at *where* messages go, not *how* agents talk. Pogo already has the mailbox, the nudge, the agent-status — bridget's job is to map a Discord channel ID to an agent name on each direction. Resisting the temptation to model "agents in Discord" with new abstractions keeps P2 small.
3. **Stage the fork.** Land the small PR upstream first so the fork has the smallest possible delta. A 1k-LOC repo with a 200-LOC fork is maintainable; a 1k-LOC repo with a 600-LOC fork is the start of a permanent divergence. Sequencing P1 → fork P2 → re-PR keeps the fork minimal and gives cloverross a chance to upstream the bigger change with real operator data behind it.
