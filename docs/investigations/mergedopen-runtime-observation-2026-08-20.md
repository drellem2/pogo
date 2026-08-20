# The merged-not-closed alert and the merged-work gate, observed at runtime

**2026-08-20 · mg-161a** (successor to mg-f17c, which carried the ruling half of
mg-9d4e and could not carry this one)

---

## What was owed

mg-9d4e added two things to pogod and neither had ever run outside a test:

1. **the merged-work gate** — the fifth refusal at the dispatch chokepoint,
   which stops a worker being dispatched onto a work item whose branch has
   already landed on the target;
2. **the merged-not-closed alert** — a coordinator-addressed mail plus a
   `work_item_merged_not_closed` event, raised when pogod's close-at-merge is
   refused and the item stays open with its work already on `main`.

mg-f17c ruled on the alert's gated-item exclusion and closed. It could not close
this half, and said why in one sentence that decides the whole shape of the work:

> **No unit test can substitute: `defaultMergedOpenAlertMail` refuses to send
> under `testing.Testing()` by construction.**

That is not an oversight in the tests. It is a deliberate guard, and it is
correct — without it a `go test ./cmd/pogod/...` run manufactures a genuine fleet
alarm in the coordinator's real inbox, which is the same reasoning as
`macguffinStoreRoot`'s. Its consequence is that **every** unit test of this path
(`cmd/pogod/mergedopen_test.go`, `testmain_test.go`) asserts against a stub
installed in the sink's place. A green `cmd/pogod` suite is therefore evidence
about the stub. Adding more of it would have produced more of the same evidence.

## The precondition, checked rather than assumed

The ticket had waited on a pogod redeploy. A redeploy having *run* is not the
same as this code being *live*, so the running revision was asked directly:

```
$ curl -s http://127.0.0.1:10000/version | jq -r .revision
c6091d3047f64478f005cb62d8c405dab70a78b2          # pogo 0.10.0

$ git merge-base --is-ancestor 2c6c47d c6091d3    # the mg-9d4e rescue
$ echo $?
0
```

So the gate and the alert are running code on this host. That is the
precondition, not the observation.

## How the observation was made

`scripts/mergedopen-runtime_test.sh`. The one property that matters is that the
daemon under observation is **not a test binary**, so the production sink runs:

- `go install ./cmd/pogo ./cmd/pogod` into a private `bin` (built under the real
  HOME, before isolation, so the toolchain and module caches are warm; `-p` is
  the core budget);
- `scripts/pogo-sandbox` for a private `HOME` / `XDG_CONFIG_HOME` / `POGO_HOME` /
  `MG_ROOT` and a port reservation with an ownership proof, plus a private
  `TMPDIR` (the daemon lockfile and agent-socket dir are anchored to
  `os.TempDir()`, which `pogo-sandbox` does not cover);
- a bare origin, a work repo whose `./build.sh` exits 0, and two work items filed
  `--type=design` so `mg new` writes `declares-remainder`, each **claimed** —
  because `mg done` on an *unclaimed* item is refused for a different reason, and
  that would be a different scenario;
- two real submissions through the real refinery, to a real merge.

Nothing about the alert path is stubbed, faked or shortened. `mg done` is refused
by mg for the reason the alert exists to report; `reportMergedButOpen` runs;
`defaultMergedOpenAlertMail` runs; `mg mail send` writes a maildir file.

## What was observed — 21 of 21

### The alert, red arm (no coordinator mailbox registered)

```
work item mg-515a, branch probe-red, merged as c29377e… on main

{"event_type":"work_item_merged_not_closed","agent":"pogod","work_item_id":"mg-515a",
 "details":{"branch":"probe-red","close_error":"item declares a remainder and NO item
 in the store names it as a predecessor, so no successor was ever filed: mg done
 failed: …"}}

{"event_type":"work_item_merged_not_closed_undelivered","agent":"pogod",
 "work_item_id":"mg-515a","details":{"error":"mg mail send failed: Error: no mailbox
 named \"mayor\" …"}}
```

and no `[merged-not-closed]` mail anywhere under `MG_ROOT`. **The red arm bites**:
the event is on the spine first by design, the send fails, and the failure is
itself recorded.

### The alert, green arm (mailbox registered, second merge)

```
Message-Id: 1787195319772838000.8888.8000
From: pogod
Subject: [merged-not-closed] mg-aef9 is MERGED but still open — file its successor, do not dispatch

A branch merged and its work item did not close. The work is on main; the item is not
closed, so it returns to the pool describing itself as unstarted.
…
WHAT TO DO — close the item, do not dispatch at it:

    mg done mg-aef9 --successor=<the id that carries the remainder>
```

read back through `mg mail list mayor`, i.e. as a coordinator would see it, not
just as a file the sender put somewhere.

### The discriminator mg-9d4e warned about

