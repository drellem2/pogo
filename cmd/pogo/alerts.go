package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
)

// newAlertsCmd builds `pogo alerts` (drellem2/pogo#262): the list of every
// alert pogod can send, by name, with who receives it and whether it is muted.
func newAlertsCmd(jsonOutput *bool) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "List every alert pogod sends, its recipient, route and mute state",
		Long: `alerts lists every alert pogod can send — each by its stable name,
"<family>.<kind>" — with its default recipient, the recipient it actually
reaches on this install, and whether it is muted.

VIA is "mail", or "nudge" for an alert typed into the recipient's terminal
(stall-watch's checks, a condition's wake, crew-reset's pointer). Nudges are
muted and routed exactly like mail. Nudges that only deliver something else —
wake-watch's mail-arrival pointers, the kickoff prompt, the mid-session-wedge
submit, scheduler deliveries — are not alerts and are not listed.

DEFAULT is the built-in rule ($coordinator and $escalation_box name the
[agents] coordinator and escalation_box). EFFECTIVE is that rule resolved
against the running daemon's config. SOURCE names the config key that
changed the recipient (a route, or a notify_to) or muted the alert, or
"default". Muted alerts are
listed first.

Mute an alert with:

  [alerts."<name>"]
  mute = true

A muted alert is not sent; its detector still runs, and each withheld send is
recorded as an alert_muted event. The older keys ([conditions] mail /
out_of_band / disable, [blind_watch] mail / out_of_band, [stall_watch]
blocked_reminder_enabled) are aliases, and an [alerts] entry wins over them.

Route an alert's primary copy (or a nudge) to another agent or box with:

  [alerts."<name>"]
  route = "<agent or box>"     # or "$coordinator" / "$escalation_box"

Escalation copies keep going to [agents] escalation_box. A route to a box that
does not exist falls back to the default recipient (recorded as an
alert_route_invalid event), so mail is never dropped. Every watcher's
notify_to is an alias for route, and an [alerts] route wins over it.

--all adds the delivery mechanics that also send mail but are not alerts
(the scheduler delivering a registered message).

The list comes from the running pogod (GET /alerts), so it reflects the
config the daemon loaded, not the file as it is now.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			rows, err := client.GetAlerts(all)
			if err != nil {
				cli.ExitWithError(*jsonOutput, "pogo server is not reachable: "+err.Error(), cli.ExitError)
			}
			if *jsonOutput {
				cli.PrintJSON(rows)
				return
			}
			printAlerts(os.Stdout, rows)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also list delivery mechanics that send mail but are not alerts")
	return cmd
}

func printAlerts(w io.Writer, rows []alerts.Row) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVIA\tMUTED\tEFFECTIVE\tDEFAULT\tSOURCE")
	for _, r := range rows {
		muted := "no"
		if r.Muted {
			muted = "MUTED"
		}
		name := string(r.Name)
		if r.Class != alerts.ClassAlert {
			name += " (" + r.Class + ")"
		}
		via := r.Channel
		if via == "" {
			via = alerts.ChannelMail // a pogod older than slice 4 lists mail only
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", name, via, muted, r.Effective, r.Default, r.Source)
	}
	tw.Flush()
}
