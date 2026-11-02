package gitvcs

import (
	"context"

	"github.com/drellem2/pogo/internal/vcs"
)

// Placeholders for the three vcs.Workspaces methods no slice has built yet, so
// that Workspaces satisfies the interface and internal/agent can hold it as a
// vcs.Workspaces. Nothing calls them. Slice 3 (mg-d72cb) confirmed it does not
// implement them on this type; whichever slice does should replace this file.

// Remove is not implemented by the git backend yet.
func (Workspaces) Remove(context.Context, string) error { return vcs.ErrUnsupported }

// Dirty is not implemented by the git backend yet.
func (Workspaces) Dirty(context.Context, string) ([]string, error) { return nil, vcs.ErrUnsupported }

// Describe is not implemented by the git backend yet.
func (Workspaces) Describe(context.Context, string) (vcs.Workspace, error) {
	return vcs.Workspace{}, vcs.ErrUnsupported
}
