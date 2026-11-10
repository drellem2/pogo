package main

import (
	"fmt"
	"net"
	"strings"
)

// allowUnauthenticatedBindEnv is the opt-in that lets pogod serve its HTTP API
// on a non-loopback address.
//
// pogod's HTTP API has no authentication. Its trust model is loopback: only
// processes on the same host can reach it (config.DefaultBind is 127.0.0.1).
// The API can spawn agents, so anyone who can reach it can run commands as the
// pogod user. Binding a non-loopback address (0.0.0.0, ::, a LAN IP, a
// hostname that resolves off-host) removes that protection. pogod therefore
// refuses such a bind unless this variable is set to a true value, which makes
// the exposure a deliberate choice. Set it only when something in front of
// pogod (a gateway, an authenticating proxy, a private network) controls who
// can reach the port.
const allowUnauthenticatedBindEnv = "POGO_ALLOW_UNAUTHENTICATED_BIND"

// lookupHost resolves a bind hostname. It is a variable so tests can stub DNS.
var lookupHost = net.LookupHost

// isLoopbackBind reports whether bind names only loopback addresses. An empty
// bind listens on every interface, so it is not loopback. A hostname counts as
// loopback only if every address it resolves to is loopback; a name that does
// not resolve is treated as non-loopback, so the guard fails closed.
func isLoopbackBind(bind string) bool {
	host := strings.TrimSpace(bind)
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // IPv6 zone, e.g. ::1%lo0
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addrs, err := lookupHost(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if i := strings.IndexByte(a, '%'); i >= 0 {
			a = a[:i]
		}
		ip := net.ParseIP(a)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// envTrue reports whether an opt-in variable's value means "yes".
func envTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// checkBindExposure decides whether pogod may listen on bind. It returns an
// error (pogod refuses to start) for a non-loopback bind without the opt-in.
// With the opt-in it returns a non-empty warning to log loudly and no error.
func checkBindExposure(bind string, getenv func(string) string) (warning string, err error) {
	if isLoopbackBind(bind) {
		return "", nil
	}
	shown := bind
	if strings.TrimSpace(shown) == "" {
		shown = "(empty: all interfaces)"
	}
	if !envTrue(getenv(allowUnauthenticatedBindEnv)) {
		return "", fmt.Errorf("pogod: refusing to listen on non-loopback address %s: "+
			"pogod's HTTP API has no authentication and can spawn agents, so anyone who can reach "+
			"this port can run commands as this user. Bind 127.0.0.1 (the default), or, if a gateway "+
			"or private network controls access to the port, set %s=1 to accept the exposure",
			shown, allowUnauthenticatedBindEnv)
	}
	return fmt.Sprintf("pogod: WARNING: serving an UNAUTHENTICATED API on non-loopback address %s "+
		"because %s is set. Anyone who can reach this port can spawn agents and run commands as "+
		"this user. Make sure a gateway or private network controls access to it.",
		shown, allowUnauthenticatedBindEnv), nil
}
