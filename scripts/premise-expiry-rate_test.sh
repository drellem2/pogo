#!/usr/bin/env bash
# Controls on scripts/premise-expiry-rate.sh — the instrument that prices
# architect's expired-premise mechanism (mg-027b).
#
# WHAT IS UNDER TEST IS NOT THE NUMBER. The live corpus produces whatever it
# produces and will keep moving. What is under test are the properties that
# decide whether the number can be believed:
#
#   1. THE PHASE SPLIT IS RIGHT. The whole finding turns on it. The motivating
#      instance — mg-24dc finishing 59 seconds BEFORE mg-0466 was claimed — must
#      land in `pre-dispatch`, not in `claimed`, because architect's proposal as
#      stated only watches `claimed` and would have missed the case that
#      motivated it. Section 2 replays that timeline verbatim, to the second.
#
#   2. A SETTLED PREMISE IS NOT A FIRE. A citation of an item that was already
#      done when the citing body was written cannot expire. If those counted,
#      every backward reference in the corpus would inflate the rate and the
#      recommendation would invert. Section 3 is that negative control.
#
#   3. THE COVERAGE LINE SURVIVES. Rates come from bodies still on disk and
#      archived bodies are swept. The first run of this analysis read 15% of the
#      corpus and reported a rate 20x too low. Section 4 hides a body and
#      asserts the UNDERSTATED warning appears — an instrument for a
#      stale-premise problem that can be read against a stale corpus without
#      saying so is the defect it was built to price.
#
#   4. IT DOES NOT WRITE TO THE STORE. Section 5 checksums the fixture before
#      and after. An analysis that edits its subject cannot be run twice.
#
# Every fixture store is built under a temp dir. The developer's live store is
# never touched: --root is passed explicitly on every invocation.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/premise-expiry-rate.sh"

# Route through the shared test isolation before anything that can reach $HOME
# (mg-457b/mg-78a5). This suite passes --root on every invocation and looks
# self-evidently unable to touch live state — which is the reasoning that put 48
# suites in the adoption ledger. It is wrong here for a specific reason: the
# script under test DEFAULTS to `$HOME/.macguffin`, so any future case that
# forgets --root reads the developer's live store, and the instrument's whole
# claim is that it only ever reads. Under isolation that mistake resolves to a
# private HOME with no store and exits 2, loudly.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create premiseexpiry
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

FAILURES=0
pass() { echo "  PASS: $*"; }
fail() {
	echo "  FAIL: $*"
	FAILURES=$((FAILURES + 1))
}

# Inside the private root, so pogo_sandbox_down removes it — there is one EXIT
# trap and the sandbox owns it.
TMP="$POGO_SANDBOX_DIR/fixtures"
mkdir -p "$TMP"

# ---------------------------------------------------------------------------
# Fixture. Four items, each pinning one behaviour:
#
#   mg-0001  cites mg-0002, which finishes 59s BEFORE mg-0001 is claimed.
#            The mg-0466/mg-24dc shape. Expect: pre-dispatch, in all four
#            populations (the body declares it live AND names a consequence).
#   mg-0003  cites mg-0004, which finishes WHILE mg-0003 is claimed.
#            Expect: claimed.
#   mg-0005  cites mg-0006, which was already done when mg-0005 was written.
#            Expect: no fire at all.
# ---------------------------------------------------------------------------
store() {
	local root="$1"
	mkdir -p "$root/work/available" "$root/work/done"
	cat >"$root/events.jsonl" <<'EOF'
{"type":"work.created","item_id":"mg-0002","ts":"2026-08-05T17:26:37Z","to_status":"available"}
{"type":"work.created","item_id":"mg-0001","ts":"2026-08-05T10:15:41Z","to_status":"available"}
{"type":"work.edited","item_id":"mg-0001","ts":"2026-08-06T22:45:08Z"}
{"type":"work.claim","item_id":"mg-0002","ts":"2026-08-06T16:32:20Z"}
{"type":"work.done","item_id":"mg-0002","ts":"2026-08-06T23:27:51Z"}
{"type":"work.claim","item_id":"mg-0001","ts":"2026-08-06T23:28:50Z"}
{"type":"work.done","item_id":"mg-0001","ts":"2026-08-07T00:06:01Z"}
{"type":"work.created","item_id":"mg-0004","ts":"2026-08-06T08:00:00Z"}
{"type":"work.created","item_id":"mg-0003","ts":"2026-08-06T08:00:00Z"}
{"type":"work.claim","item_id":"mg-0003","ts":"2026-08-06T09:00:00Z"}
{"type":"work.claim","item_id":"mg-0004","ts":"2026-08-06T09:05:00Z"}
{"type":"work.done","item_id":"mg-0004","ts":"2026-08-06T10:00:00Z"}
{"type":"work.done","item_id":"mg-0003","ts":"2026-08-06T11:00:00Z"}
{"type":"work.created","item_id":"mg-0006","ts":"2026-08-01T08:00:00Z"}
{"type":"work.done","item_id":"mg-0006","ts":"2026-08-01T09:00:00Z"}
{"type":"work.created","item_id":"mg-0005","ts":"2026-08-02T08:00:00Z"}
{"type":"work.claim","item_id":"mg-0005","ts":"2026-08-02T09:00:00Z"}
{"type":"work.done","item_id":"mg-0005","ts":"2026-08-02T10:00:00Z"}
EOF
	cat >"$root/work/done/mg-0001.md" <<'EOF'
---
id: mg-0001
---
RELATED WORK IN FLIGHT: mg-0002 is currently building the other half of this.
Read what it lands before you finalise, because if it succeeds the population
shrinks and this refusal's blast radius changes.
EOF
	cat >"$root/work/done/mg-0003.md" <<'EOF'
---
id: mg-0003
---
mg-0004 is in flight right now and its outcome changes the scope here.
EOF
	cat >"$root/work/done/mg-0005.md" <<'EOF'
---
id: mg-0005
---
Carries forward the ruling from mg-0006, which landed last week. Nothing here is
in flight; this is the settled version.
EOF
	# Bodies for the CITED items too, so coverage is 100% and section 4 can
	# lower it deliberately rather than starting there by accident.
	for id in mg-0002 mg-0004 mg-0006; do
		printf -- '---\nid: %s\n---\nbody\n' "$id" >"$root/work/done/$id.md"
	done
}

