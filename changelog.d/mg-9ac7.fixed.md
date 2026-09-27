- **`pogo doctor` and `pogo check-oneshots` said a retired `one_shot_complete`
  record left one-shots unmeasurable "until pogod is rebuilt" and offered
  `curl /version` (mg-9ac7).** The running build already had the fix, and the
  thing blocking the check was the old record still inside the 7-day window,
  which a redeploy does not remove. A reader who redeployed would see NOT
  MEASURABLE again the next morning and conclude the redeploy had failed. A bare
  revision also cannot tell you whether d71e1e2 is in it. The notice now gives
  the date the record ages out of the rolling window, says that rebuilding does
  not bring that date forward, and gives a `git merge-base --is-ancestor` test
  with its exit codes. It also gives the `--since` that measures only the part
  of the window after the last retired record.
