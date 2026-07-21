- **Measured: launchd nondemand spawn works in a post-reboot, never-slept session (mg-c09f).**
  Perishable measurement taken in the window after the 2026-07-21 00:13 unplanned
  reboot. Both `StartInterval` and `StartCalendarInterval` throwaway probes fired
  normally in `gui/501` ~2h22m post-boot with zero sleeps since boot — the class
  the standing note describes as dead. Points at **sleep, not uptime**, as the
  wedge trigger, and at reboot as clearing it. Does not unblock tier-2 on its own;
  the decisive follow-up (re-run the same probes after the next sleep/wake) is now
  cheap. See `docs/investigations/launchd-nondemand-spawn-postreboot-2026-07-21.md`.
