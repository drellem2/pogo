- **Stall-watch's dispatch notices are much quieter by default (mg-b12da,
  Refs drellem2/pogo#211).** The priority wake and the standard
  unclaimed-items notice were the loudest things stall-watch sent: measured on
  the maintainer's box over 2026-09-26..28 (55h of firing), 72.7 priority-wake
  and 66.1 unclaimed-items fires a day. Most were repeats about items the
  coordinator was already holding, and repeats rarely led to a dispatch. A
  named item was dispatched within 10 minutes of 18% of repeat priority-wake
  notices and 4.7% of repeat unclaimed-items notices. For first notices the
  rates were 44% and 20%. Default changes in `[stall_watch]`, which affect
  every operator who has not set these keys:

  | key | old default | new default |
  |---|---|---|
  | `high_priority_wake_cooldown` | `3m`, doubling per repeat to `repeat_backoff_cap` (4h) | `4h` (flat: named once, then at most every 4h) |
  | `unclaimed_item_cooldown` (new) | — (the category used `nudge_cooldown`, `5m` doubling to 4h) | `4h` (flat) |
  | `dispatch_notice_interval` (new) | — (no gap between notices) | `1h` between two notices of the same dispatch category |

  An item that comes due inside `dispatch_notice_interval` is held and named
  in the next notice. It is never dropped and never counted as told, so this
  is not the per-category cooldown that mg-1693 removed. The cost is latency: a
  second high-priority item within the same hour waits for the rest of it. We
  replayed the new defaults against the same events. The model gives about 7
  priority-wake and 11 unclaimed-items notices a day, down from 54 and 61
  under the same model of the old defaults. The model undercounts the recorded
  old rate by 5–25%. The hard ceiling is now 24 notices a day per category.
  At-cap notices are unchanged and remain report-only. `nudge_cooldown` still
  governs unread mail and the worked-but-unclaimed, preserved-worktree and
  stranded-push alarms. To restore the old behaviour, set
  `high_priority_wake_cooldown = "3m"`, `unclaimed_item_cooldown = "5m"` and
  `dispatch_notice_interval = "0s"`.
