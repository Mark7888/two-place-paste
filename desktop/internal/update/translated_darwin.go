package update

import "golang.org/x/sys/unix"

// translated reports whether this process is an x86_64 one running under
// Rosetta 2. The sysctl does not exist on Intel Macs, which reads as false.
func translated() bool {
	v, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && v == 1
}
