package documents

import (
	"golang.org/x/sys/unix"
	"unsafe"
)

func processRSS(pid int) (uint64, error) {
	// Darwin's stable PROC_PIDTASKINFO ABI: six uint64s and twelve int32s.
	var info struct {
		Virtual, Resident, User, System, ThreadsUser, ThreadsSystem uint64
		Counters                                                    [12]int32
	}
	n, _, e := unix.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), 4, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if e != 0 {
		return 0, e
	}
	if n != unsafe.Sizeof(info) {
		return 0, unix.ESRCH
	}
	return info.Resident, nil
}
func workerMemoryLimit() error { return nil } // macOS RLIMIT_DATA does not constrain Go's mmap heap.
