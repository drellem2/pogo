#!/usr/bin/env bash
# premise-expiry-rate.sh — how often does a work item's cited premise expire
# under it? (mg-027b)
#
# WHY THIS EXISTS. architect proposed a mechanism after mg-0466 and mg-24dc
# nearly shipped against each other's expired premises in the same hour: a
# ticket that cites another ticket's behaviour records the SHA it was priced
# against, and re-reading is triggered when that ticket MERGES. The proposal was
# explicitly a candidate, and mg-027b's instruction was to price it before
# building it — "twice in one hour on one night is not a rate."
#
# This is the instrument that supplies the rate. It reports how many notices
# each candidate trigger would have emitted over the corpus, so the answer to
# "is this worth building" is a measurement rather than a memory of one bad
# night. The finding it produced is docs/investigations/premise-expiry-2026-08-10.md;
# nothing in that document is re-derivable by reading it, which is why this file
# is tracked beside it.
#
# WHAT IT MEASURES. Four candidate triggers over the same corpus:
#
#   cites-any/claimed        architect's proposal AS STATED. Item A's body names
#                            item B anywhere; B finishes while A is claimed.
#   cites-any/pre-dispatch   B finishes between A's last pre-claim edit (when A
#                            was priced) and A's claim. This is the window the
#                            motivating instance actually landed in.
#   declared-live            A's text around the citation ASSERTS B is live
#                            ("in flight", "unmerged", "queued", "lands"); B
#                            finishes at any point during A's life.
#   declared-live+consequence  ...and the same text names a consequence ("blast
#                            radius", "no longer", "if it lands", "prerequisite"),
#                            and is not a "do not duplicate" inventory block.
#
# `work.done` IS A PROXY FOR MERGE, AND IT LAGS. mg's event log records no merge
# event, so an item's `work.done` stands in for the moment the world changed. On
# the motivating night that proxy was 36 minutes late (mg-24dc merged 22:46:22Z
# and 22:51:31Z; its item closed 23:27:51Z). The bias is directional and worth
# stating rather than hiding: a LATE proxy can only move a fire out of
# `pre-dispatch` and into `claimed`, never the reverse — so the pre-dispatch
# share this prints is a floor.
#
# THE COVERAGE LINE IS NOT DECORATION — READ IT FIRST. Rates are computed from
# bodies still on disk, and archived items are swept. The first run of this
# analysis saw 15% of the corpus and reported a rate 20x too low, because a
# partial corpus produces a small number and a small number looks like an
# answer. So coverage is printed above the rates and every rate is marked
# UNDERSTATED below 90%. An instrument for a stale-premise problem that can
# itself be read against a stale corpus is the defect it was built to price.
#
# Usage:
#   scripts/premise-expiry-rate.sh                 human summary
#   scripts/premise-expiry-rate.sh --json          machine-readable
#   scripts/premise-expiry-rate.sh --list          also print every fire
#   scripts/premise-expiry-rate.sh --root DIR      read another mg store
#
# Environment:
#   MG_ROOT    the mg store to read; default $HOME/.macguffin. --root wins.
#
# It READS the store and writes nothing to it, deliberately: it is an analysis
# of the record, and an instrument that edits its subject cannot be run twice.
#
# Exit status: 0 on a completed measurement, 1 on usage error, 2 if the store
# has no events log to read (a measurement that could not run has not found a
# rate of zero).

set -uo pipefail

ROOT="${MG_ROOT:-$HOME/.macguffin}"
FORMAT=human
LIST=0

while [ $# -gt 0 ]; do
	case "$1" in
	--json) FORMAT=json ;;
	--list) LIST=1 ;;
	--root)
		shift
		[ $# -gt 0 ] || {
			echo "premise-expiry-rate.sh: --root needs a directory" >&2
			exit 1
		}
		ROOT="$1"
		;;
	--root=*) ROOT="${1#--root=}" ;;
	-h | --help)
		# The header comment IS the help text. Printed by shape (every leading
		# `#` line after the shebang) rather than by line number, so editing the
		# preamble cannot silently truncate what --help shows.
		awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"
		exit 0
		;;
	*)
		echo "premise-expiry-rate.sh: unknown argument $1" >&2
		exit 1
		;;
	esac
	shift
done

if [ ! -f "$ROOT/events.jsonl" ]; then
	echo "premise-expiry-rate.sh: no events log at $ROOT/events.jsonl" >&2
	exit 2
fi

MG_ANALYSIS_ROOT="$ROOT" MG_ANALYSIS_FORMAT="$FORMAT" MG_ANALYSIS_LIST="$LIST" \
	python3 - <<'PY'
import collections, datetime, glob, json, os, re, sys

ROOT = os.environ["MG_ANALYSIS_ROOT"]
FORMAT = os.environ["MG_ANALYSIS_FORMAT"]
LIST = os.environ["MG_ANALYSIS_LIST"] == "1"

ITEM = re.compile(r"\bmg-[0-9a-f]{4}\b")

# A citation is "declared live" when the text around it says the cited item has
# not landed yet. These are the phrases the corpus actually uses; they are
# author prose, not a field anyone maintains, which is the point — the premise
# in the motivating instance was written as "RELATED WORK IN FLIGHT: mg-24dc is
# currently building the other half of this".
LIVE = re.compile(
    r"(in flight|in-flight|currently building|currently open|is currently|unmerged"
    r"|not yet merged|not yet landed|still open|queued|in review|being built"
    r"|lands|landing|will land|about to merge|merges|in parallel)", re.I)
