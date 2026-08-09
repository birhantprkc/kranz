package ui

import (
	"testing"
	"time"
)

func TestMouseTrackingRefreshIsRateLimited(t *testing.T) {
	model := newTestModel()
	defer model.Shutdown()

	now := time.Now()
	if command := model.refreshMouseTracking(now); command == nil {
		t.Fatal("initial mouse tracking refresh was not scheduled")
	}
	if command := model.refreshMouseTracking(now.Add(mouseTrackingRefreshInterval / 2)); command != nil {
		t.Fatal("mouse tracking refreshed before the watchdog interval elapsed")
	}
	if command := model.refreshMouseTracking(now.Add(mouseTrackingRefreshInterval)); command == nil {
		t.Fatal("mouse tracking was not refreshed after the watchdog interval")
	}
}
