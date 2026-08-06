package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

// The operator surface is what misled the diagnosis on 2026-08-05, so it gets
// its own controls rather than relying on the underlying record being right.

func TestFailedOnceAndFailedAfterThreeAreDifferentBlocks(t *testing.T) {
	once := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 1,
		Attempts: []refinery.AttemptFailure{{Attempt: 1, Stage: "test", Class: refinery.ClassDefect, NotRetriedReason: "not retryable: the test gate ran"}},
	}
	thrice := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 3,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 2},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 5},
			{Attempt: 3, Stage: "fetch", Class: refinery.ClassInfrastructure, NotRetriedReason: "not retryable: the network retry budget is spent"},
		},
	}
	o, th := formatMRAttempts(once), formatMRAttempts(thrice)
	if !strings.Contains(o, "failed once, no retry") {
		t.Errorf("single-attempt header does not say so: %q", firstLine(o))
	}
	if !strings.Contains(th, "failed after 3 attempts") {
		t.Errorf("multi-attempt header does not say so: %q", firstLine(th))
	}
	if firstLine(o) == firstLine(th) {
		t.Error("the two read identically — that is the 2026-08-05 defect, where every failure showed failure_count=1 and nothing said whether a retry had been tried")
	}
}

// TestBothTransportsSurviveToTheOperatorSurface is the regression for the
// several hours lost to a single-transport view. A mixed incident must show
// both transports and both raw errors in the same block.
func TestBothTransportsSurviveToTheOperatorSurface(t *testing.T) {
	mr := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 2, FailureClass: refinery.ClassInfrastructure,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 2,
				RawError: "ssh: connect to host github.com port 22: Undefined error: 0"},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "https",
				NotRetriedReason: "not retryable: budget spent",
				RawError:         "fatal: unable to access 'https://github.com/drellem2/pogo/': Could not resolve host: github.com"},
		},
	}
	out := formatMRAttempts(mr)
	for _, want := range []string{
		"transport=ssh",
		"transport=https",
		"Undefined error: 0",
		"Could not resolve host: github.com",
		"raw error (verbatim",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("attempt block is missing %q:\n%s", want, out)
		}
	}
}

func TestAbsentRetryCarriesItsReason(t *testing.T) {
	mr := &refinery.MergeRequest{
		Status:   refinery.StatusFailed,
		Attempts: []refinery.AttemptFailure{{Attempt: 1, Stage: "test", Class: refinery.ClassDefect, NotRetriedReason: "not retryable: the test gate ran on this tree and returned a verdict"}},
	}
	out := formatMRAttempts(mr)
	if !strings.Contains(out, "retried:   NO — not retryable: the test gate ran") {
		t.Errorf("a missing retry that says nothing looks exactly like a policy that does not exist:\n%s", out)
	}
}

func TestRetriedSuccessIsVisibleOnTheOperatorSurface(t *testing.T) {
	mr := &refinery.MergeRequest{
		Status: refinery.StatusMerged, AttemptCount: 3, RecoveredOnAttempt: 3, RetryBackoffSeconds: 7,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 2, Time: time.Now()},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 5, Time: time.Now()},
		},
	}
	out := formatMRAttempts(mr)
	if !strings.Contains(out, "MERGED on attempt 3 after 2 failed") {
		t.Errorf("a retried success does not name the attempt that won:\n%s", out)
	}
	if !strings.Contains(out, "7s slept") {
		t.Errorf("the backoff a merge paid is invisible:\n%s", out)
	}
}

func TestNoAttemptBlockForACleanFirstTry(t *testing.T) {
	if got := formatMRAttempts(&refinery.MergeRequest{Status: refinery.StatusMerged}); got != "" {
		t.Errorf("a first-attempt merge printed an attempt block: %q", got)
	}
	if got := formatMRAttempts(nil); got != "" {
		t.Errorf("nil MR printed %q", got)
	}
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

// TestAttemptTimesAreUTCAndSaidSo — mg-0235. An attempt time is correlated
// against the far end's logs, which are UTC; rendering it in whatever offset
// the record was deserialized into, unlabelled, asks the reader to apply the
// host's offset silently. That step has been skipped twice.
func TestAttemptTimesAreUTCAndSaidSo(t *testing.T) {
	plusOne := time.FixedZone("BST", 3600)
	at := time.Date(2026, 8, 6, 17, 51, 5, 0, time.UTC).In(plusOne)

	got := formatMRAttempts(&refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 1,
		Attempts: []refinery.AttemptFailure{{
			Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure,
			Transport: "https", Time: at, NotRetriedReason: "not retryable",
		}},
	})

	if !strings.Contains(got, "17:51:05Z") {
		t.Errorf("attempt time is not rendered as labelled UTC:\n%s", got)
	}
	if strings.Contains(got, "18:51:05") {
		t.Errorf("attempt time rendered in the record's stored +01:00 offset:\n%s", got)
	}
}
