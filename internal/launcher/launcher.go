package launcher

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Launcher manages opencode serve child processes tied to the router's lifetime.
// When the router starts with project paths, the launcher spawns opencode serve
// instances in those directories. On shutdown, it sends SIGTERM to all children.
type Launcher struct {
	portStart int
	portEnd   int
	procs     []*managedProcess
	mu        sync.Mutex
	logger    *slog.Logger
}

type managedProcess struct {
	cmd  *exec.Cmd
	path string
	port int
}

// New creates a Launcher that allocates ports from the given range.
func New(portStart, portEnd int, logger *slog.Logger) *Launcher {
	return &Launcher{
		portStart: portStart,
		portEnd:   portEnd,
		logger:    logger,
	}
}

// Launch starts opencode serve in each directory with an auto-assigned port.
// Directories that don't exist or aren't directories are skipped.
// Already-occupied ports in the range are skipped.
func (l *Launcher) Launch(paths []string) error {
	nextPort := l.portStart

	for _, dir := range paths {
		abs, err := filepath.Abs(dir)
		if err != nil {
			l.logger.Warn("skipping invalid path", "path", dir, "error", err)
			continue
		}

		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			l.logger.Warn("skipping non-directory", "path", abs)
			continue
		}

		// Find next free port in the scan range.
		for portInUse(nextPort) {
			nextPort++
			if nextPort > l.portEnd {
				return fmt.Errorf("no free ports in range %d-%d", l.portStart, l.portEnd)
			}
		}

		cmd := exec.Command("opencode", "serve", "--port", fmt.Sprintf("%d", nextPort))
		cmd.Dir = abs
		// Don't pollute router output; opencode serve logs go to /dev/null.
		cmd.Stdout = nil
		cmd.Stderr = nil

		if err := cmd.Start(); err != nil {
			l.logger.Error("failed to start opencode serve", "path", abs, "port", nextPort, "error", err)
			continue
		}

		mp := &managedProcess{cmd: cmd, path: abs, port: nextPort}
		l.mu.Lock()
		l.procs = append(l.procs, mp)
		l.mu.Unlock()

		l.logger.Info("started opencode serve", "path", abs, "port", nextPort, "pid", cmd.Process.Pid)

		// Reap the process when it exits to avoid zombies.
		go func(mp *managedProcess) {
			if err := mp.cmd.Wait(); err != nil {
				l.logger.Warn("opencode serve exited", "path", mp.path, "port", mp.port, "error", err)
			} else {
				l.logger.Info("opencode serve exited", "path", mp.path, "port", mp.port)
			}
		}(mp)

		nextPort++
	}
	return nil
}

// Shutdown sends SIGTERM to all managed opencode serve processes.
func (l *Launcher) Shutdown() {
	l.mu.Lock()
	procs := l.procs
	l.procs = nil
	l.mu.Unlock()

	if len(procs) == 0 {
		return
	}

	l.logger.Info("stopping managed opencode serve instances", "count", len(procs))
	for _, mp := range procs {
		if mp.cmd.Process == nil {
			continue
		}
		if err := mp.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			l.logger.Debug("signal failed (process may have already exited)",
				"pid", mp.cmd.Process.Pid, "error", err)
		} else {
			l.logger.Info("sent SIGTERM to opencode serve",
				"path", mp.path, "port", mp.port, "pid", mp.cmd.Process.Pid)
		}
	}
}

// portInUse checks if a TCP port is already in use on localhost.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