echo "=== 1. it runs and reports a corpus"
ROOT_A="$TMP/a"
store "$ROOT_A"
OUT="$("$SCRIPT" --root "$ROOT_A" 2>&1)"
RC=$?
if [ $RC -eq 0 ]; then pass "exit 0 on a readable store"; else fail "exit $RC: $OUT"; fi
echo "$OUT" | grep -q "coverage 100.0%" && pass "coverage is reported and complete" ||
	fail "expected 100% coverage, got: $(echo "$OUT" | grep coverage)"

echo "=== 2. the mg-0466/mg-24dc timeline lands in PRE-DISPATCH, not CLAIMED"
J="$("$SCRIPT" --root "$ROOT_A" --json --list)"
PHASE="$(echo "$J" | python3 -c '
import json,sys
d=json.load(sys.stdin)["populations"]
rows=[r for r in d["declared-live+consequence"]["rows"] if r["citer"]=="mg-0001"]
print(rows[0]["phase"] if rows else "ABSENT")')"
[ "$PHASE" = "pre-dispatch" ] && pass "mg-0001 -> mg-0002 classified pre-dispatch" ||
	fail "expected pre-dispatch for the 59-second case, got: $PHASE"
# ...and it is INVISIBLE to architect's proposal as stated. This is the finding.
SEEN="$(echo "$J" | python3 -c '
import json,sys
rows=json.load(sys.stdin)["populations"]["cites-any/claimed"]["rows"]
print(any(r["citer"]=="mg-0001" for r in rows))')"
[ "$SEEN" = "False" ] && pass "cites-any/claimed does NOT see it — the proposal's blind spot is pinned" ||
	fail "cites-any/claimed saw mg-0001, which would erase the finding"
# The genuinely-simultaneous pair must still be seen, or the negative above is
# only proving the population is empty.
SEEN3="$(echo "$J" | python3 -c '
import json,sys
rows=json.load(sys.stdin)["populations"]["cites-any/claimed"]["rows"]
print(any(r["citer"]=="mg-0003" for r in rows))')"
[ "$SEEN3" = "True" ] && pass "cites-any/claimed DOES see the simultaneous pair (positive control)" ||
	fail "cites-any/claimed missed mg-0003 -> mg-0004; the population may be dead"

echo "=== 3. a premise that was already settled is not a fire"
ANY="$(echo "$J" | python3 -c '
import json,sys
pops=json.load(sys.stdin)["populations"]
print(any(r["citer"]=="mg-0005" for p in pops.values() for r in p["rows"]))')"
[ "$ANY" = "False" ] && pass "mg-0005 -> mg-0006 counted in no population" ||
	fail "a backward citation was counted as an expiry"

echo "=== 4. a swept corpus says so"
ROOT_B="$TMP/b"
store "$ROOT_B"
rm "$ROOT_B/work/done/mg-0005.md" "$ROOT_B/work/done/mg-0006.md" "$ROOT_B/work/done/mg-0004.md"
OUT_B="$("$SCRIPT" --root "$ROOT_B" 2>&1)"
echo "$OUT_B" | grep -q "UNDERSTATED" && pass "understated-rate warning fires below 90% coverage" ||
	fail "no UNDERSTATED warning at partial coverage: $OUT_B"
SUFF="$("$SCRIPT" --root "$ROOT_B" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["coverage_sufficient"])')"
[ "$SUFF" = "False" ] && pass "--json carries coverage_sufficient=false" ||
	fail "json did not mark coverage insufficient"
# And the same field is true on the complete store, so the flag is not constant.
SUFF_A="$(echo "$J" | python3 -c 'import json,sys; print(json.load(sys.stdin)["coverage_sufficient"])')"
[ "$SUFF_A" = "True" ] && pass "coverage_sufficient=true on the complete store" ||
	fail "coverage_sufficient is stuck false"

echo "=== 5. it does not write to the store"
BEFORE="$(find "$ROOT_A" -type f -exec shasum {} \; | sort | shasum)"
"$SCRIPT" --root "$ROOT_A" >/dev/null 2>&1
AFTER="$(find "$ROOT_A" -type f -exec shasum {} \; | sort | shasum)"
[ "$BEFORE" = "$AFTER" ] && pass "store byte-identical after a run" ||
	fail "the instrument modified the store it measures"

echo "=== 6. a store with no event log refuses rather than reporting zero"
mkdir -p "$TMP/empty"
"$SCRIPT" --root "$TMP/empty" >/dev/null 2>&1
[ $? -eq 2 ] && pass "exit 2 on an unreadable store" || fail "expected exit 2 for a missing events.jsonl"

echo
if [ "$FAILURES" -eq 0 ]; then
	echo "premise-expiry-rate_test.sh: all controls passed"
	exit 0
fi
echo "premise-expiry-rate_test.sh: $FAILURES control(s) failed"
exit 1
