package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestJudgeDelivery(t *testing.T) {
	msg := longFireMessage
	runes := []rune(msg)
	tail121 := string(runes[len(runes)-121:])
	wrapped := "\n\n<pasted_content id=\"a99d\">\n" + msg + "\n</pasted_content>"

	cases := []struct {
		name string
		recs []SubmitRecord
		want deliveryVerdict
	}{
		{"whole", []SubmitRecord{newSubmitRecord(msg)}, deliveryIntact},
		{"whole but wrapped as a paste", []SubmitRecord{newSubmitRecord(wrapped)}, deliveryIntact},
		// The measured failure: the last 121 characters (t036c, 0590a095).
		{"head lost, 121-char tail", []SubmitRecord{newSubmitRecord(tail121)}, deliveryMangled},
		{"head lost, remnant shorter than the fingerprint", []SubmitRecord{newSubmitRecord(string(runes[len(runes)-8:]))}, deliveryMangled},
		{"tail lost", []SubmitRecord{newSubmitRecord(string(runes[:500]))}, deliveryMangled},
		{"an unrelated prompt is no evidence either way", []SubmitRecord{newSubmitRecord("what is the build status?")}, deliveryUnknown},
		{"a count-only record is never judged", []SubmitRecord{{}}, deliveryUnknown},
		{"no records", nil, deliveryUnknown},
		{"an intact copy anywhere in the window wins", []SubmitRecord{newSubmitRecord(tail121), newSubmitRecord(msg)}, deliveryIntact},
	}
	for _, c := range cases {
		if got, _ := judgeDelivery(msg, c.recs); got != c.want {
			t.Errorf("%s: verdict %v, want %v", c.name, got, c.want)
		}
	}
	if got, _ := judgeDelivery("   ", []SubmitRecord{newSubmitRecord(tail121)}); got != deliveryUnknown {
		t.Errorf("an empty message (a bare return) must not be judged, got %v", got)
	}
	// A short message fits inside one excerpt and is judged whole.
	if got, _ := judgeDelivery("run the sweep", []SubmitRecord{newSubmitRecord("run the sweep")}); got != deliveryIntact {
		t.Errorf("short message: %v", got)
	}
}

func TestReceiptRecordsCarryAnExcerptAndStayCountable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.submits")
	if err := RecordSubmit(path); err != nil { // legacy, count-only
		t.Fatal(err)
	}
	if err := RecordSubmitPrompt(path, longFireMessage); err != nil {
		t.Fatal(err)
	}
	if err := RecordSubmitPrompt(path, "multi\nline\tprompt"); err != nil {
		t.Fatal(err)
	}
	n, err := CountSubmits(path)
	if err != nil || n != 3 {
		t.Fatalf("CountSubmits = %d, %v; want 3 — excerpts must not add lines", n, err)
	}
	recs, err := ReadSubmits(path)
	if err != nil || len(recs) != 3 {
		t.Fatalf("ReadSubmits = %d records, %v", len(recs), err)
	}
	if recs[0].HasContent {
		t.Errorf("a timestamp-only record must parse as content-unknown")
	}
	r := recs[1]
	if !r.HasContent || r.Len != utf8.RuneCountInString(longFireMessage) ||
		!strings.HasPrefix(longFireMessage, r.Head) || !strings.HasSuffix(longFireMessage, r.Tail) ||
		utf8.RuneCountInString(r.Head) != receiptExcerptRunes {
		t.Errorf("long record = %+v", r)
	}
	if recs[2].Head != "multi\nline\tprompt" {
		t.Errorf("newline/tab prompt did not round-trip: %q", recs[2].Head)
	}
}

func TestSplitInputChunks(t *testing.T) {
	for _, max := range []int{0, 4, 5, 7, 512, 5000} {
		chunks := splitInputChunks(longFireMessage, max)
		if strings.Join(chunks, "") != longFireMessage {
			t.Fatalf("max %d: chunks do not reassemble", max)
		}
		for _, c := range chunks {
			if c == "" || !utf8.ValidString(c) {
				t.Fatalf("max %d: bad chunk %q", max, c)
			}
			if max > 0 && len(c) > max {
				t.Fatalf("max %d: chunk of %d bytes", max, len(c))
			}
		}
	}
	if got := splitInputChunks("—", 1); len(got) != 1 || got[0] != "—" {
		t.Errorf("a rune wider than max must be emitted whole, got %q", got)
	}
}
