//go:build !darwin

package collector

import "github.com/distatus/battery"

// readBatteriesNative uses the cross-platform library: sysfs on Linux and the
// Win32 power API on Windows, neither of which needs cgo.
func readBatteriesNative() BatteryStats {
	return readBatteries(battery.GetAll)
}
