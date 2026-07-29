- **A prompt pogo declines to overwrite is now reported by name.** When installing
  prompts finds a local file you have modified, it writes the incoming version
  beside it as a `.dist` sidecar and leaves yours in place. That is the right
  behaviour and it was invisible: declines were counted nowhere and named nowhere,
  so a boot that declined one file among several printed a reassuring "refreshed
  prompts" that structurally could not name what it had skipped, and a boot whose
  only outcome was a decline printed nothing at all. That silence is how a stale
  coordinator prompt went unnoticed until it misrouted a platform decision to a
  build agent. Declines now drive the summary line and appear in its counts, and
  each declined file gets its own line naming the file, the sidecar written next to
  it, and how to reconcile the two. What pogo does with a conflict is unchanged —
  only whether you hear about it.
