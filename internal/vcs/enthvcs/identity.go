package enthvcs

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// A worker's identity for `enth change` (slice 6, mg-30da0, carried over from
// slice 5).
//
// enth refuses a Change with no author or committer (`no_identity`). It takes
// each field from the first set, non-empty source in its chain (enth #9 item
// C, docs/epics/9-write-path.md §2.2):
//
//	ENTH_<ROLE>_NAME / _EMAIL, then GIT_<ROLE>_NAME / _EMAIL, then user.name /
//	user.email in $XDG_CONFIG_HOME/enth/config (else ~/.config/enth/config)
//
// with ROLE AUTHOR or COMMITTER. enth reads no git config file, so a host
// whose only identity is git's ~/.gitconfig — the normal case, and this
// fleet's (measured 2026-11-03: no enth config, no ENTH_/GIT_ identity in the
// environment) — leaves every polecat's `enth change` refused.
//
// IdentityEnv fills exactly the fields that chain would leave empty, and no
// others: an identity the user configured for enth always wins. The value is
// git's user.name / user.email for the source repo, which is the identity a
// git polecat's commits carry, so a worker's Changes are attributed as its
// commits are. With no git identity either, the worker's agent name is used,
// with an address under .invalid (RFC 2606) so it is never mistaken for a
// mailbox.

// IdentityFallbackDomain is the domain of the address made up for a worker
// when no identity is configured anywhere.
const IdentityFallbackDomain = "pogo.invalid"

// configUser reads user.name / user.email from enth's config file (git-config
// syntax, the [user] section only, as enth reads it).
func configUser(getenv func(string) string) (name, email string) {
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			return "", ""
		}
		dir = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(dir, "enth", "config"))
	if err != nil {
		return "", ""
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			section = strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			continue
		}
		if section != "user" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			name = v
		case "email":
			email = v
		}
	}
	return name, email
}

// IdentityEnv returns the ENTH_<ROLE>_NAME / _EMAIL entries a worker needs so
// that `enth change` has an identity, given the environment the worker will
// otherwise inherit (getenv), git's identity for the source repo, and the
// worker's agent name. It returns nil when enth would already find one.
func IdentityEnv(getenv func(string) string, gitName, gitEmail, worker string) []string {
	cfgName, cfgEmail := configUser(getenv)
	if gitName == "" {
		gitName = worker
	}
	if gitEmail == "" {
		gitEmail = worker + "@" + IdentityFallbackDomain
	}
	var env []string
	for _, role := range []string{"AUTHOR", "COMMITTER"} {
		for _, f := range []struct{ field, cfg, value string }{
			{"NAME", cfgName, gitName},
			{"EMAIL", cfgEmail, gitEmail},
		} {
			if getenv("ENTH_"+role+"_"+f.field) != "" || getenv("GIT_"+role+"_"+f.field) != "" || f.cfg != "" {
				continue
			}
			if f.value != "" {
				env = append(env, "ENTH_"+role+"_"+f.field+"="+f.value)
			}
		}
	}
	return env
}
