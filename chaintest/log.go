package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/term"
)

type logHandler struct {
	stderrColor   bool
	stdoutColor   bool
	stdoutSuccess bool
}

const levelSuccess = slog.LevelInfo + 1

func (h *logHandler) fmt(lvl slog.Level, timestamp time.Time, msg string) string {
	tag := "?"
	color := "\033[1;36m"
	useColor := h.stderrColor

	switch lvl {
	case slog.LevelInfo:
		tag = "*"
		color = "\033[1;34m"
	case levelSuccess:
		tag = "+"
		color = "\033[1;32m"
		if h.stdoutSuccess {
			useColor = h.stdoutColor
		}
	case slog.LevelWarn:
		tag = "!"
		color = "\033[1;33m"
	case slog.LevelError:
		tag = "-"
		color = "\033[1;31m"
	}

	ts := timestamp.Format("02 Jan 2006 15:04:05")
	if useColor {
		return fmt.Sprintf("%s[%s] [%s] %s\033[0m\n", color, ts, tag, msg)
	}
	return fmt.Sprintf("[%s] [%s] %s\n", ts, tag, msg)
}

func (h *logHandler) Format(r *logrus.Entry) ([]byte, error) {
	lvl := slog.LevelDebug
	switch r.Level {
	case logrus.InfoLevel:
		lvl = slog.LevelInfo
	case logrus.WarnLevel:
		lvl = slog.LevelWarn
	case logrus.ErrorLevel:
		lvl = slog.LevelError
	}
	return []byte(h.fmt(lvl, r.Time, r.Message)), nil
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	stream := os.Stderr
	if r.Level == levelSuccess && h.stdoutSuccess {
		stream = os.Stdout
	}
	fmt.Fprint(stream, h.fmt(r.Level, r.Time, r.Message))
	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return h
}

func (h *logHandler) logSuccess2Stderr() {
	h.stdoutSuccess = false
}

func newLogHandler() logHandler {
	return logHandler{
		term.IsTerminal(int(os.Stderr.Fd())),
		term.IsTerminal(int(os.Stdout.Fd())),
		true,
	}
}

type customLogger struct {
	inner *slog.Logger
}

func newLogger(handler *logHandler) customLogger {
	return customLogger{slog.New(handler)}
}

func (l *customLogger) infof(format string, v ...any) {
	l.inner.Info(fmt.Sprintf(format, v...))
}

func (l *customLogger) successf(format string, v ...any) {
	l.inner.Log(context.Background(), levelSuccess, fmt.Sprintf(format, v...))
}

func (l *customLogger) errorf(format string, v ...any) {
	l.inner.Error(fmt.Sprintf(format, v...))
}

func (l *customLogger) debugf(format string, v ...any) {
	l.inner.Debug(fmt.Sprintf(format, v...))
}

func (l *customLogger) warnf(format string, v ...any) {
	l.inner.Warn(fmt.Sprintf(format, v...))
}
