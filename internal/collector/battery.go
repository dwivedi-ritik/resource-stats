package collector

import (
	"errors"
	"strings"
	"time"

	"github.com/distatus/battery"
)

// BatteryStats describes every battery the machine reports. Desktops and
// most servers have none, in which case Available is false and the UI hides
// the card entirely.
type BatteryStats struct {
	Available bool      `json:"available"`
	Batteries []Battery `json:"batteries"`
}

// Battery is one battery's current state. Capacities are in milliwatt-hours,
// power in watts.
type Battery struct {
	// State is one of charging, discharging, full, idle, empty, unknown.
	State              string  `json:"state"`
	PluggedIn          bool    `json:"plugged_in"`
	Percent            float64 `json:"percent"`
	CurrentCapacityMWh float64 `json:"current_capacity_mwh"`
	FullCapacityMWh    float64 `json:"full_capacity_mwh"`
	DesignCapacityMWh  float64 `json:"design_capacity_mwh"`
	// HealthPercent is full capacity as a share of the design capacity.
	HealthPercent float64 `json:"health_percent"`
	// PowerWatts is the charge or discharge rate, always positive.
	PowerWatts float64 `json:"power_watts"`
	Voltage    float64 `json:"voltage"`
	// TimeRemainingSeconds is until empty when discharging and until full
	// when charging; zero when it cannot be estimated.
	TimeRemainingSeconds uint64 `json:"time_remaining_seconds"`
	// CycleCount is zero when the platform does not report it.
	CycleCount int `json:"cycle_count"`
}

// batteryReader caches readings because battery state changes slowly and,
// on macOS, each read spawns an ioreg process.
type batteryReader struct {
	every  time.Duration
	lastAt time.Time
	last   BatteryStats
}

func newBatteryReader() *batteryReader {
	// WHY: 5 seconds is fast enough to notice unplugging promptly while
	// keeping the per-second sampling loop free of process spawns.
	return &batteryReader{every: 5 * time.Second}
}

func (r *batteryReader) read(now time.Time) BatteryStats {
	if !r.lastAt.IsZero() && now.Sub(r.lastAt) < r.every {
		return r.last
	}
	r.lastAt = now
	r.last = readBatteriesNative()
	return r.last
}

// readBatteries converts library readings into our model. A fatal error or
// an empty list means "no battery here"; a partial error still yields the
// fields that were readable.
func readBatteries(getAll func() ([]*battery.Battery, error)) BatteryStats {
	out := BatteryStats{Batteries: []Battery{}}
	bats, err := getAll()
	var fatal battery.ErrFatal
	if err != nil && errors.As(err, &fatal) {
		return out
	}
	for _, b := range bats {
		if b == nil || (b.Full == 0 && b.Design == 0 && b.Current == 0) {
			continue
		}
		out.Batteries = append(out.Batteries, convertBattery(b))
	}
	out.Available = len(out.Batteries) > 0
	return out
}

func convertBattery(b *battery.Battery) Battery {
	state := strings.ToLower(b.State.Raw.String())
	if state == "undefined" {
		state = "unknown"
	}
	full := b.Full
	if full == 0 {
		full = b.Design
	}
	out := Battery{
		State:              state,
		PluggedIn:          state == "charging" || state == "full" || state == "idle",
		CurrentCapacityMWh: b.Current,
		FullCapacityMWh:    b.Full,
		DesignCapacityMWh:  b.Design,
		PowerWatts:         b.ChargeRate / 1000,
		Voltage:            b.Voltage,
	}
	if full > 0 {
		out.Percent = clampPercent(b.Current / full * 100)
	}
	if b.Design > 0 && b.Full > 0 {
		out.HealthPercent = clampPercent(b.Full / b.Design * 100)
	}
	if b.ChargeRate > 0 {
		var hours float64
		switch state {
		case "discharging":
			hours = b.Current / b.ChargeRate
		case "charging":
			hours = (full - b.Current) / b.ChargeRate
		}
		if hours > 0 {
			out.TimeRemainingSeconds = uint64(hours * 3600)
		}
	}
	return out
}

func clampPercent(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
