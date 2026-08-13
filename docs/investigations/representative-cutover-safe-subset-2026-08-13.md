# Representative cutover — the safe subset, performed and verified (2026-08-13)

**Work item:** mg-d611 (split from mg-8158, which retains steps 5 and 6 and its human gate)
**Scope:** step 1 (registration), step 2 (watermark), the bridget verification, the
`MIN_AGE_SECONDS` confirmation. **Nothing in steps 5 or 6 was touched.**

The cutover this covers is `~/.pogo/agents/crew/representative.md`, the operator prompt for
the relay described in [ARCHITECTURE.md](../../ARCHITECTURE.md) ("Mail to the human, and the
optional representative relay") and [docs/CONFIGURATION.md](../CONFIGURATION.md)
(`[agents] escalation_box`). pogo's own stake in it is one config key; the moving parts live
in `~/.pogo`, `pogo-reminders` and `bridget`. This record exists because three of the four
items below had been carried as *assumed* for four days, and two of the assumptions were wrong.

**Outcome in one line:** steps 1 and 2 are now DONE and verified; the `MIN_AGE_SECONDS=900`
gate is CONFIRMED against behaviour; bridget's `POGO_MAIL_DIR` re-point is **CONFIRMED NOT
DONE**; and architect's finding that bridget's mg-65d2 half "has never executed" is
**superseded** — it has been executing since 2026-08-11T21:57:09Z, and the reason nothing
happened is a config gate nobody had read, not the dormancy.

---

## 1. Registration — done, and the phantom box adopted

### Pre-state, recorded before touching anything

```
$ mg mail list | grep -E 'daniel|representative|human'
  daniel            35 unread  UNREGISTERED
  daniel-creator    1 unread  UNREGISTERED
  human             1537 unread

$ ls ~/.macguffin/mail/daniel/.registration.json
ls: .../daniel/.registration.json: No such file or directory
$ ls -d ~/.macguffin/mail/representative
ls: .../representative: No such file or directory
$ cd ~/.macguffin/mail/daniel && echo "new=$(ls new|wc -l) cur=$(ls cur|wc -l)"
new=35 cur=1
```

So the two boxes were in **different** states, and the ticket's caution was right to insist
this be looked at rather than inferred:

- **`daniel` existed and worked, with no registration record.** It is the phantom box mg-d639
  describes — minted by delivery, not by intent. Its oldest message dates to 2026-03-23, its
  newest to 2026-08-09. Mail had been arriving for months.
- **`representative` did not exist at all.** Every `mg mail send representative` would have
  been refused.

### What the phantom box had done to state

Nothing destructive, and that is the whole problem. A box created by delivery is
byte-identical on disk to one somebody set up deliberately: same `new/`/`cur/`/`tmp/`, same
permissions, no marker either way. The only difference was the absent `.registration.json`,
and nothing consults that at delivery time — which is exactly why "mail arrives at `daniel`,
therefore step 1 is done" was unsound, and why the ticket said so. The 36 messages already in
the box are the evidence that a name can be talked past the refusal once and be a good
address forever after.

### The commands, and their result

```
$ mg mail register representative --json
{"mailbox":"representative","created":true,"registered":true,"adopted":false,
 "prior_messages":0,"registered_at":"2026-08-13T09:44:43Z","registered_by":"pd611","via":"register"}

$ mg mail register daniel --json
{"mailbox":"daniel","created":false,"registered":true,"adopted":true,
 "prior_messages":36,"registered_at":"2026-08-13T09:44:43Z","registered_by":"pd611","via":"register"}
```

`created:false, adopted:true, prior_messages:36` is the phantom box being brought into
compliance rather than replaced. Adoption vouches for the **name** going forward; it makes no
claim about the 36 messages that were already there, and none of them moved.

### Post-state

```
$ mg mail list | grep -E 'daniel|representative|human'
  daniel            35 unread                 <- UNREGISTERED marker gone
  daniel-creator    1 unread  UNREGISTERED    <- unrelated, left alone (see §5)
  human             1538 unread
  representative    0 unread

$ echo "daniel new=$(ls ~/.macguffin/mail/daniel/new|wc -l) cur=$(ls ~/.macguffin/mail/daniel/cur|wc -l)"
daniel new=35 cur=1                            <- unchanged; registration never touches mail
```

Idempotency checked, because a step that cannot be safely repeated is a step that gets
half-done: a second `mg mail register daniel` is exit 0 with `registered:false`, and the
record's original `registered_by`/`registered_at` survive.

### Control — is the refusal this step exists to satisfy actually live?

Registering a box only matters if `mg mail send` really refuses an unregistered one. Probed
with a deliberate near-miss rather than assumed:

```
$ mg mail send representativ --from=pd611 --subject="control probe" --body="..."
Error: no mailbox named "representativ", and no work item is called that either:
  mg has never seen this recipient — did you mean representative?
$ echo $?
3
```

Two things at once: the refusal is live (so registration was load-bearing, not ceremonial),
and the did-you-mean now resolves `representative`, which is the new registration being read
back out of the resolver by a different code path than the one that wrote it.

---

## 2. Watermark — seeded, and verified by the partition it produces

`~/.pogo/agents/representative-state/` did not exist. Both the file and its directory were
created.

```
$ cat ~/.pogo/agents/representative-state/watermark
1786614170284987000.54615.7000
$ wc -lc ~/.pogo/agents/representative-state/watermark
       1      31
```

**Its only reader is the prompt.** Grepped for `watermark` across the pogo worktree,
`pogo-reminders` and `~/.pogo/agents`: the sole hit outside the new files is
`crew/representative.md:281`. No Go code, no shell, no launchd job parses it. So the contract
is exactly what the prompt states — one line, one message id — and it was written that way.

### Verified by observation, not by having run the command

```
$ WM=$(cat ~/.pogo/agents/representative-state/watermark)
$ ls ~/.macguffin/mail/human/new/$WM >/dev/null && echo YES
YES                                     <- the id names a message that exists
$ cd ~/.macguffin/mail/human/new
$ ls | wc -l                            ; # 1538  total
$ ls | sort | awk -v w="$WM" '$0<=w' | wc -l   ; # 1538  at or below  -> BACKLOG
$ ls | sort | awk -v w="$WM" '$0>w'    | wc -l ; #    0  above        -> live
$ ls -lt | tail -1
-rw-r--r--@ 1 daniel staff 1260 Apr 30 13:59 1777553953123023000.1813.3000
```

That is the semantics the prompt describes, checked rather than asserted: the watermark names
a real message, and it partitions `human/new` into 1538 archive and 0 live — i.e. it is
genuinely the maximum, so "everything present at install time is archive" holds exactly.
The oldest backlog message is from 30 April, confirming the "four months" the design warned
about is a measurement and not a figure of speech.

Sorting lexicographically is sound here because maildir names begin with a fixed-width
nanosecond epoch — the same property `poll-mail.sh` relies on for "glob order == chronological
order". Cross-checked against `ls -t`: both orderings named the same newest file.

**What this does NOT verify.** The watermark is an artifact; the *rule* lives in the prompt and
only executes when the agent runs, which is step 6 and human-gated. This record confirms the
file is correct, present, well-formed and correctly positioned. It cannot confirm the agent
will obey it.

### A watermark is a point in time, and steps 5/6 are still gated

This seeding bounds the first cycle to "mail that arrived after 2026-08-13T09:42Z", not to
"mail that arrived after the agent started". If mg-8158 is worked days or weeks from now, the
gap between the two is mail the representative will treat as live and summarise. That is the
*safe* direction of error — over-inclusive, never dropping — but it degrades. A README beside
the file records the one-line re-seed, and whoever performs step 6 should run it immediately
before starting the agent:

```bash
ls ~/.macguffin/mail/human/new | sort | tail -1 > ~/.pogo/agents/representative-state/watermark
```

---

## 3. `MIN_AGE_SECONDS=900` — CONFIRMED, against behaviour

The gate is configured in `com.pogo.deadman.plist` and reaches the process
(`launchctl print gui/501/com.pogo.deadman` shows `MIN_AGE_SECONDS => 900`,
`state = running`). Both of those are config text, which the acceptance criterion explicitly
refused. Two behavioural measurements were made instead.

### (a) 98 real deliveries over a day, none under the gate

`poll-mail.sh` does not move mail, so a delivered message is normally still in `human/new` and
its mtime can be compared to the delivery timestamp in `~/.pogo/reminders/deadman.log`:

```
window: the last 100 'New mail' lines   (2026-08-12T11:25:46Z .. 2026-08-13T09:59:25Z)
  unmeasurable (file since moved to cur/ by a reader):  2
  measured: 98    min 948s    median 1015s
  under the 900s gate: 0
```

The two unmeasurable rows are stated rather than dropped: a message someone has since read is
in `cur/`, so its delivery age cannot be recovered. They are 2% of the window and their absence
cannot manufacture a floor — a violation would have to be *in* the measured 98 to be hidden by
them. The maximum is an outlier at 2 661 587s (a month-old backlog message delivered late,
which is over the gate, not under it), so the floor and the median are the informative
statistics.

The *shape* matters as much as the floor. Ages cluster in a narrow band just above 900s, which
is what a 900s gate plus a ~60–110s poll cadence produces. Absent the gate, ages would cluster
near the poll interval — tens of seconds, not sixteen minutes.

### (b) A controlled message, both halves of the gate

A mail was sent to `human` at a known instant and watched:

```
written           2026-08-13T09:42:50Z   (1786614170284987000.54615.7000)
polled and held   age 215s .. 888s, 17 observations across ~11 poll cycles, delivered=0
delivered         2026-08-13T09:59:25Z   age 995s
```

So the gate **holds** (a message under 900s is skipped on every cycle, repeatedly, not merely
once) and **releases** (the same message is delivered once it ages out, rather than being
marked seen and lost). The second half is the one worth having measured: `poll-mail.sh:781-786`
is careful to `continue` without marking seen precisely so a too-young message is reconsidered,
and a bug there would look identical to a working gate for the first fifteen minutes.

**Consequence for the cutover, unchanged:** the ratio between this 900s window and the
representative's 2-minute cadence is the load-bearing quantity. It is intact.

---

## 4. bridget — the third reader, and the item nobody had checked

### 4a. The `POGO_MAIL_DIR` re-point is **NOT DONE**

Checked in all four places it could live, including the one that actually decides:

| Where | Command | Result |
|---|---|---|
| `~/.pogo/bridget.env` | `grep -c POGO_MAIL_DIR ~/.pogo/bridget.env` | `0` |
| `bridget-supervise` | `grep -n 'POGO_MAIL_DIR\|MAIL_DIR' ~/.pogo/bin/bridget-supervise` | no match (exit 1) |
| plist `EnvironmentVariables` | read `~/Library/LaunchAgents/com.pogo.bridget.plist` | only `PATH` and `HOME` |
| **the running process** | `ps eww -p 1736` | 17 env entries, **no `POGO_*` at all** |

That set is exhaustive rather than merely long, because bridget resolves config from exactly
two sources and both are covered:

```python
def lookup(key: str) -> str | None:
    # Process env wins over the env file so launchd / systemd overrides work.
    return os.environ.get(key) or file_env.get(key)          # bridget:302-304
```

Row 1 is `file_env`; row 4 is `os.environ` **as the running process actually holds it**, which
is what rows 2 and 3 can only predict. The `ps eww` output is 793 bytes and carries the plist's
full 113-byte `PATH`, so it is not a truncated listing. With both sources empty,
`bridget:333-335` takes its fallback and `MAIL_DIR = ~/.macguffin/mail/human/new`
(`bridget:481`, `mail_dir = mail_parent / 'new'`).

`bridget.env`'s mtime had moved since mg-8158 was written (2026-07-10 → 2026-08-11, the
`BRIDGET_DM_POLICY` edit), so the parent's "no match at mtime 2026-07-10" needed re-running
rather than inheriting. It was re-run. Still no match.

