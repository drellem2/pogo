- **pogod read its server pointer from the heartbeat's goroutines without
  synchronisation (mg-4d5e).** The heartbeat starts before `main` builds the
  server, and both the stall watcher's index-only pause (#190) and the
  orchestration resumer read the pointer from their own goroutines — a data
  race. The server is now published once, fully configured, through an atomic
  pointer. Also: #190's "161 notices" regression control now runs the actual
  pre-fix stallwatch code (extracted from git), not the new code configured
  with a flat cooldown.
