- **A pogod started with SIGINT ignored no longer passes the ignore to its
  children either.** Starting pogod as a `&` job of a shell without job control
  ignores SIGINT for the whole tree, and a child bash then silently refuses
  `trap ... INT`, so a gate script's interrupt cleanup never ran. pogod now
  catches and discards an inherited-ignored SIGINT the same way it does SIGHUP
  (mg-fb9d4). SIGQUIT needs nothing: the Go runtime already overrides an
  inherited ignore for it. The startup event is renamed from
  `pogod_sighup_ignored_at_launch` to `pogod_signal_ignored_at_launch`, emitted
  once per signal with `details.signal` (drellem2/pogo#106, mg-3b86e).
