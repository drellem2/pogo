// Package haroldwake is the command harold's daemon runs as its [inbox] notify
// hook: when a Discord ping lands for a harold identity, it mails the pogo agent
// that identity belongs to, so pogo's wake-watch wakes that agent to read it.
//
// harold knows nothing about pogo, and pogod knows nothing about this package.
// It is reached only through `pogo hook harold-wake`, which `pogo install`
// wraps as ~/.pogo/bin/harold-wake for harold's config to name.
//
// # The contract harold holds it to
//
// harold runs the command with no shell, cwd /, the daemon's environment minus
// token variables plus HAROLD_IDENTITY, and one JSON object on stdin:
// {"identity", "entry", "arrived"}. Runs start at least notify_interval apart,
// pings in between are folded into one trailing run, and a run still going
// after notify_timeout (10 s by default) is killed with its process group. Its
// stderr lands in harold's log, and a non-zero exit is logged as a failure.
//
// # Why only the count crosses
//
// `entry` is the message, written by anyone who can mention the bot. An agent
// reads its mail as coming from the fleet, so the entry must never reach a mail:
// not the subject, not the body, not mg's argv or stdin. The agent reads the
// ping through harold's own tool, which knows where it came from. The mail
// carries the count from `arrived` and nothing else from stdin.
//
// # Why mail and not a nudge
//
// pogo's wake-watch already turns mail into a wake for a running agent and a
// bounce to the coordinator for a stopped one. A nudge to a stopped agent just
// fails, and nobody hears about it.
package haroldwake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/alerts"
)

const (
	// MapFileName is the optional identity-to-agent map, under POGO_HOME.
	MapFileName = "harold-wake.conf"

	// ShimName is the file `pogo install` writes into POGO_HOME/bin.
	ShimName = "harold-wake"

	// From is the sender mg records. wake-watch quotes it in the pointer it
	// gives the agent, so it says where the news came from.
	From = "harold"

	// StdinLimit caps the payload read. harold's entry is one Discord message
	// and its metadata, a few KiB at most; anything past this is not harold.
	StdinLimit = 256 << 10

	// The two timeouts together stay well inside harold's default 10 s, so a
	// slow mg is reported by this command rather than killed silently by harold.
	DefaultStdinTimeout = 2 * time.Second
	DefaultSendTimeout  = 5 * time.Second
)

// nameRE is what an identity or agent name may look like. Names go into mg's
// argv, so one that starts with "-" would be read as a flag; and harold
// identities and pogo agent names are both plain words.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Options is everything Run reads from outside, so a test can supply each.
type Options struct {
	Stdin  io.Reader
	Stderr io.Writer
	// Identity is $HAROLD_IDENTITY.
	Identity string
	// MapPath is the identity map; a missing file is an empty map.
	MapPath string
	// Known lists the agents that may be woken.
	Known func() ([]string, error)
	// MG resolves the mg binary to an absolute path.
	MG func() (string, error)

	StdinTimeout time.Duration
	SendTimeout  time.Duration
}

