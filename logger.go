package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

func setupLogger() (*slog.Logger, string, func()) {
	logFile, logPath := openLogFile()
	logWriter := io.Discard
	closeFn := func() {}

	if logFile != nil {
		logWriter = logFile
		closeFn = func() {
			_ = logFile.Close()
		}
	} else {
		logPath = "disabled (io.Discard)"
	}

	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	return logger, logPath, closeFn
}

func openLogFile() (*os.File, string) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		logDir := filepath.Join(home, ".local", "share", "opencoderouter")
		if err := os.MkdirAll(logDir, 0o755); err == nil {
			path := filepath.Join(logDir, "debug.log")
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				return f, path
			}
		}
	}

	const fallback = "/tmp/ocr-debug.log"
	if f, err := os.OpenFile(fallback, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		return f, fallback
	}

	return nil, ""
}
