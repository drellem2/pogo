- **The watcher that dismisses Claude Code's rating dialog now actually fires.**
  Claude Code draws the option row as a terminal footer whose columns are placed
  with cursor-movement escapes rather than literal spaces. Stripping those escapes
  left the text run together as `1:Bad2:Fine3:Good0:Dismiss`, so the watcher's
  literal-spaces comparison never matched and it had dismissed nothing since the
  day it shipped — on one occasion leaving a stuck dialog holding a coordinator's
  input for about two and a half hours. Matching now ignores whitespace on both
  sides, so it handles the real footer, a plain-spaces render, and lesser drift such
  as a doubled or missing space. The idle gate is unchanged, so a mention of the
  dialog in a transcript still cannot trigger a dismissal.
