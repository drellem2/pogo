- **`pogo service install` no longer overwrites a custom launcher in the
  daemon plist.** The program the plist / systemd unit runs is now
  `POGOD_LAUNCHER`, then `[service] launcher` in config.toml, then pogod on
  PATH. If the installed plist runs a program that is neither, install refuses
  before quiescing the fleet, naming the program; `--adopt-launcher` keeps it,
  `--force-launcher` replaces it. Every plist overwrite by any
  `pogo service install*` keeps the previous file as `<plist>.bak.<timestamp>`.
  The doctor/nightly plist audit names a custom launcher, a missing
  `ProcessType` and a non-true `KeepAlive` instead of printing
  `pogo service install` as the fix; the doctor prompt and runbook now suggest
  install only when no plist exists (drellem2/pogo#105, mg-0e3d9).
