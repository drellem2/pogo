package refinery

import (
	"fmt"
	"time"
)

// retryPolicy is the merge pipeline's retry decision: the independent retry
// budgets, one per retryable class (mg-e5c2), and what they spent. Moved out
// of processMerge unchanged (enth-vcs slice 7, mg-1fe2b) so the enth land
// pipeline counts its retries against the same budgets — §6.1: stale_tip and
// result_mismatch are contention and count against max_attempts, as a lost
// ff-only race does.
//
// The budgets are separate on purpose: a network blip must not consume the
// budget that exists to absorb a lost race with another merge, and vice
// versa. The loop is hard-capped at their sum so a pathological alternation
// still ends.
//
//   - contention: max_attempts (a lost race with another merge).
//   - network: networkMaxAttempts.
//   - gate-network: the infrastructure failures whose retry costs a WHOLE
//     GATE RUN rather than a git command (mg-67c9). Same class as network,
//     and separate for the same reason the others are separate from each
//     other: spending 28 attempts here would hold one repo's lane for hours.
//     See the sizing note in gatenetwork.go.
//   - setup: a gate whose OWN SETUP did not stand up (mg-15bb). Separate from
//     gate-network even though both cost a whole gate run: the two conditions
//     have unrelated durations, and sharing a counter would let a network
//     outage spend the attempts that exist to ask "was the envelope broken
//     once, or standing?" — which is the only question this budget buys an
//     answer to. See the sizing note in gatesetup.go.
//   - unclassified: defaultUnclassifiedAttempts.
type retryPolicy struct {
	contentionBudget, netBudget, gateNetBudget, setupBudget, unclassifiedBudget int
	contentionUsed, netUsed, gateNetUsed, setupUsed, unclassifiedUsed           int
	// backoffSpent is the backoff slept so far; the caller adds to it after
	// each sleep.
	backoffSpent time.Duration
}

func newRetryPolicy(maxAttempts int) *retryPolicy {
	return &retryPolicy{
		contentionBudget:   maxAttempts,
		netBudget:          networkMaxAttempts,
		gateNetBudget:      gateNetworkMaxAttempts,
		setupBudget:        gateSetupMaxAttempts,
		unclassifiedBudget: defaultUnclassifiedAttempts,
	}
}

// hardCap is the combined attempt cap: the sum of the budgets.
func (p *retryPolicy) hardCap() int {
	return p.contentionBudget + p.netBudget + p.gateNetBudget + p.setupBudget + p.unclassifiedBudget
}

// counts renders the budgets for the step=retry log line.
func (p *retryPolicy) counts() string {
	return fmt.Sprintf("contention=%d/%d network=%d/%d gate-network=%d/%d setup=%d/%d unclassified=%d/%d backoff_spent=%s",
		p.contentionUsed, p.contentionBudget, p.netUsed, p.netBudget, p.gateNetUsed, p.gateNetBudget, p.setupUsed, p.setupBudget, p.unclassifiedUsed, p.unclassifiedBudget, p.backoffSpent.Round(time.Second))
}

