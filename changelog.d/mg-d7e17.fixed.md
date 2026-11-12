- **A wedged PTY reader can no longer hang agent stop forever (mg-d7e17).**
  After an agent's child is reaped, pogod waits at most 10s for its output
  to drain, then logs and finishes the stop. Before, a read on the PTY master
  that never returned left `Stop`/`StopAll` parked indefinitely.
