package turnlog

// ClauseMarker is the substring a renderer tests for before appending
// PromptClause, so a prompt that already carries the clause — a synthesized
// file copied back into crew/, most plausibly — does not accumulate copies of
// it on every render.
const ClauseMarker = "## Turn-completion log"

// PromptClause is the turn-completion instruction appended to EVERY crew
// agent's rendered prompt, including the coordinator's.
//
// WHY IT IS A RENDERER-INJECTED CONSTANT RATHER THAN PROMPT TEXT. mg-a270's
// gap was mayor, pa and architect. Two of those three prompts —
// ~/.pogo/agents/crew/architect.md and crew/pa.md — are user-authored: they
// exist in no embed, the installer never writes them, and `pogo prompts
// install` is explicitly forbidden from touching them. So any fix that edits
// shipped prompt files reaches doctor and the PM template and STOPS EXACTLY
// SHORT of the two agents the ticket is about. The convention has to be a
// property of the renderer, which every crew agent passes through
// (agent.SynthesizeExtendsPrompt), not a paragraph each prompt author has to
// have copied.
//
// That is also what makes it uniform without special-casing the PM tier: the
// PMs receive this clause through the same injection point as everyone else,
// and their existing sweep.log heartbeat — which was never designed as a
// liveness primitive and covers the two agents that need it least — stops
// being the fleet's only turn-completion evidence.
//
// It is deliberately NOT in the prompts embed. A file under prompts/ is
// installed to disk, becomes user-editable, and acquires a staleness
// comparison; this text is a fleet invariant that an operator editing their
// own crew prompt should not be able to silently drop.
//
// The `pogo turn-done` invocation here is covered by check-prompts' CLI-surface
// check the same way the shipped corpus is — see
// cmd/pogo/checkprompts_turnlog_test.go. It is the one instruction every agent
// on the machine receives, so a wrong flag in it would be wrong everywhere at
// once.
const PromptClause = `
## Turn-completion log — one line, every turn, no exceptions

At the **end of every turn**, once that turn's work is actually done, run:

` + "```bash" + `
pogo turn-done --note="<a few words on what you just finished>"
` + "```" + `

It appends one timestamped line to ` + "`~/.pogo/agents/turnlog/<your-name>.log`" + `,
takes well under a second, and is not optional. Do it last, after the work —
not first, and not as part of setting up the turn.

**Why it exists, so you do not quietly drop it as boilerplate.** On 2026-08-10/11
this fleet was inert for twenty-two hours while every signal about the daemon
read green. The processes existed. The schedules were registered. 140 nudges
were delivered. The running revision was current. All of that was TRUE, and none
of it was about you — every one of those signals measures what the daemon did,
and the daemon was fine. The outage was eventually diagnosed from the only two files on the
machine that a stopped agent cannot produce: two heartbeat logs that happened to
exist. Three of the five running agents had no such file, and one of those three
was the coordinator that every other detector routes through.

Your turnlog line is that file, for you. It is the only artifact here that
**nothing except a completed turn of yours can create**, which is what makes its
absence mean something. A liveness check that would read green over a fleet that
is present and doing nothing is a presence check wearing a liveness label, and
this line is what keeps this one honest.

So: write it when you finish a turn. Never in advance, never in a loop, never
from a background script, and never on another agent's behalf — if you notice a
peer has gone quiet, report that, do not write a line for them. A line that
something other than your own finished turn can produce is worth exactly what
` + "`nudge_delivered`" + ` was worth during those twenty-two hours, which is nothing.

If ` + "`pogo turn-done`" + ` is somehow unavailable, the equivalent is one append —
same path, same format:

` + "```bash" + `
mkdir -p ~/.pogo/agents/turnlog && echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $POGO_AGENT_NAME finished a turn" >> ~/.pogo/agents/turnlog/$POGO_AGENT_NAME.log
` + "```" + `
`
