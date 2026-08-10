package ackwatch

// Population split: WHICH MECHANISM produced an ack deficit.
//
// # Why this exists at all
//
// The detector in ackwatch.go judges a completion RATIO. Three separate
// mechanisms can push that ratio down, and a fix aimed at the wrong one leaves
// the number stubbornly non-zero — which is how a metric becomes permanently
// distrusted. mg-ddf7 required the populations to be counted SEPARATELY, and
// reported, BEFORE any fix was chosen:
//
//	1. batched      a fire's token superseded by the next before anything
//	                redeemed it, so only one of the run is redeemable however
//	                diligent the agent is. NOTE what this name does and does not
//	                claim — see "What 'batched' does not explain" below.
//	2. token-less   a fire delivered carrying no token at all. Nothing to ack,
//	                so the deficit is unclosable BY THE AGENT.
//	3. boundary     a fire outstanding at the moment of measurement. Not a
//	                property of the agent at all — it is a property of when you
//	                looked.
//
// The persisted counters on scheduler.Entry cannot answer this. They are three
// running totals, and they are ZEROED whenever a schedule re-registers (see
// ackwatch.go, "Counters reset on re-registration") — which the nightly
// redeploy guarantees. So a deficit accumulated during a storm is erased by the
// restart that follows it, and the live table always reads calm. The events log
// is the only place the storm survives, and it carries the fire token on both
// scheduler_fire_delivered and scheduler_fire_completed, which is exactly
// enough to reconstruct the interleaving. Hence: this file reads events, not
// counters.
//
// # What the measurement found (2026-07-30, mg-ddf7)
//
// Over the whole token era on this fleet — 4370 delivered, 3471 acked, a
// deficit of 899 — the split was:
//
//	batched     814   90.5%
//	token-less    0    0.0%
//	boundary     85    9.5%
//
// and the deficit is EXHAUSTIVELY explained by those two surviving mechanisms.
// Not approximately: the identity
//
//	completed/delivered  ==  1/mean_gap  -  outstanding/delivered
//
// holds to zero error across all 114 schedules in the log (see Identity, and
// the assertion in populations_test.go). There is no residual term. Whatever
// else the completion ratio might be thought to measure, it is arithmetically
// the reciprocal of the mean attention gap, corrected for where you stopped
// looking — and nothing else.
//
// That is what "the metric measures DELIVERY, not diligence" means once it is
// measured rather than argued. See docs/investigations/ack-deficit-populations-2026-07-30.md
// for the full record, including the storm-vs-calm comparison and the two
// repairs it rules out.
//
// # What "batched" does not explain (mg-772f)
//
// Population 1 is defined by TOKEN SUPERSESSION — a fire's token replaced
// before anything redeemed it. That is a fact about the token, and it was the
// only thing the original split measured. The NAME "batched" additionally
// suggests a cause: several fires arriving inside one long agent turn. That
// cause was never measured, and when it was, it turned out to be half the
// story at best.
//
// mg-772f measured the fleet's whole token era, 16,081 token-carrying
// deliveries and 7,106 superseded ones, and split them two ways:
//
//   - By DELIVERY PUNCTUALITY. 99.8% of superseded fires were delivered within
//     60s of their due time — 100.0% on 14 of the 16 measured days. pogod does
//     not hold fires and flush them together. Whatever bunches them is
//     downstream of delivery, on every day measured. This is the discriminator
//     the events log always carried and nothing read: `original_due` and
//     `fired_at` were written by the scheduler from the start and had no
//     consumer until LateDelivery below.
//
//   - By whether a synthetic-failure EPISODE was open for that agent at the
//     time (internal/synthwatch). 51.5% of superseded fires landed while
//     synthwatch had already detected that the agent's turns were dying —
//     API errors, expired credentials, DNS failures. Those fires were not
//     batched by a busy agent. They were delivered on time into turns that
//     never ran, so nothing acked them and the next fire duly superseded them.
//
// The worked example is the one that prompted the ticket. On 2026-08-09,
// architect and pm-pogo each received 27 fires between 13:01Z and 17:20Z and
// acked none, and both reported the 27 as having "arrived in a batch". They had
// not. Each was delivered on its own 10-minute mark, `fired_at` within 30s of
// `original_due` every time, and each was accepted by the harness on time —
// the session transcripts timestamp all 27 individually. 26 of the 27 turns
// then died on `API Error: Unable to connect to API (ENOTFOUND)` about three
// minutes in. synthwatch detected it at 13:04:48Z and cleared it at 17:22:29Z,
// bracketing the window in the same events.log. The 27th fire was the first
// turn to reach the API, and it saw all 27 prompts sitting unanswered in its
// own context — which is what "arrived in a batch" actually described. The
// batching was in the READING, not in the delivery and not in the scheduler.
//
// So BatchedInFailureEpisode exists to stop that reading being available. A deficit
// that coincides with a known-dead consuming turn is not evidence about the
// agent's attention, and must not be spent arguing about token lifetimes. See
// docs/investigations/scheduler-fire-batching-2026-08-10.md.
//
// Neither new counter changes the deficit arithmetic or the Identity above.
// They are strictly additive annotations on population 1: the ratio, the mean
// gap and the residual all read exactly as they did before. Re-scoring the
// deficit is mg-a14c's question and this ticket deliberately does not answer
// it — it only makes the cause visible enough to be argued about with numbers.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// FireEventKind distinguishes the two scheduler events the split reads.
type FireEventKind string

