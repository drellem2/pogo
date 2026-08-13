package agent_test

import (
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/wedgewatch"
)

// TestOutputCeilingCoversTheDetectorWindow pins the relationship mg-8a56 was
// filed about: whatever window pogod's wedge detector judges an agent on must
// be retrievable through GET /agents/{name}/output.
//
// It lives in package agent_test because internal/wedgewatch imports
// internal/agent — an in-package test naming both would be an import cycle.
// The 16*1024 literal in api_output_test.go's DetectorWindowBytes is pinned
// here, so raising OutputScanBytes past the ring fails a test that says why
// rather than silently restoring the ceiling.
func TestOutputCeilingCoversTheDetectorWindow(t *testing.T) {
	if wedgewatch.OutputScanBytes > agent.OutputRingBytes {
		t.Fatalf("wedgewatch scans %d bytes but the ring only retains %d: no HTTP caller "+
			"can reproduce what the detector judged on, which is mg-8a56 restored",
			wedgewatch.OutputScanBytes, agent.OutputRingBytes)
	}
	if agent.DefaultOutputBytes >= wedgewatch.OutputScanBytes {
		t.Logf("the endpoint's default (%d) now covers the detector window (%d); "+
			"?bytes= is no longer load-bearing for wedge diagnosis",
			agent.DefaultOutputBytes, wedgewatch.OutputScanBytes)
	}
	if agent.DetectorWindowBytes != wedgewatch.OutputScanBytes {
		t.Fatalf("api_output_test.go restates the detector window as %d, but "+
			"wedgewatch.OutputScanBytes is %d — update the literal",
			agent.DetectorWindowBytes, wedgewatch.OutputScanBytes)
	}
}