// decide charges one failed attempt of disposition disp to its budget and
// answers whether to retry, after what backoff, and the sentence recorded
// either way (reason when no retry follows, retryReason when one does).
func (p *retryPolicy) decide(disp disposition) (retry bool, backoff time.Duration, reason, retryReason string) {
	reason = disp.Reason
	// retryReason is the mirror of reason and is recorded when a retry DOES
	// follow (mg-15bb). It is composed per arm rather than taken from the
	// classifier alone, because the sentence a reader needs includes where
	// in the budget this attempt sat — "attempt 2 of 3" is the difference
	// between a policy and a loop.
	retryReason = disp.RetryReason
	switch {
	case !disp.Retryable:
		// reason is the classifier's sentence for why re-running would give
		// the same answer.
	case disp.Class == ClassInfrastructure && disp.GateRerun:
		// Ahead of the plain infrastructure arm: same class, different price.
		p.gateNetUsed++
		next := gateNetworkBackoffFor(p.gateNetUsed)
		switch {
		case p.gateNetUsed >= p.gateNetBudget:
			reason = fmt.Sprintf("not retryable: the gate-network retry budget is spent — %d of %d attempts used over %s of backoff, each one a whole gate run on the single serial slot every queued merge waits behind. The class is still INFRASTRUCTURE: the GATE could not reach the network, so this is not a verdict on the branch and no fix is warranted. The outage outlasted the refinery's patience — resubmit unchanged once the network is back",
				p.gateNetUsed, p.gateNetBudget, p.backoffSpent.Round(time.Second))
		case p.backoffSpent+next > networkRetryBudget:
			reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s retry budget (%s already slept). The class is still INFRASTRUCTURE — the GATE could not reach the network, not a verdict on the branch",
				next, networkRetryBudget, p.backoffSpent.Round(time.Second))
		default:
			retry, backoff = true, next
		}
	case disp.Class == ClassInfrastructure:
		p.netUsed++
		next := networkBackoffFor(p.netUsed)
		switch {
		case p.netUsed >= p.netBudget:
			reason = fmt.Sprintf("not retryable: the network retry budget is spent — %d of %d network-class attempts used over %s of backoff. The class is still INFRASTRUCTURE: the outage outlasted the refinery's patience, it is not a verdict on the branch",
				p.netUsed, p.netBudget, p.backoffSpent.Round(time.Second))
		case p.backoffSpent+next > networkRetryBudget:
			reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s network retry budget (%s already slept), and this is one serial slot every queued merge waits behind. The class is still INFRASTRUCTURE",
				next, networkRetryBudget, p.backoffSpent.Round(time.Second))
		default:
			retry, backoff = true, next
		}
	case disp.Class == ClassSetup:
		p.setupUsed++
		next := gateSetupBackoffFor(p.setupUsed)
		switch {
		case p.setupUsed >= p.setupBudget:
			reason = fmt.Sprintf("not retryable: the setup retry budget is spent — %d of %d attempts used over %s of backoff, each one a whole gate run on the single serial slot every queued merge waits behind. The class is still SETUP: the gate's own setup failed on every attempt, so it never returned a verdict on this tree and this is NOT a finding against the branch. What the retries established is that the envelope is broken STANDING rather than once; they did NOT establish whose setup it is — a branch can break its own. Read the banner and establish which; do NOT dispatch a fix on this alone",
				p.setupUsed, p.setupBudget, p.backoffSpent.Round(time.Second))
		case p.backoffSpent+next > networkRetryBudget:
			reason = fmt.Sprintf("not retryable: waiting %s more would exceed the %s retry budget (%s already slept). The class is still SETUP — the gate's own setup failed, not a verdict on the branch",
				next, networkRetryBudget, p.backoffSpent.Round(time.Second))
		default:
			retry, backoff = true, next
			retryReason = fmt.Sprintf("RETRYING (setup attempt %d of %d, after %s): %s",
				p.setupUsed+1, p.setupBudget, next, disp.RetryReason)
		}
	case disp.Class == ClassContention:
		p.contentionUsed++
		if p.contentionUsed >= p.contentionBudget {
			reason = fmt.Sprintf("not retryable: the contention retry budget is spent — %d of %d attempts used", p.contentionUsed, p.contentionBudget)
		} else {
			retry = true
		}
	case disp.Class == ClassUnclassified:
		p.unclassifiedUsed++
		if p.unclassifiedUsed >= p.unclassifiedBudget {
			reason = fmt.Sprintf("not retryable: %d of %d unclassified attempts used, and the refinery still cannot place this failure — read the raw error above before reacting", p.unclassifiedUsed, p.unclassifiedBudget)
		} else {
			retry = true
		}
	}

	return retry, backoff, reason, retryReason
}
