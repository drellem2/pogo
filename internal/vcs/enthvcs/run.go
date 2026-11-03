package enthvcs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// DefaultBin is the enth executable when Backend.Bin is empty: the first
// `enth` on PATH.
const DefaultBin = "enth"

// enthError is enth's own error line: one JSON object on stderr (the enth
// front door's contract, `enth --help`).
type enthError struct {
	Type    string            `json:"type"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Paths   []json.RawMessage `json:"paths"`
	// Ref and Current are a ref refusal's (enth-ref: tag_exists, ref_exists,
	// stale_tip carry the ref and its current value).
	Ref     string `json:"ref"`
	Current string `json:"current"`
}

// parseError finds enth's error line in stderr, if it printed one.
func parseError(stderr []byte) (enthError, bool) {
	for _, line := range strings.Split(string(stderr), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e enthError
		if json.Unmarshal([]byte(line), &e) == nil && e.Type == "error" && e.Code != "" {
			return e, true
		}
	}
	return enthError{}, false
}

// run executes enth with args and returns stdout. A failure is a
// *vcs.CommandError carrying stdout and stderr verbatim; when enth printed
// its error line, Err is a *vcs.RefusedError with enth's stable code, so
// callers branch on errors.As rather than on text.
//
// The token: when the project record names a token file (Repo.TokenFile,
// mg-3a813), it is read now and set as $ENTH_TOKEN for this one call only,
// overriding pogod's own. A named file that cannot be read refuses the call
// before enth runs, naming the path; it never falls back to the shared login.
// With no file named, the environment is the process's own: $ENTH_TOKEN when
// pogod has one, else the token `enth login` saved for the remote. pogo never
// prints or stores the token itself.
func (b Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	tok, ok, err := b.Repo.Token()
	if err != nil {
		return nil, &vcs.CommandError{Args: args, Err: err}
	}
	var extra []string
	if ok {
		extra = []string{vcs.EnthTokenEnv + "=" + tok}
	}
	return run(ctx, b.Bin, extra, args...)
}

// run executes bin with the process's own environment plus extra (later
// entries win, so extra overrides).
func run(ctx context.Context, bin string, extra []string, args ...string) ([]byte, error) {
	if bin == "" {
		bin = DefaultBin
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if len(extra) > 0 {
		cmd.Env = append(os.Environ(), extra...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	out := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	ce := &vcs.CommandError{Args: args, Output: out, Err: err}
	if e, ok := parseError(stderr.Bytes()); ok {
		ce.Err = &vcs.RefusedError{Code: e.Code, Msg: e.Message, Ref: e.Ref, Current: e.Current}
	}
	return stdout.Bytes(), ce
}

// remoteArgs are the global flags that send a verb to the repo's enthd.
func remoteArgs(remote, repo string) []string {
	var a []string
	if remote != "" {
		a = append(a, "--remote", remote)
	}
	if repo != "" {
		a = append(a, "--repo", repo)
	}
	return a
}
