package blob

import "io/fs"

// CountOps wraps a disk backend's filesystem operations with counters and
// returns pointers to them. It exists so that a test can prove the sweep is
// O(buckets) rather than O(blobs) (SPEC §4.5) by measuring the operations it
// performs instead of trusting the implementation to be what it says it is.
func CountOps(d *Disk) (readDirs, removeAlls *int) {
	readDirs, removeAlls = new(int), new(int)

	readDir, removeAll := d.readDir, d.removeAll
	d.readDir = func(name string) ([]fs.DirEntry, error) {
		*readDirs++
		return readDir(name)
	}
	d.removeAll = func(path string) error {
		*removeAlls++
		return removeAll(path)
	}
	return readDirs, removeAlls
}
