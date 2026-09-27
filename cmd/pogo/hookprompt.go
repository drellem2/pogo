package main

import (
	"encoding/json"
	"io"
	"time"
)

// hookStdinTimeout bounds how long `pogo hook prompt-submit` waits for its
// payload. The harness writes the payload and closes stdin at once; the bound
// exists so a hook run with a stdin nobody closes still records the submit
// instead of holding the agent's prompt hostage.
const hookStdinTimeout = 2 * time.Second

// hookStdinLimit caps how much payload is read. A prompt larger than this
// cannot be parsed, and falls back to a count-only receipt.
const hookStdinLimit = 4 << 20

// readHookPrompt reads a UserPromptSubmit payload and returns its "prompt"
// field. ok is false when there is no payload, it does not parse, it has no
// prompt, or it did not arrive within timeout.
func readHookPrompt(r io.Reader, timeout time.Duration) (string, bool) {
	type result struct {
		prompt string
		ok     bool
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, hookStdinLimit))
		if err != nil || len(data) == 0 {
			ch <- result{}
			return
		}
		var payload struct {
			Prompt *string `json:"prompt"`
		}
		if json.Unmarshal(data, &payload) != nil || payload.Prompt == nil {
			ch <- result{}
			return
		}
		ch <- result{*payload.Prompt, true}
	}()
	select {
	case res := <-ch:
		return res.prompt, res.ok
	case <-time.After(timeout):
		return "", false
	}
}
