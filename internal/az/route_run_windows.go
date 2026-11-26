package az

import (
	"errors"

	"golang.org/x/sys/windows"
)

// A reused PID only keeps a leftover overlay longer. A process that this
// user may not open still exists.
func routeRunProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	event, err := windows.WaitForSingleObject(process, 0)
	return err != nil || event == uint32(windows.WAIT_TIMEOUT)
}
