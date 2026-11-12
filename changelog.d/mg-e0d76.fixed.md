- **A recovered agent is no longer refused a restart for failures it already
  got past (mg-e0d76).** synthwatch counted every failed turn in its 30m
  window, even ones a later successful turn superseded, and the respawn gate
  trusted a cached verdict from before the recovery. A crew agent that
  recovered after a usage-limit reset stayed down ~16m. Only failures newer
  than the last successful turn now count, and the gate re-reads the
  transcript before trusting a cached failing verdict.
