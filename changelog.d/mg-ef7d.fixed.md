The witness store's polecat-only readers no longer depend on each reader
remembering to filter. `loadWitness` is split into `loadWitnessAllTypes` and
`loadPolecatWitness` (`internal/agent/witness.go`), the five polecat-only
readers call the filtered one, and a new AST lint test fails when a caller of
the unfiltered loader appears that has not declared itself.

**The defect was structural, and mg-f9e8 explicitly left it open.** That change
gave `witnessRecord` a `Type` and routed the six readers that mean "the
polecats" literally through one `isPolecat()` helper — the right fix for the
readers that existed. What it could not fix is the seventh. The entire
polecat-only invariant was enforced at ONE write site (`noteWitnessStart`'s
early return, before mg-f9e8 widened it) and checked at NONE of the readers:
each was correct by inheritance, and inheritance is not checked by anything.
mg-f9e8's own guard test enumerates the six by hand, which is precisely the
shape that does not scale — a reader added tomorrow is absent from a hand-written
list, and absence there reads as "fine".

The cost of the near-miss is what makes this worth a guard rather than a
comment. The change originally sketched on mg-f9e8 was "write witnesses for crew
too", full stop. Against unfiltered readers that would have wedged every
redeploy permanently (the drain waits for `alive_count` to reach zero and crew
never exit), mailed the coordinator an authoritative `kill <pid>` per crew row,
and added rows that never clear to gitgc's live set, the per-repo dispatch cap
and stall-watch's in-flight set. One writer changing one early return, five
subsystems, no compile error and no test failure.

**Which property this buys, stated rather than implied — the ticket asked for
it.** Go has no encapsulation below the package, so this is NOT a compiler
guard and does not claim to be: anything in `package agent` can still call
`loadWitnessAllTypes`, and the stronger option the ticket floated (a distinct
type only a filtered accessor can construct) is bypassable the same way, by
constructing it. What is bought instead:

- **The type question is asked by the identifier at the call site.** There is no
  longer a spelling of the loader that reads as "just give me the records". A
  caller writes `loadWitnessAllTypes` or `loadPolecatWitness`; both name a
  population, and the awkward one is the unfiltered one.
- **The enumeration is INVERTED.** `TestWitnessAllTypesReadersAreDeclared`
  (`witnessreaders_lint_test.go`) parses this package's non-test source and
  enumerates the EXCEPTIONS — the four functions that legitimately span every
  type, each with a written reason — not the readers. A new caller of the
  unfiltered loader is therefore *uncovered-and-failing* rather than
  *uncovered-and-silent*. That polarity is the whole point; a hand-enumeration
  of readers is what we already had.
- **The filter lives in one place.** `isPolecat` is called only by
  `loadPolecatWitness` now, pinned by `TestIsPolecatHasExactlyOneCaller`. A
  reader filtering inline is a reader that took the unfiltered load and then
  remembered — and remembering is what the six readers were relying on.
- **The cheap bypass is closed.** `TestWitnessOnDiskShapeIsNotReadOutsideTheLoader`
  fails if anything but the loader and the writer names `witnessOnDisk`, so a
  reader cannot skip the accessors by unmarshalling the file itself. A
  from-scratch reimplementation of the parse is still possible and is not
  defended against; that limit is stated in the source.

**The new behavioural test is not a duplicate of mg-f9e8's, and this was
measured.** `TestAThirdWitnessTypeIsInvisibleToThePolecatReaders` writes a record
of a type nobody has written a reader for (`AgentType("reviewer")`) alongside a
polecat positive control and a typeless legacy record, and asserts all six
readers admit POLECATS rather than merely rejecting crew. Rewriting `isPolecat`
as `r.Type != TypeCrew` — a filter that is wrong in exactly the way that matters
— leaves `TestCrewWitnessIsInvisibleToThePolecatReaders` **passing** and makes
the new test fail on all six readers. The store's population has already been
widened once by one line; the next type must be excluded on the day it is added,
before anyone writes a reader for it.

**The migration property mg-f9e8 flagged is preserved and re-pinned.** An empty
`Type` still reads as polecat — every record a pre-mg-f9e8 pogod left on disk has
no type key and all of them are polecats. Dropping them would take a redeploy's
survivors out of gitgc's live set, and worktree removal is gated on that set
ALONE. The new test carries a typeless record through all six readers so the two
facts (unknown type excluded, absent type included) are asserted against each
other rather than in separate files: "unknown" and "absent" are different facts,
which is this package's whole subject.

**The guard was observed going red for each thing it claims to catch**, since a
guard that has only ever been green is a guard nobody has tested: a seventh
reader calling the unfiltered loader (lint fails, naming the function), a
`!= TypeCrew` filter (third-type test fails, crew test passes), a
`== TypePolecat` filter (typeless survivors dropped), and — applying the lint to
itself — renaming `loadWitnessAllTypes` out from under it, which trips the
vacuity control rather than passing silently. That last one matters most: an AST
lint that greps for a name nothing has passes while enforcing nothing, so every
way this check can quietly stop checking is asserted against.

No production behaviour changes. The readers return exactly what they returned
before; what changed is what happens when someone writes the next one.
