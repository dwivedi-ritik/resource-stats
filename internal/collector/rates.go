package collector

import "time"

// ratePerSecond turns two cumulative counter readings into a per-second rate.
// Counters can reset (interface re-created, container restarted) which would
// make cur < prev; that is reported as zero rather than a huge negative spike.
func ratePerSecond(prev, cur uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 || cur < prev {
		return 0
	}
	return float64(cur-prev) / elapsed.Seconds()
}

// percent guards against a zero denominator and clamps to [0, 100].
func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	p := float64(used) / float64(total) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
