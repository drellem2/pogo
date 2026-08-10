- **Both dispatch notices consult the per-repo worker cap before naming a remedy
  (mg-dd77).** `stall-watch` and `priority-wake` told the coordinator to "claim or
  dispatch" items that `pogo agent spawn-polecat` would refuse on arrival, because
  neither knew the cap existed. They now group the items they are about by repo,
  read the same occupancy the spawn point enforces on, and say which of three
  situations it actually is.

  **THE FINDING WAS TRUE AND THE REMEDY WAS REFUSED.** On 2026-08-10 stall-watch
  mailed the mayor about **57 aging items, every one of them undispatchable**: all
  65 dispatchable items in the queue lived in two repos and both held 3 polecats
  against a cap of 3. The cap was confirmed as the binding constraint by a positive
  control rather than assumed — a real `spawn-polecat` for one of the named items
  was refused, cleanly and atomically, leaving the item `available`, the claim count
  unchanged and no agent behind. pogod's side was working correctly throughout.

  **TWO COMPONENTS HELD HALVES OF ONE FACT AND DID NOT CONSULT EACH OTHER.** The cap
  knew the fleet was saturated and said so in good prose, with the occupying workers
  named; stall-watch knew items were aging and did not know the cap existed. The
  result is a recurring alarm the recipient cannot act on — the failure mode where
  an operator learns to skim a channel, and this is the same channel that carries
  genuinely actionable dispatch news.

  **`priority-wake` HAD THE SAME DEFECT AND IS THE WORSE SURFACE**, so both are
  fixed. It fired twice for `mg-aab5` at cap; its "claim or dispatch **now**" is the
  most imperative wording the component emits, it lands on the items a coordinator
  is least willing to ignore, and its cooldown is the shortest — so the unactionable
  alarm on the highest-value work repeated the fastest. It also pushed toward a
  destructive remedy: at cap the only two ways to satisfy "dispatch now" are to
  preempt a working polecat (stranding its pushed branch, mg-be37) or to snooze the
  item (hiding ready high-priority work to silence a detector). The at-cap text now
  rules both out by name.

  **NOTHING GOES SILENT, AND THE TWO SITUATIONS BECOME COUNTABLE.** Every aging item
  is still in the message and still in `item_ids`; only the remedy changes. At cap
  the notice names the repo, the count, the cap and the occupying workers — which
  turns an instruction the coordinator must ignore into the question it can act on,
  *is one of these wedged?* `stall_watch_fired` gained `dispatchable_ids`,
  `at_cap_ids`, `at_cap_repos` and `occupancy_unknown_ids`, because at cap aging
  items are the EXPECTED steady state and carry no information about coordinator
  diligence, while below cap the identical message means work is being neglected —
  and before this they produced identical mail *and* identical events.

  **THE ADVICE CANNOT DRIFT FROM THE ENFORCEMENT.** `cmd/pogod` wires the probe to
  the same `agent.Registry.RepoOccupancyFor` the spawn point refuses on, and
  `AtCap` is *copied* from `WouldRefuse` rather than recomputed from count-vs-cap.
  A later refinement to the cap (a reserve, a grace slot) therefore changes the
  notice with it, instead of leaving a reimplemented comparison describing the old
  rule in perfectly confident prose. This is the same argument `/hostload` makes for
  serving the cap's own struct.

  **THE FIX IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so the ways it could
  repeat it are closed.** (1) *A confident remedy on missing information* is the
  defect's shape, so "cannot determine occupancy" is a third answer that says so
  rather than defaulting to "dispatch them" — and it is reachable in production: an
  unreadable witness with an empty registry is no information at all, because the
  in-memory registry is permanently empty after a restart (mg-13a3). An uncertain
  count (a bad witness read with live workers, or unattributed workers) still reads
  as dispatchable, following the cap's own fail-open direction, with the caveat
  carried alongside. (2) The at-cap text deliberately does **not** say "no action
  required, it will dispatch when a slot frees" — the wording the ticket itself
  proposed. Nothing in pogod auto-dispatches a work item; the coordinator does. The
  notice says LATER in the cap's own vocabulary instead of inventing a self-draining
  queue. (3) A "free slots" verdict is stated as the narrow claim it is — the
  host-load and stranded-push gates are not consulted, so it means *the cap would
  let this through*, not *this dispatch will succeed*.

  **Both polarities are proven, not just the quiet one.** Below cap the message is
  asserted byte-for-byte identical to the pre-fix sentence, as is the no-probe path,
  so this cannot degrade into a blanket softening of a detector that is right most
  of the time. Documented in
  [docs/design/stall-watch-design.md](../docs/design/stall-watch-design.md) and
  [docs/design/priority-wake-design.md](../docs/design/priority-wake-design.md).
