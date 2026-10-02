package documents

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"strings"
)

func processRSS(pid int) (uint64, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if e != nil {
		return 0, e
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, fmt.Errorf("Invalid process memory record")
	}
	pages, e := strconv.ParseUint(fields[1], 10, 64)
	if e != nil {
		return 0, e
	}
	return pages * uint64(os.Getpagesize()), nil
}
func workerMemoryLimit() error {
	return unix.Setrlimit(unix.RLIMIT_DATA, &unix.Rlimit{Cur: workerRSS, Max: workerRSS})
}
