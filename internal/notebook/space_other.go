//go:build !darwin && !linux && !android

package notebook

import "math"

func availableSpace(string) (int64, error) {
	return math.MaxInt64, nil
}

func isNoSpaceError(error) bool {
	return false
}
