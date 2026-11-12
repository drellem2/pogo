- **A spawned agent's environment survives a respawn (mg-7ec33).** A
  restarted agent used to lose every per-spawn variable, including
  `POGO_WORKER_CORES`, `POGO_HOST_CORES` and `POGO_ROLE`. Respawn now
  re-applies them in the same order as spawn. Refs drellem2/pogo#165
