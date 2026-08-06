package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Logger struct {
	slog    *slog.Logger
	file    io.Closer
	level   slog.Level
	logChan chan LogEntry
	done    chan struct{}
}

type LogEntry struct {
	Level   string
	Message string
	Time    time.Time
}

func New(dir string, level string, fileEnabled bool) (*Logger, error) {
	var writers []io.Writer
	writers = append(writers, os.Stdout)

	var file io.Closer
	if fileEnabled {
		logPath := filepath.Join(dir, "logs", "bot.log")
		// Derive directory and basename from the configured file path.
		// e.g. "logs/bot.log" → dir="logs", basename="bot"
		logDir := filepath.Dir(logPath)
		base := strings.TrimSuffix(filepath.Base(logPath), ".log")

		var err error
		var rw *DailyRotatingWriter
		rw, err = NewDailyRotatingWriter(logDir, base)
		if err != nil {
			return nil, fmt.Errorf("failed to create rotating log writer: %w", err)
		}
		file = rw
		writers = append(writers, rw)
	}

	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	multiWriter := io.MultiWriter(writers...)
	handler := slog.NewJSONHandler(multiWriter, &slog.HandlerOptions{
		Level: logLevel,
	})

	l := &Logger{
		slog:    slog.New(handler),
		file:    file,
		level:   logLevel,
		logChan: make(chan LogEntry, 100),
		done:    make(chan struct{}),
	}

	go func() {
		l.processLogs()
		close(l.done)
	}()

	return l, nil
}

func (l *Logger) processLogs() {
	for entry := range l.logChan {
		// slog.JSONHandler is already goroutine-safe — no mutex needed
		switch entry.Level {
		case "debug":
			l.slog.Debug(entry.Message)
		case "info":
			l.slog.Info(entry.Message)
		case "warn":
			l.slog.Warn(entry.Message)
		case "error":
			l.slog.Error(entry.Message)
		}
	}
}

// send attempts a non-blocking send to the log channel. If the channel is
// full, the entry is dropped to prevent callers from blocking indefinitely.
// A dropped entry is reported to stderr so overflow is observable.
func (l *Logger) send(entry LogEntry) {
	select {
	case l.logChan <- entry:
	default:
		// Channel full — drop to avoid blocking the caller. Report to stderr
		// directly (not via the logger, which would re-enter this path).
		fmt.Fprintf(os.Stderr, "logger: channel full, dropping %s log: %s\n", entry.Level, entry.Message)
	}
}

func (l *Logger) Debug(msg string, args ...any) {
	l.send(LogEntry{Level: "debug", Message: fmt.Sprintf(msg, args...), Time: time.Now()})
}

func (l *Logger) Info(msg string, args ...any) {
	l.send(LogEntry{Level: "info", Message: fmt.Sprintf(msg, args...), Time: time.Now()})
}

func (l *Logger) Warn(msg string, args ...any) {
	l.send(LogEntry{Level: "warn", Message: fmt.Sprintf(msg, args...), Time: time.Now()})
}

func (l *Logger) Error(msg string, args ...any) {
	l.send(LogEntry{Level: "error", Message: fmt.Sprintf(msg, args...), Time: time.Now()})
}

func (l *Logger) Close() {
	close(l.logChan)
	<-l.done
	if l.file != nil {
		l.file.Close()
	}
}
