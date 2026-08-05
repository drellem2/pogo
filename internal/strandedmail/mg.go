package strandedmail

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// execCommand is exec.Command, indirected so tests can stub the mg binary.
var execCommand = exec.Command

// EnumerateMailboxes runs `mg mail list --json` with no AGENT, which emits one
// NDJSON object per mailbox under the mail root: {mailbox, unread, exists}.
//
// That enumeration is the only outside view of a mailbox nobody polls. Every
// other read requires knowing the name to ask for — and not knowing which name
// to type is precisely what an abandoned mailbox looks like from the outside.
func EnumerateMailboxes() ([]Mailbox, error) {
	out, err := execCommand("mg", "mail", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("mg mail list --json: %w", err)
	}
	var boxes []Mailbox
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var b Mailbox
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			// One unparseable line must not discard the enumeration: the
			// mailbox this sweep exists to find could be on the next one.
			continue
		}
		boxes = append(boxes, b)
	}
	return boxes, sc.Err()
}

// ListMessages runs `mg mail list <mailbox> --json` and returns the unread
// messages, so a finding can name the sender and subject rather than a count.
func ListMessages(mailbox string) ([]Message, error) {
	out, err := execCommand("mg", "mail", "list", mailbox, "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("mg mail list %s --json: %w", mailbox, err)
	}
	var msgs []Message
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, sc.Err()
}
