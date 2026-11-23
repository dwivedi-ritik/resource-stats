package collector

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

// pseudoFilesystems never hold user data; showing them would just be noise
// (and on Linux there are dozens of them).
var pseudoFilesystems = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "devfs": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "overlay": true, "squashfs": true,
	"autofs": true, "mqueue": true, "hugetlbfs": true, "debugfs": true,
	"tracefs": true, "securityfs": true, "pstore": true, "bpf": true,
	"configfs": true, "fusectl": true, "binfmt_misc": true, "nsfs": true,
	"rpc_pipefs": true, "efivarfs": true, "ramfs": true, "fuse.portal": true,
	"fuse.gvfsd-fuse": true, "fuse.lxcfs": true, "nullfs": true, "iso9660": true,
}

// macOS mounts several internal APFS volumes that share the main container;
// only the Data volume is meaningful to a person.
var hiddenMountPrefixes = []string{
	"/System/Volumes/VM", "/System/Volumes/Preboot", "/System/Volumes/Update",
	"/System/Volumes/xarts", "/System/Volumes/iSCPreboot", "/System/Volumes/Hardware",
	"/private/var/vm", "/boot/efi", "/snap/",
}

// selectPartitions keeps the filesystems worth showing: real block/network
// filesystems, deduplicated when one device is mounted at several points.
func selectPartitions(parts []disk.PartitionStat) []disk.PartitionStat {
	seen := map[string]bool{}
	var out []disk.PartitionStat
	for _, p := range parts {
		if pseudoFilesystems[strings.ToLower(p.Fstype)] {
			continue
		}
		if hiddenMount(p.Mountpoint) {
			continue
		}
		// WHY: bind mounts and APFS firmlinks expose the same device twice;
		// keep the first mountpoint (gopsutil lists them in mount order).
		key := p.Device
		if key == "" || key == "none" {
			key = p.Mountpoint
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

func hiddenMount(mount string) bool {
	for _, prefix := range hiddenMountPrefixes {
		if strings.HasPrefix(mount, prefix) {
			return true
		}
	}
	return false
}

// usagePath maps a host mountpoint to the path we must stat. When running in
// a container with the host root bind-mounted (STATS_LITE_HOST_ROOT=/host),
// "/" on the host is "/host" for us.
func usagePath(hostRoot, mountpoint string) string {
	if hostRoot == "" {
		return mountpoint
	}
	return filepath.Join(hostRoot, mountpoint)
}
