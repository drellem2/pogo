- **A token-only Claude install no longer stops on the first-run theme picker
  (mg-b0bc2).** Before every spawn and restart that runs `claude`, pogod
  merges `hasCompletedOnboarding: true` into the agent's `.claude.json`.
  Other keys are kept byte for byte, and an unparseable file is left alone.
  Refs drellem2/pogo#286
