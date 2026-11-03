package main

import (
	"fmt"

	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
)

// postMergeTagLine is the MERGED mail's line for a tag that was made. A git
// MR's line is exactly what it always was; an enth self-land's tag was made
// on enthd by pogod at `pogo land` (slice 12, mg-ae4ac), and names the
// Change, never as a git sha.
func postMergeTagLine(mr *refinery.MergeRequest) string {
	if k, _ := mr.Landed(); k == vcs.Enth {
		return fmt.Sprintf("\nPost-merge tag: %s created on enthd at %s (made by pogod when the land was recorded, not by the polecat).", mr.PostMergeTag, mr.LandedLabel(0))
	}
	return fmt.Sprintf("\nPost-merge tag: %s pushed to origin at %s (performed by the refinery, not the polecat).", mr.PostMergeTag, mr.MergedSHA)
}