const (
	// FireDelivered is scheduler_fire_delivered.
	FireDelivered FireEventKind = "delivered"
	// FireCompleted is scheduler_fire_completed.
	FireCompleted FireEventKind = "completed"
)

// FireEvent is one delivery or completion, reduced to the four fields the split
// needs. As with Sample, it is a plain struct with no events or scheduler
// dependency so tests build timelines by hand rather than reading the
// developer's live ~/.pogo — see the note at the top of source.go.
type FireEvent struct {
	At    time.Time     `json:"at"`
	Kind  FireEventKind `json:"kind"`
	Agent string        `json:"agent"`
	ID    string        `json:"id"`
	// Token is the fire's nonce. Empty on a delivery means the fire carried no
	// token — population 2. On a completion it is the token being redeemed.
	Token string `json:"token"`

	// Due and Fired are the scheduler's own `original_due` and `fired_at`,
	// carried on every scheduler_fire_delivered since the scheduler first
	// emitted one and read by nothing until mg-772f. Their DIFFERENCE is the
	// only direct evidence of which side a bunching came from: a fire pogod
	// held and flushed late has Fired well after Due, and one it delivered
	// punctually cannot have been bunched by the scheduler at all.
	//
	// Both are zero on a completion, and on a delivery whose event predates the
	// fields or whose timestamps did not parse. LateDelivery therefore counts
	// only what it positively measured — a missing pair is not a late fire.
	Due   time.Time `json:"due,omitempty"`
	Fired time.Time `json:"fired,omitempty"`
}

// LateDeliveryThreshold is how far Fired may trail Due before the fire counts
// as held on the delivery side.
//
// 60s is chosen to be unambiguous rather than tight. Measured over 65,717
// deliveries on this fleet the lag is p50 14s, p90 27s — the scheduler's tick
// granularity plus the confirm handshake — while the shortest cadence any
// schedule runs is 5 minutes. So 60s sits an order of magnitude above the
// normal jitter and an order of magnitude below one cadence period, and no
// plausible re-tuning of either moves a fire across it.
const LateDeliveryThreshold = 60 * time.Second

// Late reports whether this delivery was held past LateDeliveryThreshold. False
// when either timestamp is absent: see Due/Fired.
func (e FireEvent) Late() bool {
	if e.Due.IsZero() || e.Fired.IsZero() {
		return false
	}
	return e.Fired.Sub(e.Due) > LateDeliveryThreshold
}

