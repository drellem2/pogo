package stallwatch

import (
	"fmt"
	"strings"
	"time"
)

// Notice is one stall-watch notification: the body the recipient reads, and the
// subject a mail client renders for it.
//
// # Why the subject is a field rather than a constant in the delivery function
//
// It used to be a constant. Every stall-watch notice — five categories, any
// number of items, any age — reached macguffin mail under the single string
// "stall-watch: work piling up", composed at the delivery site in cmd/pogod
// where none of the facts are in scope. That is where the defect lived: the
// MESSAGE has always named the category, the count and the item ids, and the
// subject threw all of it away.
//
// The cost is not theoretical and it is not "too many alerts". Measured on this
// box over 2026-08-11 12:00Z .. 2026-08-12 09:52Z, `human` received 18
// stall-watch mails. Every one was a blocked-reminder; their bodies covered
// THREE different item sets (mg-fbc1 alone, mg-8888 alone, then both together,
// then mg-0218) at counts of one and two — and all 18 carried that one subject.
// The recipient reads the mail through Discord, which renders the subject, so
// eighteen distinguishable facts arrived as one sentence printed eighteen times
// and none could be told from the others without opening it.
//
// So the remedy is not to send fewer. The rate limiting already works — 18
// notices in 22 hours is far from every occurrence, and those notices were
// correct, several stalls were dispatched off them overnight. The remedy is to
// let the subject carry what the watcher already knows.
type Notice struct {
	// Subject is the mail subject. Delivery functions that have no subject
	// concept (a PTY write) ignore it.
	Subject string
	// Message is the notice body, and the whole notice on the PTY road.
	Message string
}

// subjectIDLimit caps how many work-item ids a subject names before it
// collapses the rest into "+N more".
//
// Five, not three: the id list is the subject's strongest discriminator, and
// truncation is the one way this builder can reproduce the defect it exists to
// fix — two different item sets sharing a truncated prefix at the same count
// render the same head and the same ids. Five puts that beyond every batch size
// stall-watch has actually emitted (the measured window's largest was two)
// while keeping the line short enough to survive a Discord render. It is not a
// guarantee; see subject's note on what still discriminates when it bites.
const subjectIDLimit = 5

// subject renders a notice's mail subject from the facts the check already
// computed for its event details.
//
// The shape is "stall-watch: <head> — <ids>". A subject names WHICH ALARM this
// is; the body says how bad it is right now. Two notices get the same subject
// exactly when they are about the same condition — the same category over the
// same item set — and different subjects whenever they are not:
//
//   - head names the category and the count, so an unclaimed-items notice never
//     reads like a blocked-reminder and a batch of two never reads like a batch
//     of one. For the item categories the count is a property of the item set,
//     so it moves only when the set does.
//   - ids name which items, which is the first thing a reader wants and the
//     reason they would otherwise have to open the mail.
//
// What the subject deliberately does NOT carry is anything that moves while the
// condition merely persists — the oldest item's age, and the unread-mail count
// (mg-09d9). mg-b6f8 put the age here to make repeats distinguishable, and that
// made every copy of every stall-watch notice unique: measured 2026-09-03 in the
// mayor's mailbox, 2162 distinct subjects across 2167 stall-watch mails.
// `mg mail reclaim` coalesces a backlog by exact Subject, keeping the newest
// --keep per group, so it could retain essentially all of them — and the
// unread-mail alarm was the worst case, because each of its own notices raised
// the count printed in its successor's subject. The alarm about a pile-up was
// the one pile nobody could drain.
//
// A repeat of a persisting stall is the same fact again, and reading as the
// same line is the true rendering of that. Its body still says how old the
// stall is and, from the second notice on, "[repeat] notice #N"; its event
// still records oldest_age_seconds. mg-b6f8's actual defect — DIFFERENT item
// sets and categories collapsing onto one constant — stays fixed, and is what
// TestSubjectHeadsSeparateTheCategories and the measured-sequence test pin.
//
// Normalising digits at the reclaim end was considered and rejected: it would
// also fold item ids together (mg-0218 and mg-8888 are both "mg-N"), which
// collapses two different alarms and drops the last pointer to one of them.
//
// Where this can still repeat itself: past subjectIDLimit items, two DISTINCT
// simultaneous batches sharing a five-id prefix at an equal count render the
// same subject. Recorded rather than engineered around, because the fix for it
// (a digest of the full id list) would cost the subject the readability it is
// here to buy.
func subject(head string, ids []string) string {
	var b strings.Builder
	b.WriteString("stall-watch: ")
	b.WriteString(head)
	if len(ids) > 0 {
		b.WriteString(" — ")
		if len(ids) <= subjectIDLimit {
			b.WriteString(strings.Join(ids, ", "))
		} else {
			b.WriteString(strings.Join(ids[:subjectIDLimit], ", "))
			fmt.Fprintf(&b, " +%d more", len(ids)-subjectIDLimit)
		}
	}
	return b.String()
}

// nItems renders "N item" / "N items" for a subject head. Subjects are read at a
// glance in a notification list, where the "item(s)" the message bodies use
// reads as clutter.
func nItems(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// compactAge formats a duration for a notice line: two units at most, no
// fractional seconds, and minute resolution above an hour.
//
// time.Duration.String is unusable here — it renders 6h3m as "6h3m0s" and an
// age computed from a float second count as "6h3m0.499s". The precision is
// noise in a subject and it costs the characters the id list needs.
func compactAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