**bridget is therefore still a live third delivery channel on the box the fleet writes.**
`watch_mailbox()` (`bridget:1876`) polls `MAIL_DIR` and DMs Daniel on Discord for each new
message. Post-cutover that bypasses the representative entirely, for every message, with no
age gate — a strictly larger bypass than the deadman's. This step is not optional and it is
not done.

### 4b. Approval scanning — found, and it is not at line 2238

`scan_pending_approvals()` is at **`bridget:2534`**, not 2238; the memo's line number has
rotted (2238 is now mid-docstring in the task-state reconciler). It iterates `MAIL_DIR`, so it
follows the re-point:

```python
def scan_pending_approvals() -> list[str]:
    """Subjects of unread mails in human/new/ that are approval requests."""
    if not MAIL_DIR.exists(): return []
    for p in sorted(MAIL_DIR.iterdir()):
        ... if APPROVAL_RE.match(line): ...
```

It is **working today** against `human/new`, and it feeds the Discord `status` and `mine`
views ("Awaiting your approval (N)").

**A finding for whoever performs the re-point, which nothing in mg-8158 anticipates.**
`APPROVAL_RE` defaults to `^Subject: approval needed ` (`bridget:424`). Agents write that
subject to `human`. After the re-point, `scan_pending_approvals` reads `daniel/new` — which
contains the representative's **rewritten** messages, and rewriting the subject is the
representative's job (its prompt forbids holding or dropping an approval request but requires
rewriting it, and forbids internal identifiers in subjects). A rewritten subject does not match
the regex, so the approvals section silently empties and reads as "nothing awaiting you". That
is the mg-f04b shape again: a plausible configuration that is delivered to, filed, and never
read, with no instrument distinguishing it from a working channel.

