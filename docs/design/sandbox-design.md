# Pogo sandbox design

**Status:** proposal · **Owner:** architect · **Tracks:** mg-72bf
**Date:** 2026-05-07

## Context

Daniel directive 2026-05-07: *"work with architect to investigate sandbox in pogo and propose a design."* Today every polecat is spawned with `claude --dangerously-skip-permissions` inside a git worktree at `~/.pogo/polecats/<name>` and otherwise runs as the unrestricted parent user. That is fine while pogo is one trusted developer talking to one trusted Claude, but it leaves no defence-in-depth for the cases that matter: a buggy or hallucinating polecat doing `rm -rf` on the wrong path, prompt-injection from a malicious mg ticket body, a compromised dependency in a build script, and (later) any move toward multi-tenant cloud pogo.

This is design-only. No production code lands in this ticket; implementation tickets are filed as follow-ups once Daniel picks an option.

## Diagnosis

Survey of the codebase (rooted at `internal/agent/`, `internal/refinery/`, `internal/claude/`):

- **Spawn path is bare.** `internal/agent/agent.go` (~lines 266–313) sets `cmd.Dir`, injects three `POGO_*` env vars, hands the child a PTY via `creack/pty`, and starts it. There is no `SysProcAttr`, no `setrlimit`, no process-group separation, no namespace work. Polecats inherit pogod's full ambient authority.
- **Worktrees isolate code, not behaviour.** `git worktree add` gives the polecat a unique branch and checkout dir; nothing stops the child from `cd`-ing out of it, reading `~/.ssh/`, or writing to `~/.zshrc`. Cleanup is `git worktree remove --force` on exit (`internal/agent/agent.go:462–465`); files written outside the worktree are not reaped.
- **Refinery TOFU.** `internal/refinery/merge.go` does `git clone --no-local` of polecat-authored branches and runs configurable quality gates — by default `./build.sh` and `./test.sh` (`merge.go:275–323`) — as the same user as pogod. **Polecats can ship build.sh changes that the refinery then executes.** This is the most direct path from "polecat is wrong" to "host is owned"; any sandbox design has to cover refinery gate execution, not just the polecat itself.
- **Claude-CLI permissions ≠ OS sandbox.** `docs/polecat-permissions.md` documents the `--dangerously-skip-permissions` flag and trust-dialog auto-accept. Those bypass Claude Code's *internal* permission prompts so the polecat can run autonomously; they say nothing about syscalls, files, or the network. The OS layer is unguarded.
- **No sandbox-shaped code anywhere.** Greps for `sandbox`, `seccomp`, `namespace`, `chroot`, `setrlimit`, `prlimit`, `cgroup`, `landlock`, `apparmor`, `selinux`, `bwrap`, `firejail`, `sandbox-exec` find only two test-file comments. This is greenfield.
- **No `internal/platform/` package yet.** The only OS branching is `runtime.GOOS` in `internal/service/service.go:163` picking launchd vs systemd. The sleep-resilience design (mg-c4a3) already proposes `internal/platform/sleep/`; this design adopts the same pattern for sandboxing.

## Threat models

The design has to cover one of two adversaries; Daniel should pick before implementation tickets land. They share an interface but ship in very different shapes.

- **TM1 — Daniel-machine, defence-in-depth.** Adversary is a buggy polecat, hallucinating Claude, prompt-injection from a ticket body, or a compromised dev dependency. We accept that the polecat can read code on the host (Daniel could anyway). We refuse: writes outside the worktree + a small allowlist (`~/.pogo`, `/tmp`, build/cache dirs); arbitrary network egress; persistence (cron, LaunchAgents, `~/.ssh/authorized_keys`); fork bombs and runaway memory; refinery executing untrusted shell as Daniel. Process-level sandboxing (sandbox-exec on macOS, seccomp+namespaces on Linux) is sufficient; kernel-exploit resistance is not in scope.
- **TM2 — cloud / rent-a-programmer, multi-tenant.** Adversary is a hostile customer attacking other tenants, or a polecat trying to pivot off the worker host. We need strong isolation: per-polecat MicroVM (Firecracker / Apple Virtualization.framework) or gVisor, per-tenant network namespace, encrypted scratch volumes, audit logs to an out-of-VM sink. Process-level sandboxing alone is not enough.

