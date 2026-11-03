# Live enth self-land rehearsal — 2026-11-03 (mg-6c099)

The first enth self-land (mg-5b904) through this box's **live** pogod, against
production enthd https://enth.app, on an enth-native scratch repo. Feeds the
cut-over box-op mg-a2df4. Everything below was observed, not inferred from exit
codes.

## Precondition (observed at 14:00Z)

- `pogo land --help` exits 0.
- pogod `/version`: revision `2f237ed9` (mg-5b904's merge), start_time 12:26:32Z.
- `~/.pogo/agents/templates/polecat.md` contains `self_land` (16 occurrences); the
  rendered polecat prompt for the rehearsal item carried it 17 times.

## Setup

- Repo `daniel/pogo-land-rehearsal` (writer=enth, source=null, private). Created
  13:51Z by the first dispatch (p6c099), together with bot user `daniel-pogo` and
  its 7-day token. **The bot was not used**: Daniel ruled at 13:55Z that the
  rehearsal runs on his own enth login (`~/.config/enth/credentials.json`). The
  per-project token is mg-3a813.
- **Seeding the empty repo needed a non-client step (an enth gap, not a pogo slice
  defect).** An enth-written repo starts with no trunk ref. `enth --remote checkout`
  of `@`, `main` and `refs/heads/main` all refuse `not_found` (exit 3), so neither
  a read checkout nor a polecat checkout can exist until something lands a root.
  The enth docs record this (epic 9 §3.6, "Not built here: the first Change of an
  empty enth-written repo through enthd … no client verb uploads a spec-built
  record yet"). Seed used here: build a root Change with the local tools
  (`enth-store`, `enth-change build`), upload its record and blobs with one
  `curl POST /v1/blobs` (`{"type":"stored","objects":3}`, HTTP 200), then land it
  with the normal client `enth --remote … --request-id mg-6c099/seed-land land <id>`
  → entry 1, `refs/heads/main` = `3c312abc…`. Author and committer are
  `Daniel Miller <daniel@users.noreply.enth.app>`. `/v1/change` was never called
  for the root, and the land still accepted it. So the root has no change entry,
  and `enth changes` does not list it.
- Read checkout: `enth --remote … checkout @ ~/dev/pogo-land-rehearsal`; `.enth/checkout`
  origin = `{remote: https://enth.app, repo: daniel/pogo-land-rehearsal}`.
- `pogo project add ~/dev/pogo-land-rehearsal --vcs=enth --enth-remote=https://enth.app
  --enth-repo=daniel/pogo-land-rehearsal` → project id 311, vcs=enth, trunk
  `refs/heads/main`.

## The self-land

Item mg-29d73 (append one line to NOTES.txt) was dispatched at 14:01:35Z:
`pogo agent spawn-polecat p29d73 --id=mg-29d73 --template=polecat --repo=…`.

- **Checkout:** pogod gave p29d73 a whole-repo enth checkout at
  `~/.pogo/polecats/p29d73`. `.enth/pogo-workspace.json` names the project, the remote, the repo,
  `refs/heads/main` and base `3c312abc…`.
- **Change:** `9c650909…` (change entry 3), parent `3c312abc…`, one op
  (`NOTES.txt` put), trailers `Pogo-Item: mg-29d73` and `Pogo-Worker: p29d73`.
  Identity carry-over: the author and committer are Daniel's git identity (name and the
  git `user.email`), not the noreply address. Whether enth Changes should carry that
  address is for pm-enth / pm-pogo to decide.
- **Land (self):** `enth query log --trailer Pogo-Item=mg-29d73` returns
  `9c650909…`, and the trunk tip is `9c650909…`. `enth changes` shows it as
  `landed: 9c650909…`.
- **Land record:** `pogo refinery show mr-dbkuil2tjv1m15691b30` → `status: merged`,
  `self_landed: true`, `merged_sha` absent, `enth: {ref: refs/heads/main, entry: 4,
  old: 3c312abc…, new: 9c650909…, request_id: mg-29d73/1}`.
- **Close:** at 14:01:56Z (21s after spawn) mg-29d73 was `done`. Sidecar:
  `completed_by: refinery`, `landed: {vcs: enth, id: 9c650909…}`, the
  worker's verdict (`pass`). The filer was mailed `COMPLETED` by pogod. The polecat
  was stopped after the land was recorded.
- **Deploy hook:** none ran. A self-land runs no per-repo deploy hook (§6.3), and
  the scratch repo has none configured anyway, so nothing was skipped.

## check-stranded (slice 6b, mg-28cbc): first live run against an enth project

- **After the land (negative):** the scratch repo row reads
  `enth: 0 item(s), 0 unlanded Change(s) listed by trailer (0 with NO Pogo-Item
  trailer)`, and there is no row for mg-29d73.
- **Positive control:** item mg-9b5ff was filed parked and closed by hand. It carries
  Change `88cfeba8…` (trailer `Pogo-Item=mg-9b5ff`, parent `9c650909…`, made with
  `enth change` and never landed). check-stranded then reported:

  ```
  stranded          done       mg-9b5ff
    1 unlanded enth Change(s) in daniel/pogo-land-rehearsal carry Pogo-Item=mg-9b5ff; 1 stack(s) vs main
      88cfeba83a82b0a44525da844f06e4f90bd9c55e157ed36bd6748e6be944e119
    The item is DONE and no land record names this work: it closed without it.
    -> enth --remote https://enth.app --repo daniel/pogo-land-rehearsal land 88cfeba8… --request-id mg-9b5ff/recover-1 > /tmp/mg-9b5ff-land.out && pogo land --item=mg-9b5ff --repo=/Users/daniel/dev/pogo-land-rehearsal --landed-file=/tmp/mg-9b5ff-land.out
  ```

  The 09:26Z note expected a `refinery submit --item` remedy. That expectation was
  superseded: after the 09:58Z re-scope and mg-35ae4, an enth repo has no merge
  queue, so the remedy printed is the self-land path (`enth land` then
  `pogo land --item`). That is correct for the code that is running.

## Mirror: NOT RUN

pm-enth (14:03Z): no mirror is configured for daniel/pogo-land-rehearsal, which is
enth-native with no GitHub copy. The mirror is rehearsed separately at mg-c51c0
step 3.

## Git control

`mr-dbku9litjv1m15691b1g` (`/Users/daniel/dev/pogo`, branch `polecat-pe2ab1`)
merged at 14:03:15Z as `a2c4b913…` through the same pogod. That is 1m19s after the
self-land, inside the same hour. The MR carrying this record is a second git
control.

## Teardown

- `pogo project remove ~/dev/pogo-land-rehearsal`: the scratch repo is
  **deregistered** from pogo, and its read checkout directory is removed. The
  positive control's unlanded Change stays on enth.app. Once the repo is
  deregistered it no longer appears in check-stranded.
- Left on enth.app for Daniel / pm-enth: the repo `daniel/pogo-land-rehearsal`
  (entries 1–6), bot `daniel-pogo` and its token `t58a0dcef63f8feb4` (expires
  2026-11-10). Nothing was deleted.

## Findings for mg-a2df4

1. Self-land works end to end on the live pogod: checkout, Change with trailers,
   land, land record, close. There were no slice defects.
2. An empty enth-written repo cannot be seeded with the enth client (enth epic 9
   §3.6 gap). This does not affect daniel/enth, which already has a trunk.
3. Per-project enth token: the run used Daniel's admin login (his ruling). mg-3a813 is tracked separately.
4. Changes carry the operator's git email through the identity carry-over.