// FailureEpisode is one agent's synthetic-failure episode: the interval over which
// synthwatch had detected that this agent's turns were failing without the
// agent exiting, crashing or wedging (see internal/synthwatch).
//
// Until is zero for an episode still open at the end of the window, which reads
// as "open-ended" — the right direction to fail in, because an episode whose
// close was never logged is far more likely to have continued than to have
// silently ended at the last event we happened to read.
type FailureEpisode struct {
	Agent string    `json:"agent"`
	From  time.Time `json:"from"`
	Until time.Time `json:"until,omitempty"`
}

// Covers reports whether at lies inside the episode.
func (b FailureEpisode) Covers(at time.Time) bool {
	if at.Before(b.From) {
		return false
	}
	return b.Until.IsZero() || !at.After(b.Until)
}

// episodeIndex answers "was agent A dark at time T" without rescanning a slice
// per fire.
type episodeIndex map[string][]FailureEpisode

func newEpisodeIndex(bs []FailureEpisode) episodeIndex {
	if len(bs) == 0 {
		return nil
	}
	idx := episodeIndex{}
	for _, b := range bs {
		idx[b.Agent] = append(idx[b.Agent], b)
	}
	return idx
}

func (i episodeIndex) dark(agent string, at time.Time) bool {
	for _, b := range i[agent] {
		if b.Covers(at) {
			return true
		}
	}
	return false
}

// SchedulePopulation is one schedule's deficit, split by mechanism.
type SchedulePopulation struct {
	Agent string `json:"agent"`
	ID    string `json:"id"`

	// Delivered and Completed are counted within the measured window, from
	// events — NOT read from the persisted counters, which a re-registration
	// would have zeroed.
	Delivered int `json:"delivered"`
	Completed int `json:"completed"`

	// Batched is population 1: deliveries whose token was replaced by a later
	// fire's before anything redeemed it.
	Batched int `json:"batched"`
	// BatchedInFailureEpisode is the subset of Batched delivered while synthwatch had
	// an open synthetic-failure episode for this agent — a fire that landed on
	// a turn already known to be dying. It is a SUBSET, not a fourth
	// population: it does not enter the deficit arithmetic, and subtracting it
	// from Batched would double-count.
	//
	// Zero when the caller passed no episodes, which is indistinguishable from
	// "no episodes in the window". That ambiguity is why Render only mentions
	// the counter when it is non-zero: a silent 0 must not read as an acquittal.
	BatchedInFailureEpisode int `json:"batched_in_failure_episode"`
	// LateDelivery counts token-carrying deliveries the scheduler itself held
	// past LateDeliveryThreshold. This is the delivery-side arm: a non-zero
	// count here is the only thing in this report that would implicate pogod
	// rather than what happens to a fire after it lands.
	LateDelivery int `json:"late_delivery"`
	// DeliveryMeasured counts token-carrying deliveries that carried BOTH
	// stamps, i.e. the denominator LateDelivery is a count out of.
	//
	// It exists because LateDelivery == 0 has two readings — "every fire was
	// punctual" and "no fire was measured" — and this ticket is about a
	// measurement whose zero was read as health. A log predating the stamps, or
	// one whose stamps did not parse, produces a delivery-side count of zero
	// that would otherwise render as an exoneration nothing looked hard enough
	// to earn. Whoever reads the acquittal is entitled to see the denominator
	// under it.
	DeliveryMeasured int `json:"delivery_measured"`
	// TokenLess is population 2: deliveries carrying no token.
	TokenLess int `json:"token_less"`
	// Boundary is population 3: 1 if a token was still outstanding when the
	// window ended, 0 otherwise. It is bounded at ONE PER SCHEDULE, which is
	// what makes it negligible for a long-lived schedule and dominant for a
	// short-lived one — the distortion is 1/Delivered, not a constant.
	Boundary int `json:"boundary"`

	// OrphanAcks are completions whose delivery fell outside the window (or
	// before a pogod restart). They are counted and then excluded from the
	// arithmetic: crediting an ack whose delivery we never saw would inflate
	// the ratio above 1 at a window edge.
	OrphanAcks int `json:"orphan_acks"`

	// MeanGap is the mean attention gap in FIRES: deliveries per ack cycle.
	// 1.0 is an agent that acks every fire before the next arrives; 2.27 is one
	// that acks once per 2.27 fires. Zero when nothing was measurable.
	MeanGap float64 `json:"mean_gap"`
	// MaxGap is the longest unbroken run of unacked token-carrying deliveries.
	// This is the statistic that does NOT saturate: a busy-but-healthy agent's
	// run is bounded by its own turn length, a wedged one's climbs without
	// bound (202, for the mayor, during the 2026-07-22 outage).
	MaxGap int `json:"max_gap"`
}