Recommended posture: **design for TM1 now, leave the seam for TM2 later.** Pogo cloud is not a real product yet; building Firecracker-grade isolation today is months of work that defends nothing currently shipping. But the abstraction below — a `Profile` + a `Sandbox` interface in `internal/platform/sandbox/` — is the same interface a future MicroVM driver would implement, so we don't paint ourselves into a corner.

## Proposal

### Core abstraction

A `Profile` describes what a confined process is allowed to do; a `Sandbox` is the platform shim that enforces it. Both live in `internal/platform/sandbox/`, behind a Go interface so the rest of pogo never branches on `runtime.GOOS`.

```go
// internal/platform/sandbox/sandbox.go (sketch — not final)
package sandbox

type Profile struct {
    AllowReadPaths  []string  // absolute paths readable (default deny)
    AllowWritePaths []string  // absolute paths writable (default deny)
    AllowNetwork    NetPolicy // hostname/CIDR allowlist; zero value = deny-all
    MemoryLimitMB   int
    CPUTimeSec      int
    MaxProcs        int       // RLIMIT_NPROC
    MaxFDs          int       // RLIMIT_NOFILE
    MaxOutputBytes  int64     // RLIMIT_FSIZE on stdout/stderr capture
}

type Sandbox interface {
    Apply(cmd *exec.Cmd, p Profile) error
    Capabilities() Capabilities  // which Profile dimensions are enforced
}
```

Profiles are declared in prompt frontmatter (TOML) per agent template, e.g.:

```toml
[sandbox]
allow_write = ["{{.WorktreeDir}}", "~/.pogo", "/tmp/pogo-*"]
allow_network = ["api.anthropic.com", "github.com", "registry.npmjs.org"]
memory_mb = 4096
cpu_seconds = 1800
```

