//go:build darwin && cgo

package primary

/*
#cgo LDFLAGS: -lproc
#include <libproc.h>
#include <sys/proc_info.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func readProcess(pid int) (Process, error) {
	if pid < 1 || int(C.int(pid)) != pid {
		return Process{}, errors.New("invalid process ID")
	}
	var before C.struct_proc_bsdinfo
	size := C.int(unsafe.Sizeof(before))
	count, err := C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&before), size)
	if count != size {
		return Process{}, processIdentityError(pid, err)
	}
	var path [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	count, err = C.proc_pidpath(C.int(pid), unsafe.Pointer(&path[0]), C.uint32_t(len(path)))
	if count <= 0 {
		return Process{}, processIdentityError(pid, err)
	}
	executable := C.GoString(&path[0])
	var after C.struct_proc_bsdinfo
	count, err = C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&after), size)
	if count != size {
		return Process{}, processIdentityError(pid, err)
	}
	if int(before.pbi_pid) != pid || before.pbi_pid != after.pbi_pid ||
		before.pbi_ppid != after.pbi_ppid || before.pbi_start_tvsec != after.pbi_start_tvsec ||
		before.pbi_start_tvusec != after.pbi_start_tvusec || before.pbi_start_tvsec == 0 ||
		before.pbi_start_tvusec >= 1000000 || !filepath.IsAbs(executable) {
		return Process{}, fmt.Errorf("process %d identity changed or is incomplete", pid)
	}
	return Process{PID: pid, ParentPID: int(before.pbi_ppid), Kind: classifyExecutable(executable),
		StartTicks: fmt.Sprintf(
			"%d:%d",
			uint64(before.pbi_start_tvsec),
			uint64(before.pbi_start_tvusec),
		)}, nil
}

func processIdentityError(pid int, err error) error {
	if errors.Is(err, syscall.ESRCH) {
		err = os.ErrNotExist
	}
	if err == nil {
		err = errors.New("incomplete native identity data")
	}
	return fmt.Errorf("process %d identity is unavailable: %w", pid, err)
}