// Rate is Completed/Delivered over the window — the same quantity Sample.Rate
// computes from the persisted counters, recomputed here from events so the two
// can be compared.
func (s SchedulePopulation) Rate() float64 {
	if s.Delivered <= 0 {
		return 0
	}
	return float64(s.Completed) / float64(s.Delivered)
}

// Identity returns the residual of
//
//	Rate  ==  1/MeanGap  -  Boundary/Delivered
//
// It is zero, always, and not as an empirical near-miss: sum(runs) is by
// construction the count of token-carrying deliveries and len(runs) is the
// count of acks plus the boundary term, so the equality is algebra. It is
// exposed — and asserted in the tests — because it is the whole argument. A
// future edit that makes the ratio mean something OTHER than an inverted
// attention gap will show up here as a non-zero residual, and anyone reading
// this package deserves to find that out from a failing test rather than from a
// mail nobody trusts.
func (s SchedulePopulation) Identity() float64 {
	if s.Delivered <= 0 || s.MeanGap <= 0 {
		return 0
	}
	return s.Rate() - (1/s.MeanGap - float64(s.Boundary)/float64(s.Delivered))
}

// PopulationReport is the whole fleet's deficit, split by mechanism.
type PopulationReport struct {
	// From and To bound the measured window. Reported because population 3 is
	// literally an artefact of where To falls.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	Schedules []SchedulePopulation `json:"schedules"`

	Delivered int `json:"delivered"`
	Completed int `json:"completed"`
	Batched   int `json:"batched"`
	TokenLess int `json:"token_less"`
	Boundary  int `json:"boundary"`

	// BatchedInFailureEpisode and LateDelivery are the mg-772f arms — a subset of
	// Batched, and a delivery-side count, respectively. Neither is a
	// population: see the fields of the same name on SchedulePopulation.
	BatchedInFailureEpisode int `json:"batched_in_failure_episode"`
	LateDelivery            int `json:"late_delivery"`
	DeliveryMeasured        int `json:"delivery_measured"`

	// FirstTokenCarrying and LastTokenLess let a reader tell a pre-feature ERA
	// from an ongoing MECHANISM without knowing when mg-a754 shipped. If every
	// token-less fire predates every token-carrying one, population 2 is
	// history and no fix addresses it. On this fleet that is exactly the case:
	// the last token-less delivery was 2026-07-23T18:40:34Z and the first
	// token-carrying one 2026-07-23T18:50:23Z, ten minutes — one cadence period
	// — apart.
	FirstTokenCarrying time.Time `json:"first_token_carrying,omitempty"`
	LastTokenLess      time.Time `json:"last_token_less,omitempty"`
}

// Deficit is Delivered-Completed: the number the alert is read off.
func (r PopulationReport) Deficit() int { return r.Delivered - r.Completed }

// Rate is the fleet's completion ratio over the window.
func (r PopulationReport) Rate() float64 {
	if r.Delivered <= 0 {
		return 0
	}
	return float64(r.Completed) / float64(r.Delivered)
}

// TokenLessIsHistorical reports whether every token-less delivery in the window
// precedes every token-carrying one — i.e. population 2 is a pre-mg-a754
// artefact rather than a live mechanism. False when there were no token-less
// fires at all, since there is then nothing to characterise.
func (r PopulationReport) TokenLessIsHistorical() bool {
	if r.TokenLess == 0 || r.FirstTokenCarrying.IsZero() {
		return false
	}
	return r.LastTokenLess.Before(r.FirstTokenCarrying)
}

