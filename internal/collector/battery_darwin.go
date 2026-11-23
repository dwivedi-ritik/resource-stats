//go:build darwin

package collector

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/distatus/battery"
	"howett.net/plist"
)

// ioregBattery is the subset of the AppleSmartBattery registry entry we use.
// On Apple Silicon the top-level CurrentCapacity/MaxCapacity are percentages
// and the real milliamp-hour figures live under BatteryData, which is why the
// generic library reports zero capacity on modern Macs.
type ioregBattery struct {
	BatteryInstalled  bool `plist:"BatteryInstalled"`
	CurrentCapacity   int  `plist:"CurrentCapacity"`
	MaxCapacity       int  `plist:"MaxCapacity"`
	Voltage           int  `plist:"Voltage"`  // millivolts
	Amperage          int  `plist:"Amperage"` // milliamps, negative when discharging
	IsCharging        bool `plist:"IsCharging"`
	ExternalConnected bool `plist:"ExternalConnected"`
	FullyCharged      bool `plist:"FullyCharged"`
	CycleCount        int  `plist:"CycleCount"`
	AvgTimeToEmpty    int  `plist:"AvgTimeToEmpty"` // minutes, 65535 = unknown
	AvgTimeToFull     int  `plist:"AvgTimeToFull"`  // minutes, 65535 = unknown
	BatteryData       struct {
		RemainingCapacity  int `plist:"RemainingCapacity"`  // mAh
		FullChargeCapacity int `plist:"FullChargeCapacity"` // mAh
		DesignCapacity     int `plist:"DesignCapacity"`     // mAh
		BatteryPower       int `plist:"BatteryPower"`       // mW, negative when discharging
	} `plist:"BatteryData"`
}

// unknownMinutes is what the SMC reports when it has no estimate.
const unknownMinutes = 65535

func readBatteriesNative() BatteryStats {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ioreg", "-n", "AppleSmartBattery", "-r", "-a").Output()
	if err == nil {
		if stats, perr := parseIORegBatteries(out); perr == nil {
			return stats
		}
	}
	// Older Intel Macs expose the classic keys the library understands.
	return readBatteries(battery.GetAll)
}

func parseIORegBatteries(data []byte) (BatteryStats, error) {
	out := BatteryStats{Batteries: []Battery{}}
	var entries []ioregBattery
	if _, err := plist.Unmarshal(data, &entries); err != nil {
		return out, err
	}
	for _, e := range entries {
		if !e.BatteryInstalled && e.MaxCapacity == 0 {
			continue
		}
		out.Batteries = append(out.Batteries, convertIORegBattery(e))
	}
	if len(out.Batteries) == 0 {
		return out, errors.New("ioreg listed no installed battery")
	}
	out.Available = true
	return out, nil
}

func convertIORegBattery(e ioregBattery) Battery {
	volts := float64(e.Voltage) / 1000
	// mAh * V = mWh
	mwh := func(mah int) float64 { return float64(mah) * volts }
	b := Battery{
		PluggedIn:          e.ExternalConnected,
		CycleCount:         e.CycleCount,
		Voltage:            volts,
		CurrentCapacityMWh: mwh(e.BatteryData.RemainingCapacity),
		FullCapacityMWh:    mwh(e.BatteryData.FullChargeCapacity),
		DesignCapacityMWh:  mwh(e.BatteryData.DesignCapacity),
	}
	switch {
	case e.FullyCharged:
		b.State = "full"
	case e.IsCharging:
		b.State = "charging"
	case e.ExternalConnected:
		b.State = "idle"
	default:
		b.State = "discharging"
	}
	// WHY: use the percentage macOS itself displays rather than the raw
	// mAh ratio, so the number matches the menu bar.
	if e.MaxCapacity > 0 {
		b.Percent = clampPercent(float64(e.CurrentCapacity) / float64(e.MaxCapacity) * 100)
	}
	if e.BatteryData.DesignCapacity > 0 && e.BatteryData.FullChargeCapacity > 0 {
		b.HealthPercent = clampPercent(float64(e.BatteryData.FullChargeCapacity) / float64(e.BatteryData.DesignCapacity) * 100)
	}
	power := e.BatteryData.BatteryPower
	if power == 0 {
		power = e.Amperage * e.Voltage / 1000
	}
	if power < 0 {
		power = -power
	}
	b.PowerWatts = float64(power) / 1000
	switch b.State {
	case "discharging":
		if e.AvgTimeToEmpty > 0 && e.AvgTimeToEmpty != unknownMinutes {
			b.TimeRemainingSeconds = uint64(e.AvgTimeToEmpty) * 60
		}
	case "charging":
		if e.AvgTimeToFull > 0 && e.AvgTimeToFull != unknownMinutes {
			b.TimeRemainingSeconds = uint64(e.AvgTimeToFull) * 60
		}
	}
	return b
}
