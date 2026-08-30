// Package logging: console + rotating-ish file log (the agent runs for years
// unattended on a site PC — the log must never eat the disk).
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxLogBytes = 5 << 20 // 5MB, then rotate once to .old

type Logger struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func New(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{path: path, f: f}, nil
}

// Hook receives every rendered log line (the local dashboard's log pane).
var Hook func(line string)

func (l *Logger) write(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("%s:<%s>: %s\n", level, time.Now().Format("1/2/2006 3:04:05 PM"), fmt.Sprintf(format, args...))
	fmt.Fprint(os.Stdout, line)
	if Hook != nil {
		Hook(strings.TrimRight(line, "\n"))
	}
	if l.f != nil {
		l.f.WriteString(line)
		if st, err := l.f.Stat(); err == nil && st.Size() > maxLogBytes {
			l.f.Close()
			os.Rename(l.path, l.path+".old") // best-effort single rotation
			l.f, _ = os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		}
	}
}

func (l *Logger) Infof(format string, args ...any)  { l.write("Information", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.write("Error", format, args...) }
func (l *Logger) Debugf(format string, args ...any) { l.write("Debug", format, args...) }

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
	}
}