Two ways out, both cheap, neither chosen here (out of scope): set `BRIDGET_APPROVAL_RE` to
match whatever the representative writes, or split the approval scan's directory from the
watcher's so only the watcher moves. **Recorded as its own item rather than fixed here.**

### 4c. architect's dormancy finding is SUPERSEDED — the restart already happened

mg-8158 carries architect's arithmetic that the running bridget (pid 28953, started
2026-08-06 15:13) predated commit `1582e04` (2026-08-07 19:14), so the mg-65d2 half "HAS NEVER
EXECUTED", and framed the danger as: *what will turn it on, and who will be watching when it
does?* — answered "a crash" and "nobody".

That was correct on 2026-08-09. It is no longer true. **The restart it feared has already
occurred, unattended, and nothing happened.**

```
$ ps -eo pid,lstart,command | grep '[b]ridget'
 1685 Tue Aug 11 22:57:09 2026  bash /Users/daniel/.pogo/bin/bridget-supervise
 1736 Tue Aug 11 22:57:09 2026  .../Python /Users/daniel/dev/bridget/bridget

$ grep 'supervise: starting bridget' ~/.pogo/bridget.log | tail -1
[2026-08-11T21:57:09Z] supervise: starting bridget (spawn #1) at 4baa3f7 on main (current with origin/main)
```

