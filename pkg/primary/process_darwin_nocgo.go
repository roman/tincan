//go:build darwin && !cgo

package primary

import "errors"

func readProcess(pid int) (Process, error) {
	return Process{}, errors.New("Darwin process identity requires a build with Cgo and libproc")
}
