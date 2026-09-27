package agent

import (
	"strings"
)

// Judging WHAT arrived, not only THAT something did (mg-8a70).
//
// A submission receipt proves a submit happened. On Claude Code 2.1.283 that
// was true of 128 of 257 polecat mail-check fires whose text arrived as only its
// last ~120 characters: a message longer than the tty input queue (1022 bytes
// on darwin, TTYHOG-2) reaches the harness in two reads, the harness turns the
// first into a "[Pasted text #N]" placeholder, and the placeholder is dropped at
// submit. Each of those fires was logged as nudge_sent mode=confirm. The fix
// for the cause is in Agent.Nudge (chunked, drain-gated writes); this is the
// check that would have noticed it, and will notice the next shape of it.

// deliveryFingerprintRunes is how much of each end of the sent message must be
// found in the received prompt's excerpts. Long enough that an unrelated prompt
// does not match by accident; short enough to fit inside receiptExcerptRunes
// alongside a harness's wrapper tag.
const deliveryFingerprintRunes = 32

type deliveryVerdict int

const (
	// deliveryUnknown: no record in the window can be judged — every one is a
	// legacy count-only record, or none of them is recognisably this message
	// (a human typed something else into the same window). Not a claim either
	// way; the caller keeps its pre-mg-8a70 behaviour.
	deliveryUnknown deliveryVerdict = iota
	// deliveryIntact: some record carries both ends of the message.
	deliveryIntact
	// deliveryMangled: a record is recognisably this message but is missing
	// one of its ends — the head-truncation this file exists for, or its
	// mirror image.
	deliveryMangled
)

func (v deliveryVerdict) String() string {
	switch v {
	case deliveryIntact:
		return "intact"
	case deliveryMangled:
		return "mangled"
	}
	return "unknown"
}

// judgeDelivery compares the message pogod typed with the prompts the harness
// recorded submitting since. It returns the verdict and, for deliveryMangled,
// the record that showed it.
//
// "Recognisably this message" means the received prompt carries the message's
// head, its tail, or — for a remnant shorter than the fingerprint — is itself
// a suffix of the message. That last case matters because the truncation keeps
// the END: a 1030-byte message keeps only 8 bytes, which can hold no
// 32-rune fingerprint but is plainly the tail of what was sent.
func judgeDelivery(msg string, recs []SubmitRecord) (deliveryVerdict, SubmitRecord) {
	sent := strings.TrimSpace(msg)
	if sent == "" {
		return deliveryUnknown, SubmitRecord{}
	}
	r := []rune(sent)
	head, tail := sent, sent
	if len(r) > deliveryFingerprintRunes {
		head = string(r[:deliveryFingerprintRunes])
		tail = string(r[len(r)-deliveryFingerprintRunes:])
	}

	var mangled *SubmitRecord
	for i := range recs {
		rec := recs[i]
		if !rec.HasContent {
			continue
		}
		hasHead := strings.Contains(rec.Head, head)
		hasTail := strings.Contains(rec.Tail, tail)
		if hasHead && hasTail {
			return deliveryIntact, rec
		}
		remnant := strings.TrimSpace(rec.Head)
		isRemnant := rec.Len <= receiptExcerptRunes && remnant != "" &&
			strings.HasSuffix(sent, remnant)
		if (hasHead || hasTail || isRemnant) && mangled == nil {
			mangled = &recs[i]
		}
	}
	if mangled != nil {
		return deliveryMangled, *mangled
	}
	return deliveryUnknown, SubmitRecord{}
}
