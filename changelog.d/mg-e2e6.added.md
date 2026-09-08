The revision witness watches **any long-lived process**, not only `pogod`, and
the launchd job that arms it is finally audited.

mg-a03d scoped `scripts/revision-probe.sh` to `pogod`, said so, and filed the
general case rather than implying it. Applying architect's test to what it
shipped — *what would this instrument report if the thing it names stopped
entirely?* — the narrow probe reports **green** for a bridget reader that has
been inert for two days, because it is not watching it. That happened twice and
was found by hand both times: the mg-65d2 change sat unexecuted in the running
bridget for a day (mg-c2f5 / mg-8158), and on 2026-08-14 pid 1736 had been up
2d08h and predated two merged fixes, one of them functional. `~/.pogo/bin/bridget`
is a symlink into the repo, so the file on disk is always current and any check
that stats it reports healthy — only the running process is stale.

The probe now also reads a tracked registry, `scripts/revision-subjects.conf`.
`com.pogo.revisionprobe` needed **no change** to gain subjects: the registry
lives in the checkout the probe already reads, so a subject is armed by a merge
plus `sync_src` — no plist edit, no re-install, no `pogo`, no build. A second
launchd job would have been a second thing to notice has stopped.

The new axis is weaker than the revision comparison and is reported as the weaker
thing. A `ps` start time dates the **process**, not the code it loaded, so it can
only disprove: commits that landed after a process started cannot be running in
it (`STALE-PROCESS`, exit 1), while no commits since it started proves nothing at
all and is recorded as `NOT-DISPROVEN`, never `OK`. A subject with no matching
process is `ABSENT` at exit 2 rather than clean, an absent registry is
`NO-REGISTRY` at exit 2 rather than a quiet fall back to pogod-only, and the run
exits with the **worst** subject's status.

`scripts/check-revisionprobe-install.sh` is the second half. The Go audit
registry deliberately has no row for `com.pogo.revisionprobe` and says why in
place — a row needs a Go mirror of the plist, and it would put the auditor for
the deploy witness inside the binary the deploy installs. Both reasons stand;
what they left behind was an audit that does not happen. The new script renders
through the tracked installer from the tracked template, invokes no
`go`/`pogo`/`pogod`, and reports four rows separately: plist drift, whether
launchd knows the label at all, whether the checkout the job executes is current,
and how old the probe's own ledger is. On its first live run it found
`~/.pogo/deploy-src` behind `origin/main` — the hourly job was firing probe text
containing none of the fixes merged since.

The re-notify throttle compares the subject's identity with slop rather than by
string equality, and that was the merge gate's finding, not a standalone suite
run's. The start instant is derived — `date +%s` minus the whole seconds `ps`
reports — so two samples of the same unrestarted process can put its start a
second apart as the truncations fall either side of a boundary. Under exact
equality the throttle reads that as a restart and re-mails on every hourly fire,
which is the alarm nobody reads arriving through the mechanism built to prevent
it. It is now covered by a control that forces a +/-1s wobble rather than by a
gate happening to run slowly.
