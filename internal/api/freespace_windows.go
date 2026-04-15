//go:build windows

package api

func statFreeSpace(_ string) (int64, error) {
	return 0, nil
}