// SplitPopulations classifies a timeline of deliveries and completions.
//
// It is pure: same events in, same report out, no clock and no filesystem. The
// window is taken from the events themselves rather than passed in, so a caller
// cannot accidentally claim a wider window than it actually read.
//
// Equivalent to SplitWithEpisodes with none: BatchedInFailureEpisode comes back 0,
// which is why Render will not print an acquittal off it.
func SplitPopulations(evs []FireEvent) PopulationReport {
	return SplitWithEpisodes(evs, nil)
}

// SplitWithEpisodes is SplitPopulations with synthwatch's episodes joined in,
// so population 1 can be split into fires that superseded each other while the
// agent was WORKING and fires that superseded each other while its turns were
// already known to be DYING (mg-772f).
//
// The join is on agent name and time only. Both event families name the agent
// the same way — scheduler_fire_delivered's `to` and synthwatch's
// details.target are both the bare name — so no normalisation is needed; a
// mismatch would show up as BatchedInFailureEpisode stuck at 0, which is the safe
// direction because it under-claims rather than manufacturing an excuse.
func SplitWithEpisodes(evs []FireEvent, episodes []FailureEpisode) PopulationReport {
	dark := newEpisodeIndex(episodes)
	byKey := map[string][]FireEvent{}
	keys := []string{}
	rep := PopulationReport{}

	ordered := make([]FireEvent, len(evs))
	copy(ordered, evs)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })

	for _, ev := range ordered {
		if rep.From.IsZero() || ev.At.Before(rep.From) {
			rep.From = ev.At
		}
		if ev.At.After(rep.To) {
			rep.To = ev.At
		}
		if ev.Kind == FireDelivered {
			if ev.Token == "" {
				if rep.LastTokenLess.IsZero() || ev.At.After(rep.LastTokenLess) {
					rep.LastTokenLess = ev.At
				}
			} else if rep.FirstTokenCarrying.IsZero() || ev.At.Before(rep.FirstTokenCarrying) {
				rep.FirstTokenCarrying = ev.At
			}
		}
		k := ev.Agent + "\x00" + ev.ID
		if _, seen := byKey[k]; !seen {
			keys = append(keys, k)
		}
		byKey[k] = append(byKey[k], ev)
	}
	sort.Strings(keys)

	for _, k := range keys {
		sp := classifyTimeline(byKey[k], dark)
		if sp.Delivered == 0 && sp.Completed == 0 {
			continue
		}
		rep.Schedules = append(rep.Schedules, sp)
		rep.Delivered += sp.Delivered
		rep.Completed += sp.Completed
		rep.Batched += sp.Batched
		rep.TokenLess += sp.TokenLess
		rep.Boundary += sp.Boundary
		rep.BatchedInFailureEpisode += sp.BatchedInFailureEpisode
		rep.LateDelivery += sp.LateDelivery
		rep.DeliveryMeasured += sp.DeliveryMeasured
	}
	return rep
}

