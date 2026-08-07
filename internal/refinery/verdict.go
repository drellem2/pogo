package refinery

import (
	"encoding/json"
	"fmt"
)

// ValidateVerdict checks that an author-supplied verdict is a non-empty JSON
// object (mg-dfea).
//
// The shape is constrained for one reason: the verdict is carried into the work
// item's result sidecar, which is itself a JSON object, and a reader of that
// sidecar has to be able to ask it for named fields. A bare string or array
// would be storable but not answerable — "did this branch pass its own author's
// checks" has no key to read.
//
// Emptiness is rejected rather than accepted-and-ignored because `{}` is
// exactly the failure this field exists to end: a result that closes an item
// while recording no verdict at all. Passing it is far more likely to be a
// broken shell expansion than a deliberate statement, and the author is still
// alive at submit time to be told.
//
// Nothing beyond that is checked. The refinery does not require a `verdict`
// key, does not enumerate legal values, and does not read the contents — a
// merge queue is not the right actor to rule on what a worker concluded.
func ValidateVerdict(raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("must be a JSON object: %w", err)
	}
	if obj == nil {
		return fmt.Errorf("must be a JSON object, got null")
	}
	if len(obj) == 0 {
		return fmt.Errorf("is an empty object, which records nothing — omit it, or state what you concluded")
	}
	return nil
}
