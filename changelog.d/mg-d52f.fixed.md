- **An unreachable nil guard in `mailLoopExclusionFor` is gone, and the
  precondition it pretended to enforce is written down instead (mg-d52f).** The
  `a == nil` arm could not be reached from either way in: `mailLoopFor` returns
  `mailLoopUnknown` on `a == nil` before ever calling it, and
  `Registry.MailLoopReport`'s unjudged arm dereferences `a.Name` and `a.Type` in
  the *same composite literal* that calls it — so a nil agent panics there
  whatever this function returns.

  Cosmetic, with one real cost, which is why deletion rather than "make it
  reachable" was the correct change: it read as protection that is not there. A
  future reader adding a third caller could believe the nil case was handled
  here. It never was. The doc comment now says so, and says that a third caller
  has to answer for nil itself.

  Found by r4f4c reviewing PR #129 and deliberately not folded into it, so the
  merged tree stayed the one the reviewer had verified. Refs drellem2/pogo#127.