// classifyTimeline walks one schedule's events in order. evs must be sorted.
// dark may be nil, meaning no episodes were supplied.
func classifyTimeline(evs []FireEvent, dark episodeIndex) SchedulePopulation {
	sp := SchedulePopulation{}
	if len(evs) > 0 {
		sp.Agent, sp.ID = evs[0].Agent, evs[0].ID
	}

	outstanding := false // is a token currently redeemable?
	run := 0             // token-carrying deliveries since the last ack
	var runs []int

	for _, ev := range evs {
		switch ev.Kind {
		case FireDelivered:
			sp.Delivered++
			if ev.Token == "" {
				// Nothing to ack. Deliberately does not affect the run
				// structure: a fire that could never be acked must not be
				// blamed on the agent's attention, and must not make the next
				// genuine fire look superseded.
				sp.TokenLess++
				continue
			}
			if !ev.Due.IsZero() && !ev.Fired.IsZero() {
				sp.DeliveryMeasured++
			}
			if ev.Late() {
				// The scheduler itself held this one. Counted whether or not it
				// went on to be superseded: a late fire is a delivery-side fact
				// and does not need an ack outcome to be true.
				sp.LateDelivery++
			}
			if outstanding {
				// This token replaces one nothing redeemed. Population 1.
				sp.Batched++
				if dark.dark(ev.Agent, ev.At) {
					// ...but the agent's turns were already failing when it
					// landed, so this supersession says nothing about attention.
					sp.BatchedInFailureEpisode++
				}
			}
			outstanding = true
			run++
			if run > sp.MaxGap {
				sp.MaxGap = run
			}
		case FireCompleted:
			if !outstanding {
				// The delivery this redeems is outside the window. Counted,
				// then excluded — see SchedulePopulation.OrphanAcks.
				sp.OrphanAcks++
				continue
			}
			// Credited without comparing tokens. The scheduler only emits
			// scheduler_fire_completed for a token that matched the
			// outstanding fire (Ack rejects everything else with
			// ErrStaleToken), so a mismatch here can only mean the matching
			// delivery fell outside the window. The agent did the work either
			// way, and refusing the credit would move a window-edge artefact
			// into the numerator, where it would read as negligence.
			sp.Completed++
			runs = append(runs, run)
			run = 0
			outstanding = false
		}
	}

	if outstanding {
		// A fire still redeemable when the window ended. Population 3, and
		// bounded at one: the agent has not failed to ack it, it has not yet
		// had the chance.
		sp.Boundary = 1
		runs = append(runs, run)
	}

	if len(runs) > 0 {
		total := 0
		for _, r := range runs {
			total += r
		}
		sp.MeanGap = float64(total) / float64(len(runs))
	}
	return sp
}

