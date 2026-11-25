package notebook

import (
	"errors"
	"fmt"
	"math"
)

const minFreeSpaceReserve int64 = 64 << 20

var errNoDiskSpace = errors.New("设备存储空间不足，无法导入该文件")

var availableSpaceFn = availableSpace

func formatSpaceBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", max(int64(0), n))
	}
}

func checkAvailableSpace(dir string, needed int64) error {
	if needed <= 0 {
		return nil
	}
	avail, err := availableSpaceFn(dir)
	if err != nil {
		return nil
	}
	if needed > math.MaxInt64-minFreeSpaceReserve {
		return errNoDiskSpace
	}
	required := needed + minFreeSpaceReserve
	if avail < required {
		return fmt.Errorf("%w（至少需要 %s，当前剩余 %s）", errNoDiskSpace, formatSpaceBytes(required), formatSpaceBytes(avail))
	}
	return nil
}

func mapDiskSpaceError(err error) error {
	if err == nil {
		return nil
	}
	if isNoSpaceError(err) {
		return errNoDiskSpace
	}
	return err
}
