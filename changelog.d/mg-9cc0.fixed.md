- **A kickstart that took more than one spawn now says so in `pogo-deploy.log`
  (mg-9cc0).** On 2026-08-13 at 02:01:19Z — inside the `verify` stage of a run
  the same log describes as a success — launchd's first post-kickstart spawn of
  `com.pogo.daemon` died 29ms in: `xpcproxy exited due to OS_REASON_CODESIGNING
  | Launch Constraint Violation`, followed by `removing service since it exited
  with consistent failure`. launchd respawned 10s later, that instance came up,
  `verify_running` saw main's revision, and the deploy reported success. Every
  line after the first spawn was true. The burned spawn appeared in nothing this
  project writes: for that window `pogo-deploy.log` holds `stage: restart`,
  `stage: verify`, `verified:` and not one word about a restart that needed two
  attempts. **Every agent on the box booted from that redeploy.**

  The reason it had to be fixed rather than merely investigated is that the only
  record was in a log tier that ages out in hours. It was readable at ~07:30 and
  gone by ~08:50 the same morning; a re-check at 5.7h returned zero, and so did
  its positive control, so the zero established nothing in either direction. By
  mid-morning the failure was not just unrecorded but **unobservable** — the
  next occurrence would have been equally so.

  **The fix is a delta, and the delta is the whole idea.** launchd does keep
  durable state — this box still read `last exit reason = OS_REASON_CODESIGNING`
  on a healthy daemon seven hours later, long after the log line had aged out —
  but `runs` and `last exit …` are *lifetime* fields with no timestamp and no
  attribution. Printed on their own each night they would say
  `OS_REASON_CODESIGNING` forever about an event nobody could place, which is
  why mg-9cc0's sibling `pogo service supervision` carries them as report-only
  and refuses to let them reach a verdict. That treatment is correct for a
  single sample. Two samples answer a question one cannot: `runs` read either
  side of *this* kickstart bounds the spawns to *this* restart, and once the
  delta shows an attempt was burned, `last exit …` stops floating — the only
  instance to have exited since the pre-reading is the burned one. So the deploy
  now writes `restart: 1 spawn, no attempts burned (launchd runs N -> N+1)` on a
  clean night, and on a dirty one `restart: took 2 SPAWNS — 1 burned before one
  stayed up`, with launchd's reason for the death.

  **The clean line is not noise.** A recorder that speaks only on failure cannot
  be distinguished from one that is broken, and "no news" is precisely what
  2026-08-13 looked like.

  Three deliberate constraints, each from a way this remedy could have exhibited
  the defect it removes:

  - **It never invokes the just-installed `pogo` binary.** The neighbouring
    `report_supervision` and `report_prompt_refresh` steps legitimately do, but
    the failure being recorded here *is the new binary being refused at launch* —
    a reader that must launch that binary can fail for the same reason as the
    thing it was sent to observe, and would go quiet exactly when it is needed.
    Pure shell over `launchctl print`, which is not the artifact under
    suspicion.
  - **It reports before the exit-8 path, not after.** `verify_running || exit 8`
    would have exited straight through the worst case — spawns burned *and* the
    daemon never came up — without a word, reproducing the silence in the
    remedy.
  - **An unreadable count is loud.** A sample that could not be taken renders as
    `SPAWN COUNT UNREADABLE`, never as a clean restart. A counter that went
    *backwards* is louder still: launchd resets it when a job is unloaded and
    re-registered, which is what `removing service since it exited with
    consistent failure` does.

  **Measured, not asserted.** A sandbox LaunchAgent armed to fail its first
  spawn moved `runs` 2 -> 4 across one `launchctl kickstart -k`, and the new
  reporting fired: the burned count, and `last exit code = 1` from the spawn
  that died. Its replacement arrived on launchd's 10s respawn throttle — the
  same 10s gap as the night this is about. The control also caught a real bound
  on its first run: launchd reports a *dying* spawn as `state = running` for the
  milliseconds it lives, so a post-sample taken on launchd's own state field can
  land between the burned spawn and its replacement and read a burned restart as
  clean. `verify_running` is immune because it polls `GET /version` until a
  pogod answers, which a 29ms-dead spawn never does — hence the placement, which
  a test now pins.

  Still open, deliberately not started here: **why** the first spawn violated a
  launch constraint and the second did not. `com.pogo.daemon` carries
  `managed LWCR | has LWCR` in `launchctl print`, so a signing or quarantine
  state that settles between attempts is the shape to look for. Getting it
  recorded came first; without that, the next occurrence would have been as
  unobservable as this one.
