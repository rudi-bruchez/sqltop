package mssql

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestCaptureDDLNeverStallsTheWorkload(t *testing.T) {
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if strings.Contains(strings.ToUpper(ddl), "NO_EVENT_LOSS") {
		t.Fatal("the capture DDL asks the engine to block the monitored workload when the buffer fills")
	}
	if !strings.Contains(ddl, "ALLOW_SINGLE_EVENT_LOSS") {
		t.Error("the retention mode is not stated, so it defaults rather than being chosen")
	}
}

func TestCaptureDDLStatesBothRingBufferCaps(t *testing.T) {
	// Measured on 2019 and 2022 at 1024 KB and again at 4096 KB: a target
	// naming only MAX_MEMORY holds exactly 1000 events, because the event
	// limit defaults and governs. The memory figure alone describes a
	// buffer the feature never receives.
	ddl := fmt.Sprintf(createCaptureQueryTemplate, "sqltop_capture_51_a3f2c9d1", 51, 51)
	if !strings.Contains(ddl, "MAX_EVENTS_LIMIT = 1000") {
		t.Error("the event count cap is left implicit, so the default governs silently")
	}
	if !strings.Contains(ddl, "MAX_MEMORY = 1024") {
		t.Error("the ring buffer target memory cap is missing")
	}
	if !strings.Contains(ddl, "STARTUP_STATE = OFF") {
		t.Error("without STARTUP_STATE = OFF a leftover session returns after a server restart")
	}
}

func TestCaptureSessionNameIsPrefixedAndInert(t *testing.T) {
	ok := regexp.MustCompile(`^sqltop_capture_[0-9]+_[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := captureSessionName(51)
		if err != nil {
			t.Fatal(err)
		}
		if !ok.MatchString(name) {
			t.Fatalf("name %q is not the prefix, an integer and hex", name)
		}
		seen[name] = true
	}
	if len(seen) < 90 {
		t.Errorf("only %d distinct names in 100; the suffix is not random enough", len(seen))
	}
}

func TestTheSweepComparesTimesOnTheSameClock(t *testing.T) {
	// The defect this test exists for shipped green on every UTC container
	// and would have destroyed a colleague's live capture on any server west
	// of Greenwich. sys.dm_xe_sessions.create_time is local server time;
	// comparing it to SYSUTCDATETIME() makes every session look hours old.
	if strings.Contains(strings.ToUpper(sweepCaptureQueryTemplate), "SYSUTCDATETIME") {
		t.Fatal("the sweep compares a local-time column to UTC; west of Greenwich it drops live captures, east of it leaves dead ones")
	}
	if !strings.Contains(strings.ToUpper(sweepCaptureQueryTemplate), "SYSDATETIME") {
		t.Error("the age comparison must be made on the server, against the same clock as create_time")
	}
	if !strings.Contains(sweepCaptureQueryTemplate, capturePrefix+"%") {
		t.Error("the sweep does not filter on the prefix, so it can see other people's event sessions")
	}
}
