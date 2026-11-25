//go:build darwin || linux || android

package notebook

import (
	"errors"
	"math"
	"path/filepath"
	"syscall"
)

func availableSpace(dir string) (int64, error) {
	cur := filepath.Clean(dir)
	var st syscall.Statfs_t
	for {
		if err := syscall.Statfs(cur, &st); err == nil {
			if st.Bavail <= 0 || st.Bsize <= 0 {
				return 0, nil
			}
			bavail := uint64(st.Bavail)
			bsize := uint64(st.Bsize)
			if bavail > uint64(math.MaxInt64)/bsize {
				return math.MaxInt64, nil
			}
			return int64(bavail * bsize), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return 0, syscall.ENOENT
		}
		cur = parent
	}
}

func isNoSpaceError(err error) bool {
	return errors.Is(err, syscall.ENOSPC)
}
