// Package lock guarantees ONE running agent per machine.
//
// Two agents against one facility corrupt delta formats: each commits a
// last-roster.json the other never emitted, so the vendor file receives wrong
// add/remove ops. The classic way this happens in the field: the Windows
// service is running and a tech ALSO starts `unitrise-gate run` in a console
// to "watch it work". The second copy must refuse, loudly and helpfully.
//
// Windows: a named Global\ mutex (visible across sessions, vanishes with the
// process - no stale state possible). Unix: flock on <config dir>/agent.lock
// (released by the kernel on exit, so crashes never leave a stale lock).
package lock

import "fmt"

// ErrHeld is wrapped in the error returned when another agent holds the lock.
type AlreadyRunningError struct{}

func (AlreadyRunningError) Error() string {
	return "another UnitRise Gate Bridge is already running on this machine " +
		"(usually the installed service). Watch it with `unitrise-gate ui`, " +
		"or stop it first: `unitrise-gate service stop`"
}

// Acquire takes the machine-wide single-instance lock. On success it returns
// a release func (also released automatically when the process exits). When
// another agent holds it, the error explains what to do.
func Acquire() (func(), error) {
	rel, err := acquire()
	if err != nil {
		return nil, fmt.Errorf("single-instance check: %w", err)
	}
	return rel, nil
}
