- **An unconfigured daemon no longer nudges a coordinator it never started.** The
  stall watcher armed on its own enabled flag, which defaults on, while actually
  starting a coordinator is gated on there being a configuration file at all. A
  daemon running without configuration — an isolated checkout, CI, a sandbox —
  therefore spent its life nudging the default coordinator name and filling a
  mailbox with durable mail about an agent that did not exist. Arming now requires
  a configuration source as well, matching the gate the coordinator's own startup
  already used.