# ...and "consequence" is the citing item saying what changes if it does land.
CONSEQ = re.compile(
    r"(blast radius|shrink|changes? the|no longer|invalidat|moot|re-?read|re-?price"
    r"|prerequisite|softens?|if it (succeeds|lands|merges)|once it (lands|merges)"
    r"|then this|depends on the outcome|scope changes|supersede)", re.I)
# An inventory of live work ("do not duplicate: mg-a, mg-b") is declared-live by
# construction and its expiry is the DESIRED outcome, not a rotted premise.
INVENTORY = re.compile(r"(do not duplicate|already claimed and in flight|already in flight)", re.I)


def parse_ts(s):
    try:
        return datetime.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ")
    except Exception:
        return None


created, claimed, finished = {}, {}, {}
edits = collections.defaultdict(list)
claim_days = set()
with open(os.path.join(ROOT, "events.jsonl"), errors="replace") as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except Exception:
            continue
        item, when = ev.get("item_id"), parse_ts(ev.get("ts", ""))
        if not item or when is None:
            continue
        kind = ev.get("type")
        if kind == "work.created":
            created.setdefault(item, when)
        elif kind == "work.claim":
            claimed.setdefault(item, when)
            claim_days.add(when.date())
        elif kind == "work.done":
            finished[item] = when
        elif kind == "work.edited":
            edits[item].append(when)

bodies = {}
for path in glob.glob(os.path.join(ROOT, "work", "**", "mg-*.md"), recursive=True):
    if ".bodybak" in path:
        continue
    bodies.setdefault(os.path.basename(path)[:-3], open(path, errors="replace").read())

known = set(created)
coverage = (100.0 * len(known & set(bodies)) / len(known)) if known else 0.0
days = len(claim_days) or 1
now = datetime.datetime.now(datetime.timezone.utc).replace(tzinfo=None)

pops = {name: [] for name in
        ("cites-any/claimed", "cites-any/pre-dispatch", "declared-live", "declared-live+consequence")}

for citer, body in bodies.items():
    born = created.get(citer)
    if born is None:
        continue
    start, end = claimed.get(citer), finished.get(citer)
    # When the item was last priced: its creation, or the last edit made before
    # a worker picked it up. An edit AFTER the claim is the worker's own note.
    priced = max([born] + [e for e in edits[citer] if start is None or e <= start])
    for cited in set(ITEM.findall(body)) - {citer}:
        gone = finished.get(cited)
        if gone is None or gone <= born or gone > (end or now):
            continue
        context = " ".join(
            body[max(0, m.start() - 250):m.end() + 250].replace("\n", " ")
            for m in re.finditer(re.escape(cited), body))
        phase = "claimed" if (start and gone > start) else "pre-dispatch"
        row = {"citer": citer, "cited": cited, "expired": gone.strftime("%Y-%m-%dT%H:%M:%SZ"),
               "phase": phase}
        if phase == "claimed":
            pops["cites-any/claimed"].append(row)
        elif start and gone > priced:
            pops["cites-any/pre-dispatch"].append(row)
        if LIVE.search(context):
            pops["declared-live"].append(row)
            if CONSEQ.search(context) and not INVENTORY.search(context):
                pops["declared-live+consequence"].append(row)

report = {
    "root": ROOT,
    "items_in_events": len(known),
    "bodies_on_disk": len(bodies),
    "body_coverage_pct": round(coverage, 1),
    "coverage_sufficient": coverage >= 90.0,
    "active_days": days,
    "populations": {
        name: {
            "fires": len(rows),
            "per_active_day": round(len(rows) / days, 2),
            "phase": dict(collections.Counter(r["phase"] for r in rows)),
            **({"rows": sorted(rows, key=lambda r: r["expired"])} if LIST else {}),
        }
        for name, rows in pops.items()
    },
}

if FORMAT == "json":
    print(json.dumps(report, indent=2, sort_keys=True))
    sys.exit(0)

print("store:    %s" % ROOT)
print("corpus:   %d items in the event log, %d bodies on disk — coverage %.1f%%"
      % (len(known), len(bodies), coverage))
if coverage < 90.0:
    print("          COVERAGE BELOW 90%% — every rate below is UNDERSTATED.")
    print("          Bodies are swept on archive; a partial corpus yields a small")
    print("          number, and a small number reads like an answer.")
print("window:   %d days on which at least one item was claimed" % days)
print()
print("%-28s %7s %9s   %s" % ("trigger", "fires", "per day", "phase split"))
for name, rows in pops.items():
    split = collections.Counter(r["phase"] for r in rows)
    print("%-28s %7d %9.2f   %s"
          % (name, len(rows), len(rows) / days,
             ", ".join("%s=%d" % kv for kv in sorted(split.items())) or "-"))
if LIST:
    for name, rows in pops.items():
        print("\n-- %s" % name)
        for r in sorted(rows, key=lambda r: r["expired"]):
            print("   %s  %s cites %s [%s]" % (r["expired"], r["citer"], r["cited"], r["phase"]))
PY