`4baa3f7` is `origin/main` and contains `1582e04`. The spawn line is itself mg-6ca7's
activation record — the fix built *for this class of problem* is what makes the answer
readable, four days after architect had to derive it from `ps` and `git log`.

**Why the unattended activation was harmless, which is the part worth carrying forward.** It
was not luck and it was not the dormancy. `relay_copy_to_representative()` (`bridget:3150`)
opens with:

```python
    if not REPRESENTATIVE:
        return
```

and `REPRESENTATIVE = lookup('POGO_REPRESENTATIVE') or ''` (`bridget:361`) — absent from the
running process's environment along with every other `POGO_*`. The seam ships **off by
default**, deliberately, so that an install without a representative behaves exactly as it
always has. Confirmed by observation on both sides:

```
$ ls ~/.macguffin/mail/representative/new ~/.macguffin/mail/representative/cur    # both empty
$ grep -c 'representative inbound copy' ~/.pogo/bridget.err.log
0
```

The second is the stronger control. The code prints one stderr line **per failed attempt**,
precisely so a misconfigured `POGO_REPRESENTATIVE` cannot fail silently forever; zero lines in
a 5MB log means the branch has never been taken, not that it failed quietly. And a live
inbound reply arrived during this work (Daniel replied via Discord at 09:44:09Z and it was
relayed to `pd611` as `--from=human`), so the surrounding path was demonstrably executing at
the time the copy did not happen.

**So the corrected statement of bridget's risk:** the inbound copy seam is inert *by
configuration*, not by a process not having restarted, and it stays inert until someone sets
`POGO_REPRESENTATIVE`. That is a deliberate switch, not a race. The genuine unattended risk
architect identified was real and has now passed through without incident. What remains
un-done is 4a — and that one does not self-arm, because its absence is the fallback.

---

## 5. What was deliberately not done

- **Steps 5 and 6 were not touched.** `~/.pogo/config.toml` was not edited, pogod was not
  restarted, `auto_start` in `crew/representative.md` is still `false`, and no agent was
  started. They remain on mg-8158 behind its human gate. Nothing in this subset turned out to
  require them.
- **bridget's `POGO_MAIL_DIR` was not set.** The item asked for it to be *verified*, and it
  verified as not done. Setting it is a reader re-point, and the lesson mg-8158 asks be
  carried is that a reader re-point must land *after* the thing feeding the new location —
  which is the representative, which is step 6. Doing it now would send bridget to poll a box
  nothing writes.
- **`daniel-creator` (1 unread, UNREGISTERED) was left alone.** Out of scope, and adoption is
  a statement about a name being meant, which is not this polecat's to make.
- **The Discord bot token was not rotated.** It was echoed into this session's transcript by a
  careless `cat ~/.pogo/bridget.env` (the right command was the `grep` used everywhere else in
  §4a). Reported to Daniel immediately; he replied "the transcripts are local files as is the
  token, no big deal" and it was left.

## 6. The remedy checked against its own defect

This item's defect class is *a state assumed rather than observed*. The obvious way for its
remedy to carry that defect is to report a step as done because the command exited 0.

- Registration: verified by re-reading the records from disk, by the disappearance of the
  `UNREGISTERED` marker in a second tool, and by a negative control proving the refusal it
  satisfies is live — not by `exit=0`.
- Watermark: verified by the partition it produces over 1538 real messages, and by confirming
  the id resolves to a file — not by `cat` of the thing just written.
- MIN_AGE: verified against 25 historical deliveries and one controlled message with a known
  write time — not by the plist and not by `launchctl print`.
- bridget: verified against the **running process's environment** and its own startup record,
  not against the three files that would have been read as authoritative. Two of the parent's
  facts (the env-file mtime, the process start time) had rotted in four days; both were
  re-measured rather than inherited. The line number 2238 had rotted too.

The one place the pattern is knowingly unresolved: §2's watermark rule and §4b's approval
regex can only be exercised once the representative runs, which is human-gated. Both are
flagged as unverified rather than reported as passing.
