package collector

import (
	"errors"
	"testing"

	"github.com/distatus/battery"
)

func TestReadBatteriesNoBatteryIsUnavailable(t *testing.T) {
	got := readBatteries(func() ([]*battery.Battery, error) {
		return nil, battery.ErrFatal{Err: errors.New("no batteries")}
	})
	if got.Available || len(got.Batteries) != 0 {
		t.Fatalf("fatal error must mean no battery: %+v", got)
	}
	got = readBatteries(func() ([]*battery.Battery, error) { return []*battery.Battery{}, nil })
	if got.Available {
		t.Fatal("empty list must mean no battery")
	}
}

func TestReadBatteriesDischarging(t *testing.T) {
	b := &battery.Battery{Current: 30000, Full: 50000, Design: 60000, ChargeRate: 10000, Voltage: 11.4}
	b.State.Raw = battery.Discharging
	got := readBatteries(func() ([]*battery.Battery, error) { return []*battery.Battery{b}, nil })
	if !got.Available || len(got.Batteries) != 1 {
		t.Fatalf("expected one battery: %+v", got)
	}
	bat := got.Batteries[0]
	if bat.State != "discharging" || bat.PluggedIn {
		t.Errorf("state = %q plugged=%v", bat.State, bat.PluggedIn)
	}
	if bat.Percent != 60 {
		t.Errorf("percent = %v, want 60", bat.Percent)
	}
	if bat.HealthPercent < 83.3 || bat.HealthPercent > 83.4 {
		t.Errorf("health = %v, want ~83.3", bat.HealthPercent)
	}
	if bat.PowerWatts != 10 {
		t.Errorf("power = %v W, want 10", bat.PowerWatts)
	}
	// 30 Wh left at 10 W = 3 hours
	if bat.TimeRemainingSeconds != 3*3600 {
		t.Errorf("time remaining = %d s, want 10800", bat.TimeRemainingSeconds)
	}
}

func TestReadBatteriesChargingAndPartialError(t *testing.T) {
	b := &battery.Battery{Current: 40000, Full: 50000, Design: 50000, ChargeRate: 20000}
	b.State.Raw = battery.Charging
	got := readBatteries(func() ([]*battery.Battery, error) {
		return []*battery.Battery{b}, battery.ErrPartial{Voltage: errors.New("not readable")}
	})
	if !got.Available {
		t.Fatal("a partial error still yields a battery")
	}
	bat := got.Batteries[0]
	if !bat.PluggedIn || bat.State != "charging" {
		t.Errorf("state = %q plugged=%v", bat.State, bat.PluggedIn)
	}
	// 10 Wh to go at 20 W = 30 minutes
	if bat.TimeRemainingSeconds != 1800 {
		t.Errorf("time to full = %d s, want 1800", bat.TimeRemainingSeconds)
	}
	if bat.HealthPercent != 100 {
		t.Errorf("health = %v, want 100", bat.HealthPercent)
	}
}

func TestReadBatteriesFullHasNoEstimate(t *testing.T) {
	b := &battery.Battery{Current: 50000, Full: 50000, Design: 55000, ChargeRate: 0}
	b.State.Raw = battery.Full
	got := readBatteries(func() ([]*battery.Battery, error) { return []*battery.Battery{b}, nil })
	bat := got.Batteries[0]
	if bat.TimeRemainingSeconds != 0 || !bat.PluggedIn || bat.Percent != 100 {
		t.Fatalf("full battery: %+v", bat)
	}
}