// Render produces the human-readable population split.
func (r PopulationReport) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "ack deficit population split — %s .. %s\n",
		stamp(r.From), stamp(r.To))

	if r.Delivered == 0 {
		b.WriteString("No fires in the window. Nothing measured — which is not the same as nothing wrong.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%d delivered, %d completed (%s). Deficit %d.\n\n",
		r.Delivered, r.Completed, pct(r.Rate()), r.Deficit())

	if r.Deficit() > 0 {
		d := float64(r.Deficit())
		b.WriteString("MECHANISM, not diligence:\n")
		// Deliberately says what was MEASURED (a token replaced before anything
		// redeemed it) and not why. The old wording here — "several fires
		// inside one turn" — named a cause nothing in this report had checked,
		// and it was wrong for 51.5% of the fleet's fires. The "WHICH SIDE"
		// block below is where cause is allowed to be claimed, because that is
		// the only part that measures one.
		fmt.Fprintf(&b, "  1. batched     %6d  %5.1f%%  superseded before redemption; one redeemable token\n",
			r.Batched, float64(r.Batched)/d*100)
		fmt.Fprintf(&b, "  2. token-less  %6d  %5.1f%%  no token delivered — unclosable BY THE AGENT\n",
			r.TokenLess, float64(r.TokenLess)/d*100)
		fmt.Fprintf(&b, "  3. boundary    %6d  %5.1f%%  outstanding when we looked — not an agent property\n",
			r.Boundary, float64(r.Boundary)/d*100)
		if resid := r.Deficit() - r.Batched - r.TokenLess - r.Boundary; resid != 0 {
			fmt.Fprintf(&b, "     unattributed %5d          <-- INVESTIGATE: the split should be exhaustive\n", resid)
		}
		b.WriteString("\n")
	}

	if r.Batched > 0 {
		b.WriteString("WHICH SIDE population 1 came from (mg-772f):\n")
		fmt.Fprintf(&b, "  delivery-side  %6d  %5.1f%%  fires pogod held >%s past due\n",
			r.LateDelivery, float64(r.LateDelivery)/float64(r.Batched)*100, LateDeliveryThreshold)
		// Reported only when non-zero. A blank line here means either no
		// episodes overlapped the fires or the caller supplied none, and those
		// two are not distinguishable from inside this function — printing
		// "0 in blackout" would read as an acquittal the data cannot support.
		if r.BatchedInFailureEpisode > 0 {
			fmt.Fprintf(&b, "  dead turns     %6d  %5.1f%%  landed while synthwatch had this agent FAILING\n",
				r.BatchedInFailureEpisode, float64(r.BatchedInFailureEpisode)/float64(r.Batched)*100)
			b.WriteString("     Those fires were delivered on time into turns that never ran. They are\n")
			b.WriteString("     not evidence about the agent's attention, and a token-lifetime change\n")
			b.WriteString("     would not recover one of them.\n")
		}
		switch {
		case r.DeliveryMeasured == 0:
			// The acquittal-from-absence this ticket exists to name. A window
			// whose fires carry no due/fired stamps produces LateDelivery == 0
			// for the same reason a dead fleet produced a clean nudge_sent
			// count: nothing looked.
			b.WriteString("     Delivery is UNMEASURED for this window, NOT exonerated: no fire\n")
			b.WriteString("     carried both original_due and fired_at, so the 0 above is an\n")
			b.WriteString("     absence of evidence and must not be read as evidence of absence.\n")
		case r.LateDelivery == 0:
			fmt.Fprintf(&b, "     Delivery is EXONERATED for this window: all %d fires whose punctuality\n",
				r.DeliveryMeasured)
			b.WriteString("     could be measured were on time, so whatever bunched them is\n")
			b.WriteString("     downstream of pogod.\n")
		}
		b.WriteString("\n")
	}

	if r.TokenLess > 0 {
		if r.TokenLessIsHistorical() {
			fmt.Fprintf(&b, "Population 2 is HISTORICAL, not a live mechanism: the last token-less fire\n"+
				"(%s) predates the first token-carrying one (%s). Those fires\n"+
				"were delivered before completion tokens existed (mg-a754). No fix addresses them.\n\n",
				stamp(r.LastTokenLess), stamp(r.FirstTokenCarrying))
		} else {
			fmt.Fprintf(&b, "Population 2 is LIVE: token-less fires continue past the first token-carrying\n"+
				"one (last token-less %s). A fire nothing can ack is a defect in the\n"+
				"scheduler, not in the agent — a token-lifetime change would not touch it.\n\n",
				stamp(r.LastTokenLess))
		}
	}

	fmt.Fprintf(&b, "%-38s %6s %6s %6s %8s %7s %7s\n",
		"agent/schedule", "deliv", "ack", "rate", "meanGap", "maxGap", "batched")
	rows := make([]SchedulePopulation, len(r.Schedules))
	copy(rows, r.Schedules)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rate() != rows[j].Rate() {
			return rows[i].Rate() < rows[j].Rate()
		}
		return rows[i].ID < rows[j].ID
	})
	for _, s := range rows {
		fmt.Fprintf(&b, "%-38s %6d %6d %6s %8.2f %7d %7d\n",
			truncMid(s.Agent, s.ID), s.Delivered, s.Completed, pct(s.Rate()),
			s.MeanGap, s.MaxGap, s.Batched)
	}

	b.WriteString("\nrate == 1/meanGap - boundary/delivered, to zero error, for every row above.\n")
	b.WriteString("The completion ratio is the reciprocal of the mean attention gap and nothing\n")
	b.WriteString("else: an agent whose turns run longer than its cadence CANNOT score 100%,\n")
	b.WriteString("however diligent it is. Read maxGap for the failure this detector exists to\n")
	b.WriteString("catch — it is the column that does not saturate.\n")
	return b.String()
}

// stamp renders a time for the report, or a placeholder for the zero value.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "(none)"
	}
	return t.UTC().Format(time.RFC3339)
}

// truncMid renders agent/id inside the table's column width.
func truncMid(agent, id string) string {
	if len(agent) > 14 {
		agent = agent[:14]
	}
	if len(id) > 23 {
		id = id[:23]
	}
	return agent + "/" + id
}
