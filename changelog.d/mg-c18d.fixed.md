- **doctor's prompt now reads the event log it writes, and asks the CLI where
  it is (drellem2/pogo#145).** `doctor.md` was 484 lines carrying `pogo events
  emit` twice and zero occurrences of `events.log` or `pogo events list`:
  doctor wrote a `stall_restart` record every time it restarted a wedged agent
  and had no route back to what it had written. The log it *was* pointed at
  cannot answer that class of question — measured over 6h on the reporting
  host, pogod's stdout log held **0** occurrences of `server_mode_boot` /
  `agent_stopped` / `wedge_watch_pending` / `work_item_stranded_push` while the
  event log held **233**. doctor is a fresh process on every wake, so "has this
  target already been restarted today?" is not something it can remember, only
  something it can look up; the prompt now hands it the two `pogo events list`
  invocations that answer it, and says that a second restart inside a day is a
  finding to mail `human` rather than a remedy to repeat.

- **The reporter's proposed patch is refused in the same change, and pinned by
  test.** `${POGO_HOME:-$HOME/.pogo}/events.log` cannot reproduce
  `config.PogoHome()`, which normalizes `POGO_HOME == $HOME` to `$HOME/.pogo`.
  On a host where `POGO_HOME` is the home directory that expression names a
  *different* file — which may exist, and be months stale — so the grep returns
  a well-formed wrong answer that `ls -l` then confirms. Run verbatim on the
  upstream host it reproduced exactly the false negative the issue reports:
  empty output at exit 1, against 8 events from `pogo events list --since=2h
  --type=agent_stopped`. `pogo events list` resolves the path through
  `events.LogPath()` and is the only reader that cannot get it wrong. The
  prompt also now states that `--type` and `--agent` are **exact** matches, so a
  family like `wedge_watch_*` is a jq of the output and never a flag — asking
  for `--type=wedge_watch_` returns a clean empty that reads like health.

- **doctor's pogod-log recipe derives the path instead of grepping a literal,
  four months late.** `7082121` gave both `mayor.md` and `doctor.md` the pinned
  `~/Library/Logs/pogo/pogod.log`; `e846f2a` replaced mayor's with a `log=$(...)`
  derivation and never touched doctor, which kept `grep -A1 StandardOutPath
  "$plist"   # today: <literal>` — prose saying *derive it*, one line above a
  command saying *hardcode it*. Both halves were correct on the day, which is
  why nothing caught it. `TestPromptsDoNotAssertADeadLogPath` previously
  asserted the ingredients (`StandardOutPath`, the literal, the `journalctl`
  fallback) and passed on the broken form; it now pins the derivation itself and
  rejects any command line that greps the literal, in **both** prompts. A new
  `TestPromptsThatEmitEventsAreRoutedBackToReadThem` pins the read-back route.
  Prompt changes have no automatic path onto disk — `pogo agent prompt install`
  plus an agent restart is still required before doctor runs the new recipe
  (mg-b6bd).