`cmd/pogod/mergedopen.go` warns whoever verifies this that a **different**
merged-not-closed mail already arrives, from `internal/filernotify`, and that
reading the old path as evidence for the new one would score the observation as
done when nothing new had run. The warning is well founded: in the same sandbox
run, on the same merge, the log carries

```
filernotify: told mayor that work item mg-2728 MERGED BUT IS STILL OPEN (route=merge, redirected=true)
```

— the old path delivering to the *same box*. So the separation is asserted rather
than left to the reader: the alert mail must carry the bracketed subject and
`From: pogod`, and must **not** carry the old path's `MERGED BUT NOT CLOSED:`
subject.

### The gate, in the sandbox

With the claim released — the live shape, since pogod stops the merged polecat
about half a second after the merge and its claim goes with it — the item is back
in `available/`, which is exactly what priority-wake advertises as ready:

```
$ pogo agent spawn-polecat … --id=mg-aef9
spawn-polecat failed: work item mg-aef9 HAS ALREADY MERGED: branch probe-green
landed on main as ac1b374e1a88 2s ago (mr-da36vdqtjv1hca8nm4k0). A worker
dispatched here would re-derive work that is on the target right now …
```

`ac1b374e1a88` is the commit on `origin/main`. No worktree and no agent dir were
left behind. And `--merged-override="…"` dispatches over the identical refusal,
emitting `dispatch_merged_work_overridden` with the reason — **which is what
proves the refusal came from this gate** and not from one of the four ahead of it
at the same chokepoint.

### The gate, on the LIVE daemon

Not a sandbox. The running pogod (`c6091d3`), a real work item whose branch
merged eighteen minutes earlier:

```
$ pogo agent spawn-polecat p161aprobe --id=mg-5b5e --repo=/Users/daniel/dev/pogo \
      --template=mg161a-probe-no-such-template …
spawn-polecat failed: work item mg-5b5e HAS ALREADY MERGED: branch polecat-p5b5e
landed on main as d2fcef9a2307 18m35s ago (mr-da36i3atjv1iqerunf80). …
```

`d2fcef9` is on `main`. Nothing was created: no `~/.pogo/polecats/p161aprobe`, no
`~/.pogo/agents/p161aprobe`, nothing in `pogo agent list`.

**Why that probe was safe to run against the live fleet**, since a probe of a
refusal is only safe if the failure direction is harmless. `--template` names a
template that does not exist. In `internal/agent/api.go` the gates run first;
`ResolveTemplate` runs *after* all of them and *before* the worktree, agent dir
and expanded prompt file (mg-ef80). So the request had exactly two outcomes: the
gate refuses (409, the observation), or the gate fails open and the request dies
at template resolution (404) having created nothing. There was no path on which a
worker was dispatched.

## What is NOT established

- **The alert has never fired on the live fleet.** Measured 2026-08-20 after the
  redeploy: **0** `work_item_merged_not_closed` events across `~/.pogo/events.log`
  and `events.log.1`, and **0** mails under `~/.macguffin/mail` whose `Subject:`
  line is this path's — against **18** old-path `MERGED BUT NOT CLOSED:` filer
  mails. Recorded, in the form the coordinator required (see the ruling below):
  **demonstrated in sandbox on both arms; not yet witnessed on the live fleet;
  the observation is SCHEDULED rather than awaited.**
- **The coordinator mailbox in the observation is the sandbox's,** not the live
  mayor's. What that costs is the routing to *this machine's* mayor; what it does
  not cost is any part of the sink, since the same `client.SendMGMail` →
  `mg mail send` → maildir path runs either way and the recipient comes from
  `agent.CoordinatorName()` in both.
- **The live gate probe used an archived item.** `mg-5b5e` is `archived`, and the
  gate refused it anyway — which is its documented behaviour, not a defect: it
  does not consult the item, because mg's status cannot distinguish "never
  closed" from "closed and reopened". The refusal names the merge and offers
  `--merged-override`, which is the documented way out. The probe therefore
  observed the gate firing; it did not observe it firing on an item that was
  *also* open.
- **The live refusal is not in `pogod.log`** — but it is not untraced either, and
  the difference matters, because the log is where a reader would look and the
  spine is where it actually is. `failPolecatSpawn` emits before it writes the
  HTTP error, so the live observation has a durable record independent of this
  polecat's terminal:

  ```json
  {"timestamp":"2026-08-20T03:12:37.874026Z","event_type":"agent_spawn_failed",
   "agent":"cat-p161aprobe","work_item_id":"mg-5b5e","repo":"/Users/daniel/dev/pogo",
   "details":{"status_code":409,"reason":"work item mg-5b5e HAS ALREADY MERGED:
   branch polecat-p5b5e landed on main as d2fcef9a2307 18m35s ago
   (mr-da36i3atjv1iqerunf80) …"}}
  ```

  in `~/.pogo/events.log`. Anyone re-checking this observation should read it
  there rather than take this document's word for the refusal text.