// Run does one notify run and returns the exit status: 0 when it mailed the
// agent or chose not to act, 1 when it meant to mail and could not, or when its
// own configuration is broken. Every decision is one line on stderr.
func Run(o Options) int {
	say := func(format string, args ...any) {
		fmt.Fprintf(o.Stderr, "harold-wake: "+format+"\n", args...)
	}
	stdinTimeout := o.StdinTimeout
	if stdinTimeout <= 0 {
		stdinTimeout = DefaultStdinTimeout
	}
	sendTimeout := o.SendTimeout
	if sendTimeout <= 0 {
		sendTimeout = DefaultSendTimeout
	}

	p, err := ReadPayload(o.Stdin, stdinTimeout)
	if err != nil {
		say("not waking anyone: %v", err)
		return 0
	}

	identity := o.Identity
	switch {
	case identity == "":
		identity = p.Identity
	case p.Identity != "" && p.Identity != identity:
		// harold sets both from the same value; a caller that disagrees with
		// itself is not harold, and neither name can be trusted.
		say("not waking anyone: HAROLD_IDENTITY %q and stdin's identity %q disagree", clip(identity), clip(p.Identity))
		return 0
	}
	if identity == "" {
		say("not waking anyone: no identity in HAROLD_IDENTITY or on stdin")
		return 0
	}
	if !nameRE.MatchString(identity) {
		say("not waking anyone: identity %q is not a plain name", clip(identity))
		return 0
	}

	overrides, err := LoadMap(o.MapPath)
	if err != nil {
		// A map that does not parse may hold the line that redirects this
		// identity; falling back to the name could mail the wrong agent.
		say("not waking anyone: %v", err)
		return 1
	}
	known, err := o.Known()
	if err != nil {
		say("not waking anyone: cannot list pogo agents: %v", err)
		return 1
	}
	target, why := Resolve(identity, overrides, known)
	if target == "" {
		say("not waking anyone: %s", why)
		return 0
	}

	mg, err := o.MG()
	if err != nil {
		say("cannot wake %s: %v", target, err)
		return 1
	}
	if !filepath.IsAbs(mg) {
		say("cannot wake %s: mg path %q is not absolute", target, mg)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	var out bytes.Buffer
	// Through alerts.Mail like every other mail pogo sends, so it is listed by
	// `pogo alerts --all` and gets whatever muting and routing that seam grows.
	// The transport is this command's own: mg by absolute path, with a timeout.
	err = alerts.Mail(alerts.HaroldWakePing, func(to, from, subject, body string) error {
		cmd := exec.CommandContext(ctx, mg, "mail", "send", to,
			"--from="+from, "--subject="+subject, "--body="+body)
		// No stdin: mg reads none for --body, and nothing from harold's stdin
		// is passed on in any form.
		cmd.Stdin = nil
		cmd.Stdout = &out
		cmd.Stderr = &out
		// Without this, a child of mg that keeps the output pipe open would
		// hold Wait past the timeout, and harold would kill the run instead.
		cmd.WaitDelay = time.Second
		return cmd.Run()
	}, target, From, Subject(p.Arrived), Body(identity, p.Arrived))
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("mg did not finish within %s", sendTimeout)
		}
		say("cannot wake %s: %s: %v: %s", target, mg, err, clip(strings.TrimSpace(out.String())))
		return 1
	}
	say("mailed %s about %d new ping(s) for identity %s", target, p.Arrived, identity)
	return 0
}

// Payload is the part of harold's notify object this command reads. The entry
// is deliberately absent: it is never decoded, so it cannot be used.
type Payload struct {
	Identity string
	Arrived  int
}

// ReadPayload reads harold's notify object from r, giving up after timeout so a
// stdin nobody closes cannot hold the run until harold kills it.
func ReadPayload(r io.Reader, timeout time.Duration) (Payload, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, StdinLimit+1))
		ch <- result{data, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-time.After(timeout):
		return Payload{}, fmt.Errorf("no payload on stdin within %s", timeout)
	}
	if res.err != nil {
		return Payload{}, fmt.Errorf("reading stdin: %v", res.err)
	}
	if len(res.data) > StdinLimit {
		return Payload{}, fmt.Errorf("stdin is larger than %d bytes", StdinLimit)
	}
	var raw struct {
		Identity *string `json:"identity"`
		Arrived  *int    `json:"arrived"`
	}
	// Errors from here on name what was wrong, never what was there: stdin
	// carries a stranger's text, and stderr goes to harold's log.
	if len(bytes.TrimSpace(res.data)) == 0 {
		return Payload{}, errors.New("stdin is empty")
	}
	if err := json.Unmarshal(res.data, &raw); err != nil {
		return Payload{}, errors.New("stdin is not one JSON object with an integer arrived")
	}
	if raw.Arrived == nil {
		return Payload{}, errors.New("stdin has no arrived count")
	}
	if *raw.Arrived < 1 {
		return Payload{}, fmt.Errorf("stdin's arrived count is %d", *raw.Arrived)
	}
	p := Payload{Arrived: *raw.Arrived}
	if raw.Identity != nil {
		p.Identity = *raw.Identity
	}
	return p, nil
}

// Resolve maps a harold identity to the agent to wake. An override in the map
// wins; otherwise the identity's own name is the agent's, which is how the
// identities are meant to be named. The result must be a known agent: mail to
// any other name makes a mailbox nobody reads, or reaches a polecat that never
// asked for Discord. An empty agent comes with the reason.
func Resolve(identity string, overrides map[string]string, known []string) (agent, why string) {
	target, mapped := overrides[identity]
	if !mapped {
		target = identity
	}
	for _, k := range known {
		if k == target && nameRE.MatchString(k) {
			return target, ""
		}
	}
	if mapped {
		return "", fmt.Sprintf("identity %s maps to %s, which is not a pogo agent", identity, target)
	}
	return "", fmt.Sprintf("identity %s is not a pogo agent and has no line in %s", identity, MapFileName)
}

