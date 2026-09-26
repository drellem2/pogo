package main

import "github.com/drellem2/pogo/internal/carrierdrift"

// newCarrierDriftSource is the store source the carrier re-read watcher scans
// with — the sibling of newIntakeCarrierSource, and a function for the same
// reason: main() is not reachable from a test, and the one property that
// matters here is invisible in a review.
//
// That property is the Cache. It lives as long as the watcher, so a pass forks
// `mg show` only for live items whose file changed since the last one
// (drellem2/pogo#179, applied to carrierdrift by mg-e353). Dropping it compiles,
// passes every carrierdrift test, and silently returns every pass to one fork
// per live item. TestCarrierDriftSourceCachesAcrossPasses pins it.
func newCarrierDriftSource(includeShelved bool) carrierdrift.MGSource {
	return carrierdrift.MGSource{IncludeShelved: includeShelved, Cache: carrierdrift.NewItemCache()}
}
