package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type orphanProcess struct {
	Port    int
	PID     int
	Command string
}

func handleStartupOrphanOffer(scanStart, scanEnd int, cleanup bool, logger *slog.Logger) {
	orphans, err := detectLikelyOrphanOpenCodeServes(scanStart, scanEnd)
	if err != nil {
		if logger != nil {
			logger.Debug("orphan detection unavailable", "error", err)
		}
		return
	}
	if len(orphans) == 0 {
		return
	}

	if logger != nil {
		logger.Warn(
			"detected likely orphan opencode serve processes in configured scan range",
			"count", len(orphans),
			"scan_range", fmt.Sprintf("%d-%d", scanStart, scanEnd),
			"cleanup_hint", "rerun with --cleanup-orphans or OCR_CLEANUP_ORPHANS=1",
		)
		for _, orphan := range orphans {
			logger.Warn("orphan candidate", "port", orphan.Port, "pid", orphan.PID, "command", orphan.Command)
		}
	}

	if !cleanup {
		return
	}

	if logger != nil {
		logger.Warn("startup orphan cleanup enabled; sending SIGTERM", "count", len(orphans))
	}
	cleanupLikelyOrphans(orphans, logger)
}

func detectLikelyOrphanOpenCodeServes(scanStart, scanEnd int) ([]orphanProcess, error) {
	if scanEnd < scanStart {
		return nil, nil
	}

	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d-%d", scanStart, scanEnd), "-sTCP:LISTEN", "-Fpcn")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLikelyOrphansFromLsofOutput(string(output), scanStart, scanEnd), nil
}

func parseLikelyOrphansFromLsofOutput(raw string, scanStart, scanEnd int) []orphanProcess {
	if scanEnd < scanStart {
		return nil
	}

	lines := strings.Split(raw, "\n")
	var (
		currentPID int
		currentCmd string
	)

	seen := make(map[string]struct{})
	orphans := make([]orphanProcess, 0)

	for _, lineRaw := range lines {
		line := strings.TrimSpace(lineRaw)
		if line == "" {
			continue
		}

		tag := line[0]
		value := strings.TrimSpace(line[1:])

		switch tag {
		case 'p':
			pid, convErr := strconv.Atoi(value)
			if convErr != nil {
				currentPID = 0
				continue
			}
			currentPID = pid
		case 'c':
			currentCmd = value
		case 'n':
			if currentPID == 0 {
				continue
			}
			if !strings.Contains(strings.ToLower(currentCmd), "opencode") {
				continue
			}
			port, ok := extractListenPort(value)
			if !ok || port < scanStart || port > scanEnd {
				continue
			}
			key := fmt.Sprintf("%d:%d", currentPID, port)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			orphans = append(orphans, orphanProcess{Port: port, PID: currentPID, Command: currentCmd})
		}
	}

	return orphans
}

func cleanupLikelyOrphans(orphans []orphanProcess, logger *slog.Logger) {
	seen := make(map[int]struct{})
	for _, orphan := range orphans {
		if _, ok := seen[orphan.PID]; ok {
			continue
		}
		seen[orphan.PID] = struct{}{}

		err := syscall.Kill(orphan.PID, syscall.SIGTERM)
		if err == nil {
			if logger != nil {
				logger.Info("sent SIGTERM to likely orphan process", "pid", orphan.PID, "command", orphan.Command)
			}
			continue
		}
		if errors.Is(err, syscall.ESRCH) {
			if logger != nil {
				logger.Debug("orphan process already exited", "pid", orphan.PID)
			}
			continue
		}
		if logger != nil {
			logger.Warn("failed to terminate likely orphan process", "pid", orphan.PID, "error", err)
		}
	}
}

func extractListenPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, false
	}
	if idx := strings.Index(addr, "->"); idx > 0 {
		addr = strings.TrimSpace(addr[:idx])
	}
	if idx := strings.Index(addr, "("); idx > 0 {
		addr = strings.TrimSpace(addr[:idx])
	}
	idx := strings.LastIndex(addr, ":")
	if idx < 0 || idx+1 >= len(addr) {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(addr[idx+1:]))
	if err != nil {
		return 0, false
	}
	return port, true
}

func envEnabled(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
