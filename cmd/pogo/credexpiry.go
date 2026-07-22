package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/credexpiry"
)

// newCredentialCmd builds `pogo credential expiry` — the on-demand counterpart
// to pogod's heartbeat warner (mg-7024).
//
// It exists for three jobs the background warner cannot do:
//
//   - Confirm a `/login` landed. The warning mail tells a human to run `/login`
//     and warns that running sessions take ~an hour to recover; without a way to
//     check the new date they cannot tell a successful login from a failed one
//     during that hour, and will run it again.
//   - Answer "when does this expire?" without waiting up to an interval for the
//     daemon to sample.
//   - Make the warner DEMONSTRABLE. A warning that has never been seen to fire
//     is indistinguishable from one that cannot, and this is how you look.
//
// Read-only, like everything in this package: it prints two integers and some
// non-secret descriptors, and has no way to re-mint a credential. Only a human
// can run `/login`.
func newCredentialCmd(jsonOutput *bool) *cobra.Command {
	cmdCredential := &cobra.Command{
		Use:   "credential",
		Short: "Inspect the harness credential's expiry",
		Long: `credential inspects the fleet's harness credential.

It reads ONLY the expiry timestamps and non-secret descriptors. No token value
is ever read, printed, logged or stored.`,
	}

	cmdCredentialExpiry := &cobra.Command{
		Use:   "expiry",
		Short: "Report when the fleet's auth grant expires",
		Long: `expiry reports when the OAuth refresh grant expires — the date of the
next fleet-wide auth outage, absent a /login.

The grant has a fixed 30-day life that use does not extend. When it lapses the
harness can no longer mint access tokens; the fleet coasts on its final 8-hour
access token and then stops. pogod warns automatically at 7d/72h/24h/2h; this
command answers the same question on demand.

Exit status:
  0  the grant is healthy, or there is no credential to inspect on this host
  1  the grant expires within 7 days, or has already lapsed
  2  a credential exists but its expiry could NOT be determined

Status 2 is deliberately distinct from 0. A credential that cannot be read is
not a healthy one — it means the advance warning is blind.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			st := credexpiry.SystemReader(context.Background())
			now := time.Now().UTC()

			if *jsonOutput {
				printCredExpiryJSON(st, now)
			} else {
				printCredExpiryText(st, now)
			}

			switch st.State {
			case credexpiry.StateUnreadable:
				os.Exit(2)
			case credexpiry.StateAbsent:
				// Nothing to report and nothing CLAIMED. Not an error: a Linux
				// box or a sandbox legitimately has no credential.
				os.Exit(0)
			}
			if credexpiry.TierFor(st.RefreshExpiry.Sub(now)) != credexpiry.TierNone {
				os.Exit(1)
			}
		},
	}
	cmdCredential.AddCommand(cmdCredentialExpiry)
	return cmdCredential
}

func printCredExpiryJSON(st credexpiry.Status, now time.Time) {
	out := map[string]any{
		"state":  st.State.String(),
		"reason": st.Reason,
	}
	if st.State == credexpiry.StatePresent {
		remaining := st.RefreshExpiry.Sub(now)
		out["refresh_token_expires_at"] = st.RefreshExpiry.Format(time.RFC3339)
		out["remaining"] = credexpiry.FormatRemaining(remaining)
		out["remaining_hours"] = int(remaining.Hours())
		out["tier"] = credexpiry.TierFor(remaining).String()
		out["fleet_dead_by"] = st.RefreshExpiry.Add(credexpiry.AccessTokenLife).Format(time.RFC3339)
		out["subscription_type"] = st.SubscriptionType
		out["rate_limit_tier"] = st.RateLimitTier
		out["scope_count"] = st.ScopeCount
		if !st.AccessExpiry.IsZero() {
			// Emitted with a name that says what it is NOT, because a consumer
			// who threshold-alerts on this will alert constantly: it is
			// routinely stale on a healthy machine.
			out["access_token_expires_at_informational"] = st.AccessExpiry.Format(time.RFC3339)
		}
	}
	cli.PrintJSON(out)
}

func printCredExpiryText(st credexpiry.Status, now time.Time) {
	switch st.State {
	case credexpiry.StateAbsent:
		fmt.Printf("No credential to inspect: %s\n", st.Reason)
		fmt.Println("\nThis is NOT a report that the credential is healthy — it is a report")
		fmt.Println("that no claim can be made. pogod's expiry warner is disarmed on this host.")
		return

	case credexpiry.StateUnreadable:
		fmt.Printf("Credential expiry is UNREADABLE: %s\n", st.Reason)
		fmt.Println("\nThe keychain item exists but its expiry could not be determined, so the")
		fmt.Println("advance warning of the next fleet-wide auth outage is BLIND. This is not")
		fmt.Println("the same as healthy. Most likely the harness moved its storage or schema,")
		fmt.Println("both of which are harness-internal and not a pogo contract.")
		return
	}

	remaining := st.RefreshExpiry.Sub(now)
	tier := credexpiry.TierFor(remaining)

	fmt.Printf("refreshTokenExpiresAt : %s\n", st.RefreshExpiry.Format(time.RFC3339))
	fmt.Printf("remaining             : %s\n", credexpiry.FormatRemaining(remaining))
	fmt.Printf("subscriptionType      : %s\n", st.SubscriptionType)
	if st.RateLimitTier != "" {
		fmt.Printf("rateLimitTier         : %s\n", st.RateLimitTier)
	}
	if !st.AccessExpiry.IsZero() {
		fmt.Printf("expiresAt             : %s  (8h access token — informational;\n",
			st.AccessExpiry.Format(time.RFC3339))
		fmt.Printf("                        routinely stale on a healthy machine, not a signal)\n")
	}
	fmt.Println()

	switch tier {
	case credexpiry.TierNone:
		fmt.Printf("HEALTHY. pogod will warn `human` starting %s.\n",
			st.RefreshExpiry.Add(-credexpiry.LeadWeek).Format(time.RFC3339))
	case credexpiry.TierLapsed:
		fmt.Printf("LAPSED. The fleet stops working by %s if it has not already.\n",
			st.RefreshExpiry.Add(credexpiry.AccessTokenLife).Format(time.RFC3339))
		fmt.Println("Run /login in any Claude Code session NOW.")
	default:
		fmt.Printf("WARNING (%s tier). Absent a /login the fleet dies between\n", tier)
		fmt.Printf("%s and %s.\n",
			st.RefreshExpiry.Format(time.RFC3339),
			st.RefreshExpiry.Add(credexpiry.AccessTokenLife).Format(time.RFC3339))
		fmt.Println("Run /login in any Claude Code session. It takes seconds; only a human can.")
		fmt.Println("Already-running sessions take ~1h to pick up the new grant (mg-ed45) — that")
		fmt.Println("lag is expected, do not repeat the login.")
	}
}
