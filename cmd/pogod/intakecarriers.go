package main

import "github.com/drellem2/pogo/internal/ghintake"

// newIntakeCarrierSource is the carrier source the gh-issue intake watcher scans
// the store with. It is a function rather than a literal in main() for the same
// reason decideIntakeArming is (see intakearming.go): main() is not reachable
// from a test, and the one property that matters here is invisible in a review.
//
// That property is the Cache. It lives as long as the watcher, so a pass forks
// `mg show` only for items whose file changed since the last one
// (drellem2/pogo#179: 88% of the live store is archived and never changes).
// Dropping it compiles, passes every ghintake test, and silently returns every
// 15-minute pass to ~4,000 forks. TestIntakeCarrierSourceCachesAcrossPasses
// pins it.
func newIntakeCarrierSource() ghintake.MGSource {
	return ghintake.MGSource{Cache: ghintake.NewRefCache()}
}
