package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/alerts"
)

func TestPrintAlerts(t *testing.T) {
	var b bytes.Buffer
	printAlerts(&b, []alerts.Row{
		{Name: alerts.BlindWatchFinding, Class: alerts.ClassAlert, Muted: true, Effective: "mayor", Default: "$coordinator", Source: "blind_watch.mail = false"},
		{Name: alerts.SchedulerDelivery, Class: alerts.ClassDelivery, Effective: "the schedule's target", Default: "the schedule's target", Source: "default"},
	})
	out := b.String()
	for _, want := range []string{"NAME", "blind_watch.finding", "MUTED", "blind_watch.mail = false", "scheduler.delivery (delivery)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
