//go:build darwin

package collector

import (
	"os"
	"testing"
)

func TestParseIORegBatteries(t *testing.T) {
	data, err := os.ReadFile("testdata/ioreg_battery_macos.plist")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseIORegBatteries(data)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Batteries) != 1 {
		t.Fatalf("expected one battery: %+v", got)
	}
	b := got.Batteries[0]
	if b.State != "discharging" || b.PluggedIn {
		t.Errorf("state = %q plugged=%v", b.State, b.PluggedIn)
	}
	if b.Percent != 44 {
		t.Errorf("percent = %v, want 44 (what the menu bar shows)", b.Percent)
	}
	if b.CycleCount != 548 {
		t.Errorf("cycles = %d", b.CycleCount)
	}
	// 3766 / 4563 mAh
	if b.HealthPercent < 82.5 || b.HealthPercent > 82.6 {
		t.Errorf("health = %v, want ~82.5", b.HealthPercent)
	}
	if b.PowerWatts < 6.99 || b.PowerWatts > 7.0 {
		t.Errorf("power = %v W, want ~6.99", b.PowerWatts)
	}
	if b.TimeRemainingSeconds != 193*60 {
		t.Errorf("time remaining = %d, want %d", b.TimeRemainingSeconds, 193*60)
	}
	// 1598 mAh * 11.426 V
	if b.CurrentCapacityMWh < 18250 || b.CurrentCapacityMWh > 18270 {
		t.Errorf("current capacity = %v mWh", b.CurrentCapacityMWh)
	}
}

func TestParseIORegBatteriesNoBattery(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><array/></plist>`)
	got, err := parseIORegBatteries(data)
	if err == nil || got.Available {
		t.Fatalf("a Mac mini must report no battery, got %+v err=%v", got, err)
	}
}
