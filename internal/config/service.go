package config

// ServiceConfig is the [service] section: settings for `pogo service install`,
// which renders the launchd plist (macOS) or systemd unit (Linux) that runs
// pogod.
//
//	[service]
//	launcher = "/Users/me/.pogo/bin/pogod-launch.sh"
//
// Launcher is the program the installed service execs in place of the pogod
// found on PATH — typically a local wrapper that injects credentials and then
// execs pogod. Empty (the default) means pogod on PATH. The POGOD_LAUNCHER
// environment variable takes precedence over this key. Without it, a host
// whose plist names a wrapper cannot re-render its plist at all: the installer
// would write pogod's path over the wrapper and silently drop whatever the
// wrapper injected (drellem2/pogo#105).
type ServiceConfig struct {
	Launcher string
}
