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
// The shape is "stall-watch: <head>, oldest <age> — <ids>", and each part is
// there to make two notices that differ in ANY way render differently:
//
//   - head names the category and the count, so an unclaimed-items notice never
//     reads like a blocked-reminder and a batch of two never reads like a batch
//     of one.
//   - age is the oldest item's age, and it is what distinguishes a REPEAT.
//     Ids and count are identical across the repeats of a persisting stall —
//     that is exactly the 18-mail case above, six consecutive notices about
//     mg-0218 — and age is the only fact of the three that must have moved.
//     It is strictly increasing for a fixed item set, and minute resolution is
//     finer than the shortest cooldown any category uses (3m, the priority
//     wake), so consecutive fires cannot collide on it.
//   - ids name which items, which is the first thing a reader wants and the
//     reason they would otherwise have to open the mail.
//
// Where this can still repeat itself: past subjectIDLimit items, two sets with
// a shared prefix and an equal count differ only in age — which does still
// differ between fires, so repeats stay distinguishable; it is two DISTINCT
// simultaneous batches that could collide, and only at six-plus items each.
// Recorded rather than engineered around, because the fix for it (a digest of
// the full id list) would cost the subject the readability it is here to buy.
func subject(head string, oldest time.Duration, ids []string) string {
	var b strings.Builder
	b.WriteString("stall-watch: ")
	b.WriteString(head)
	if oldest > 0 {
		b.WriteString(", oldest ")
		b.WriteString(compactAge(oldest))
	}
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

// compactAge formats a duration for a subject line: two units at most, no
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
