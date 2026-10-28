package main

// `pogo agent spawn-worker` is a static alias for `spawn-polecat`
// (drellem2/pogo#243, mg-4b366). Both names must reach the same command: the
// alias is what the docs offer as the role-neutral spelling, and spawn-polecat
// is what ~25 shipped prompts and checkprompts still name.

import (
	"strings"
	"testing"
)

func TestSpawnWorkerAliasResolvesToSpawnPolecat(t *testing.T) {
	var reqs = map[string]spawnCapture{}
	for _, name := range []string{"spawn-polecat", "spawn-worker"} {
		var cap spawnCapture
		_, stderr, code := runShPogo(t, &cap, "",
			pogoBin+" agent "+name+" cat-4b366 --id mg-4b366 --template polecat --body inline-body")
		if code != 0 {
			t.Fatalf("pogo agent %s must exit 0, got %d\nstderr: %s", name, code, stderr)
		}
		if !cap.got {
			t.Fatalf("pogo agent %s never reached the stub pogod's spawn endpoint", name)
		}
		reqs[name] = cap
	}
	p, w := reqs["spawn-polecat"].req, reqs["spawn-worker"].req
	if p.Name != w.Name || p.Id != w.Id || p.Template != w.Template || p.Body != w.Body {
		t.Errorf("spawn-worker must send the same request as spawn-polecat.\n spawn-polecat: %+v\n  spawn-worker: %+v", p, w)
	}

	// Positive control: an unknown sibling name is NOT accepted, so the pass
	// above is the alias resolving rather than cobra swallowing any name.
	var bogus spawnCapture
	_, stderr, code := runShPogo(t, &bogus, "",
		pogoBin+" agent spawn-nonesuch cat-4b366 --id mg-4b366 --template polecat --body x")
	if bogus.got {
		t.Errorf("an unknown subcommand must not spawn; code=%d stderr=%s", code, stderr)
	}
}

func TestSpawnPolecatHelpNamesSpawnWorkerAlias(t *testing.T) {
	var cap spawnCapture
	stdout, stderr, code := runShPogo(t, &cap, "", pogoBin+" agent spawn-polecat --help")
	if code != 0 {
		t.Fatalf("spawn-polecat --help must exit 0, got %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "spawn-worker") {
		t.Errorf("spawn-polecat --help must list the spawn-worker alias, got:\n%s", stdout)
	}
}