Default profiles per agent class: **crew** (architect, mayor, pm-*) gets a wide profile (broad read, write to `~/.pogo` and the user's work tree); **polecat** gets the narrow profile above; **refinery quality-gate** gets the strictest (worktree read-only, no network, hard CPU/memory caps). Per-mg-ticket overrides are out of scope for v1.

### Three implementation options, ranked by complexity

**Option A — rlimits + audit, no enforcement of FS/network (~1–2 days).**
Cross-platform `setrlimit` via `cmd.SysProcAttr.Credential` and `syscall.Setrlimit` in a `pre-start` hook. Memory cap, CPU cap, fd cap, fsize cap, nproc cap. Add a passive FS-write audit log (LD_PRELOAD on Linux; DTrace probe on macOS, optional) that records every open/write outside the worktree to `~/.pogo/audit.log` without blocking. Network stays unrestricted. **Defends:** runaway resource use, fork bombs, accidental output blowups. **Does not defend:** anything intentional. **Value:** ships in days; the audit log de-risks the Option B profile by telling us what polecats actually touch.

**Option B — OS-native sandbox (~1–2 weeks).**
Two platform shims behind the `Sandbox` interface.
- **macOS** via `sandbox-exec -p '<scheme-profile>'`. Generate the SBPL profile from the `Profile` struct: `(deny default)`, then `(allow file-read*)` for the read paths, `(allow file-write*)` for the write paths, `(allow network*)` only for the explicit hosts. sandbox-exec is officially deprecated but still functional on every macOS Daniel runs and what Apple's own tools use; if Apple removes it, the seam to swap in `App Sandbox`/Endpoint Security stays the same.
- **Linux** via `bubblewrap` (`bwrap`) when present, with a built-in fallback that does the user-namespace / mount-namespace / seccomp-bpf dance directly. bwrap saves us writing the namespace setup and is packaged on every modern distro. Seccomp filter is a default-allow-with-denylist (deny `mount`, `umount2`, `pivot_root`, `unshare`, `kexec_*`, `bpf`, `ptrace`, `perf_event_open`, `keyctl`, `personality`).
- Plus rlimits from Option A on both platforms.
- Network: rely on the OS sandbox to deny by default; allowlist via the `Profile.AllowNetwork` field translated to sandbox-exec network rules / bwrap socket filtering. If hostname-level (not just IP/CIDR) allowlisting proves too painful at the OS layer, fall back to a local SOCKS/HTTPS proxy that pogod runs and the sandboxed child is forced through via env vars.

**Defends:** TM1 fully — file writes, network egress, persistence, process resource exhaustion. **Does not defend:** kernel exploits, shared-kernel side-channels (irrelevant for TM1, fatal for TM2).

**Option C — VM/container isolation (~1–2 months).**
Each polecat runs inside a Firecracker MicroVM (Linux) / Apple Virtualization.framework VM (macOS) with the worktree mounted via virtio-fs. Per-VM tap device with explicit firewall. Snapshotable, killable, accountable. **Defends:** TM1 + TM2. **Cost:** seconds of spawn latency per polecat, real engineering effort, currently zero customer demand. Not justified until pogo cloud is a real product.

### Refinery's role

Refinery's quality-gate execution is the highest-value sandboxing target in the system: it runs polecat-authored shell as the merging user. The design treats refinery as a sandbox client like any other — gates run under `Sandbox.Apply` with the strictest profile (no network, read-only worktree, no parent-tree writes, tight CPU/memory caps). This blocks the "polecat ships malicious build.sh → refinery owns the host" path without blocking the legitimate "polecat ships better tests" workflow.

### Rollout posture

- **Phase 0 (now):** ship Option A. It's nearly free and the audit log produces ground truth for the Option B allowlists.
- **Phase 1 (after ~1–2 weeks of audit data):** ship Option B for both macOS and Linux behind a default-on flag; keep an env-var escape hatch (`POGO_SANDBOX=off`) for one release in case a profile is wrong.
- **Phase 2 (deferred, gated on pogo cloud):** Option C. Same `Sandbox` interface, new platform driver.

## Roadmap (follow-up implementation tickets)

Filed once Daniel picks an option. Sized as rough working days.

1. `internal/platform/sandbox/`: package skeleton + `Profile` / `Sandbox` interface + no-op default — 0.5d.
2. Cross-platform rlimits shim (Option A) — 0.5d.
3. Audit-mode FS-write logger (Option A; LD_PRELOAD + DTrace) — 1–2d.
4. Profile schema in prompt frontmatter + per-agent-class defaults — 1d.
5. macOS sandbox-exec driver (Option B) — 3–5d.
6. Linux bwrap + seccomp driver (Option B) — 3–5d.
7. Refinery: run quality gates under the strict profile — 1–2d.
8. Network allowlist plumbing (sandbox-native first; SOCKS-proxy fallback if needed) — 2–3d.
9. Docs: `docs/sandbox-profiles.md` (per-template profile reference) and operator-facing tuning notes — 1d.

## Out of scope

- TM2 cloud isolation (Option C). Filed only if pogo cloud becomes real.
- Replacing `--dangerously-skip-permissions`. Once OS sandboxing enforces, the Claude-CLI permission layer is redundant defence; orthogonal cleanup.
- Per-mg-ticket profile overrides. v1 ships per-agent-class only.
- Signing / attestation of polecat commits. Adjacent problem; separate ticket if needed.

## Design rationale

Three things drove the shape:

1. **Refinery is the real attack surface.** Any sandbox design that ignores quality-gate execution misses the most direct host-compromise path. Treating refinery as a sandbox client (with the strictest profile) costs almost nothing in the abstraction and closes the largest hole.
2. **Ground truth before enforcement.** Default-deny FS/network without knowing what polecats actually touch will produce a profile riddled with false positives and a frustrating debugging loop. Option A's audit log buys the data we need to size Option B's allowlists honestly.
3. **One interface, three drivers.** The cross-platform constraint plus the eventual cloud move both want the same shape: a `Profile` value, a `Sandbox` interface, swappable drivers. macOS sandbox-exec, Linux bwrap+seccomp, and a future Firecracker driver are all just `Apply(cmd, profile) error`. That is also why the design doesn't reach for a higher-level abstraction (containers, Docker, nsjail) — they each pin us to one platform shape and make TM2 harder, not easier.