// Subject is the mail's subject. The count is the only thing in it that came
// from harold.
func Subject(n int) string {
	if n == 1 {
		return "Discord: 1 new ping; call discord_poll"
	}
	return fmt.Sprintf("Discord: %d new pings; call discord_poll", n)
}

// Body is the mail's body. identity is harold's configured name, not anything
// a Discord user chose.
func Body(identity string, n int) string {
	pings := "pings"
	if n == 1 {
		pings = "ping"
	}
	return fmt.Sprintf("harold has %d new Discord %s for identity %s. "+
		"Call discord_poll to read them. "+
		"The text is not in this mail because someone outside the fleet wrote it.",
		n, pings, identity)
}

// MapPath is the identity map's location under pogoHome.
func MapPath(pogoHome string) string {
	return filepath.Join(pogoHome, MapFileName)
}

// LoadMap reads the identity map at path. A missing file is an empty map:
// identities named after their agents need no file at all.
func LoadMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := ParseMap(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return m, nil
}

// ParseMap parses `identity = agent` lines. `#` starts a comment, values may be
// quoted, and anything else is an error rather than a line skipped: a skipped
// override would silently send the identity's pings to the agent of its name.
func ParseMap(data []byte) (map[string]string, error) {
	m := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: want identity = agent", i+1)
		}
		k = unquote(strings.TrimSpace(k))
		v = unquote(strings.TrimSpace(v))
		if !nameRE.MatchString(k) || !nameRE.MatchString(v) {
			return nil, fmt.Errorf("line %d: identity and agent must each be a plain name", i+1)
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("line %d: identity %s is mapped twice", i+1, k)
		}
		m[k] = v
	}
	return m, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// KnownAgents is the coordinator and every crew agent with a prompt. Polecat
// templates are left out: a template is not an agent, and polecats run with
// HAROLD_IDENTITY=none rather than an identity of their own.
func KnownAgents() ([]string, error) {
	prompts, err := agent.ListPrompts()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range prompts {
		if p.Category == "mayor" || p.Category == "crew" {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

// Finder resolves mg without trusting PATH. harold's daemon inherits whatever
// environment started it, so PATH may be launchd's minimal one, or a shell's
// with relative entries that resolve against harold's cwd of /.
type Finder struct {
	// Home is $HOME; ~/go/bin/mg, where `go install` puts mg, is tried first.
	Home string
	// Dirs are tried next, in order.
	Dirs []string
	// PATH is tried last, absolute entries only.
	PATH string
}

// DefaultFinder looks in ~/go/bin, then beside the running pogo (pogod's own
// PATH repair puts that directory first for the same reason), then the usual
// install prefixes, then PATH.
func DefaultFinder() Finder {
	home, _ := os.UserHomeDir()
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin")
	return Finder{Home: home, Dirs: dirs, PATH: os.Getenv("PATH")}
}

// MG returns the first candidate that is an absolute path to an executable
// regular file (symlinks followed).
func (f Finder) MG() (string, error) {
	var dirs []string
	if f.Home != "" {
		dirs = append(dirs, filepath.Join(f.Home, "go", "bin"))
	}
	dirs = append(dirs, f.Dirs...)
	dirs = append(dirs, filepath.SplitList(f.PATH)...)
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, "mg")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("no mg at ~/go/bin/mg, beside pogo, in /opt/homebrew/bin, /usr/local/bin or /usr/bin, or on PATH")
}

// InstallShim writes dir/harold-wake: a short script that runs
// `pogo hook harold-wake` from the given pogo. The logic stays in the pogo
// binary so it upgrades with pogo; a copy of the logic in ~/.pogo/bin would
// go stale the way pogo's other installed copies have. changed is false when
// the file already held exactly this.
func InstallShim(dir, pogo string) (changed bool, err error) {
	if !filepath.IsAbs(pogo) {
		// harold runs the shim from /, so a relative path names the wrong file.
		return false, fmt.Errorf("pogo path %q is not absolute", pogo)
	}
	content := "#!/bin/sh\n" +
		"# harold's [inbox] notify hook, written by `pogo install`. See pogo's\n" +
		"# docs/CONFIGURATION.md, \"Waking an agent on Discord activity\".\n" +
		"exec " + shellQuote(pogo) + " hook harold-wake \"$@\"\n"
	path := filepath.Join(dir, ShimName)
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o111 != 0 {
			return false, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, "."+ShimName+".*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return false, err
	}
	// Renamed into place, so harold never runs a half-written file.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	return true, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// clip bounds a string quoted into stderr.
func clip(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
