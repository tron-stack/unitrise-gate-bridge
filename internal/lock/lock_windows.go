//go:build windows

package lock

import (
	"golang.org/x/sys/windows"
)

// A Global\ named mutex spans sessions (session 0 service vs an interactive
// console) and is destroyed by the OS with the owning process - the exact
// semantics a single-instance guard needs.
const mutexName = `Global\UnitRiseGateBridge`

func acquire() (func(), error) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		// ERROR_ALREADY_EXISTS arrives as the error even when a handle is
		// returned - either way, someone else owns the instance.
		if err == windows.ERROR_ALREADY_EXISTS || err == windows.ERROR_ACCESS_DENIED {
			if h != 0 {
				windows.CloseHandle(h)
			}
			return nil, AlreadyRunningError{}
		}
		return nil, err
	}
	return func() {
		windows.ReleaseMutex(h)
		windows.CloseHandle(h)
	}, nil
}