## A trap in the census, for whoever re-measures

mg-f17c recorded its never-fired proof as "zero mails under `~/.macguffin/mail`
carrying the `[merged-not-closed]` subject prefix". Repeat that today as a
**substring** search and it returns 1:

```
$ grep -rlF '[merged-not-closed]' ~/.macguffin/mail | wc -l
1
$ grep -rl  '^Subject: \[merged-not-closed\]' ~/.macguffin/mail | wc -l
0
```

The single hit is `mayor/cur/1787183978874226000.99177.6000` — a filernotify
`COMPLETED:` mail for **mg-f17c itself**, whose body quotes that ticket's verdict
verbatim, including the sentence recording the string's absence. A census whose
own evidence contains the token it is counting. Anchor to `^Subject: `.

## The control's own defect, since a remedy is subject to the defect it remedies

The first version of `mail_body_for_subject` used a plain `grep`, in which
`[merged-not-closed]` is a bracket expression matching **one** character. The
pattern could not match the subject it was written to find. The green arm noticed
immediately — no mail was found and the assertion went red — but **the red arm did
not**, because an absence assertion passes for free when the search is broken.
OBS-3 had been passing on a store it had never really looked in.

The fix is `grep -rlF`, and the guard is OBS-3a: a decoy carrying the same subject
shape under a different id is planted and must be found at the instant the absence
is claimed. If OBS-3a fails, OBS-3 says nothing.

---

## The two limits mg-f17c carried, since this item was where they were parked

mg-161a's body carried two caveats forward "so they are not lost with that item".
Both are addressed here rather than passed on again.

### "18 is a floor, not a census" — **settled: it is a census**

mg-f17c's polecat counted **18** `MERGED BUT NOT CLOSED` events in
`~/Library/Logs/pogo/pogod.log` (13 declares-remainder, 5 gated) and flagged that
it had not established the rotation retained the whole window, so 18 was a lower
bound. Two checks close that, neither of them a re-run of the original count:

**The log covers the whole window.** The current `pogod.log` opens at
`2026/08/13 03:01:29` — *before* mg-2b71 landed at 04:44Z, which is when the
gated decline began — and `pogod.log.1`, `.2`, `.3` hold zero matches. There is no
window for a rotation to have eaten.

**A second instrument, independent of the log, agrees exactly.** Counted by
subject line over the mail store (mine, re-derived — not mg-f17c's figure
re-quoted):

```
$ grep -rl '^Subject: MERGED BUT NOT CLOSED:' ~/.macguffin/mail | wc -l
18                       # earliest Date: 2026-08-14T03:24:18Z
                         # latest   Date: 2026-08-19T22:26:46Z
$ grep -c 'filernotify: FAILED' ~/Library/Logs/pogo/pogod.log
0
```

The earliest mail is the same event as the log's earliest line
(`2026/08/14 04:24:18` local, BST) seen through the other instrument, and no send
failed, so mail count and event count are measuring the same population. **18 is
the count, not a bound on it.**

### "The `parked`/`human` sub-case has zero live instances" — **still true, still carried**

All 5 gated cases in that window were `blocked:mayor`. The single historical
`parked` instance (mg-479c, 2026-08-13 03:27Z) predates mg-2b71 landing at 04:44Z,
so its filer mail was never sent, and the routing claim for that sub-case remains
**untested**. Nothing in this item's work touched it. Not re-derived here; that is
mg-f17c's polecat's finding, quoted as theirs.

---

## The ruling on the residual

The live occasion could have been manufactured — file a throwaway
`declares-remainder` item, push a no-op branch, submit it with that item as author
— and this polecat asked the coordinator rather than deciding, because it puts a
real alarm in a real inbox and a real commit on `main`. **The coordinator chose
not to** (2026-08-20 03:20Z), and the reasoning is recorded here rather than only
the choice:

> What (a) would add is narrow: proof that the live fleet's own wiring delivers to
> a real mailbox. Real, but not worth a commit on main and a genuine alarm in the
> coordinator's inbox — a manufactured alarm is indistinguishable from a real one
> to everything downstream that reads that box, and I would be putting a false
> positive into the exact channel this path exists to make trustworthy.

And it refused the alternative **as this polecat had framed it**, which is the part
worth carrying:

> "It will happen on its own, 13 events in six days" is a prediction, not a plan.
> Nothing observes it when it fires, so as written (b) closes the residual by
> hoping someone notices — which is the same defect as an alarm nobody reads, one
> level up.

So the wait is **armed** rather than left standing: the coordinator is scheduling a
dated check that someone is obliged to run, carrying the anchored census below —
`^Subject: \[merged-not-closed\]`, never a substring match, for the reason set out
in the trap section above. That scheduling is the coordinator's action, not this
item's, and is recorded here so the residual can be chased to something that will
actually happen rather than to a sentence.
