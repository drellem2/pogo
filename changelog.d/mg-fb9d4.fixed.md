- **A pogod started with SIGHUP ignored no longer passes the ignore to every
  agent it spawns.** Under `nohup` (or a `trap '' HUP` wrapper) SIG_IGN
  survived fork and exec, so polecats survived the PTY hangup and outlived
  pogod, unreachable. pogod now catches and discards an inherited-ignored
  SIGHUP: pogod itself stays immune, and its children exec with SIGHUP at
  default. It logs `pogod: SIGHUP was ignored at launch (nohup?); pogod stays
  immune, children get default` and emits `pogod_sighup_ignored_at_launch`
  (drellem2/pogo#106, mg-fb9d4).
