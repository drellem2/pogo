A pogod killed in its first ~30s left no heartbeat, because the heartbeat loop's
first write waited one full interval. The next `pogod_boot` then either had no
bound on that death, or reported an EARLIER run's beat as the dead run's last
one, which puts the bound before the run started. pogod now writes its first
heartbeat at boot. When the heartbeat file still cannot speak for the previous
run, `pogod_boot`'s `previous.last_heartbeat` is `null` and a `no_heartbeat`
reason says so explicitly. This was hidden on macOS because the wake shim's
`log stream` banner nudges the heartbeat at startup; on the Linux CI runner it
failed `TestPogodRecordsBootShutdownAndUncleanDeath`.
