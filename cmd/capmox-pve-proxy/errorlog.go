package main

import (
	"context"
	"log"
	"log/slog"
	"strings"
)

// newErrorLog returns a log for http.Server.ErrorLog that writes each line net/http
// logs as a record of l, so the proxy's log stays JSON.
func newErrorLog(l *slog.Logger) *log.Logger {
	return log.New(errorLogWriter{l}, "", 0)
}

type errorLogWriter struct{ log *slog.Logger }

// Write logs one line: log.Logger writes each line with a single Write.
func (w errorLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimSuffix(string(p), "\n")
	level, remote := classify(line)
	args := []any{"error", line}
	if remote != "" {
		args = append(args, "remote", remote)
	}
	w.log.Log(context.Background(), level, "http server", args...)
	return len(p), nil
}

// classify returns the level of a line net/http logs, and the client address in it if
// it has one. A client that closes without a handshake, such as a probe or a port
// scanner, is a debug line. Another failed handshake is a warning: the client and the
// proxy disagree, as when CAPMOX does not trust the proxy's CA. Anything else is an
// error.
func classify(line string) (level slog.Level, remote string) {
	if rest, ok := strings.CutPrefix(line, "http: TLS handshake error from "); ok {
		remote, reason, _ := strings.Cut(rest, ": ")
		if reason == "EOF" || strings.HasSuffix(reason, "connection reset by peer") {
			return slog.LevelDebug, remote
		}
		return slog.LevelWarn, remote
	}
	if rest, ok := strings.CutPrefix(line, "http: panic serving "); ok {
		remote, _, _ := strings.Cut(rest, ": ")
		return slog.LevelError, remote
	}
	return slog.LevelError, ""
}
