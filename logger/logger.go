package logger

import (
	"log/slog"
	"os"
	"strings"

	sysd "github.com/iguanesolutions/go-systemd/v6"
	sysdjdslog "github.com/iguanesolutions/go-systemd/v6/journald/slog"
	"github.com/mattn/go-isatty"
)

const (
	DefaultLevel = slog.LevelInfo
)

func GetLogLevels() []string {
	return []string{
		slog.LevelDebug.String(),
		slog.LevelInfo.String(),
		slog.LevelWarn.String(),
		slog.LevelError.String(),
	}
}

func CreateLogger(requestedLogLevel string) (logger *slog.Logger) {
	// Handle slog options
	var opts slog.HandlerOptions
	switch strings.ToUpper(requestedLogLevel) {
	case slog.LevelDebug.String():
		opts.Level = slog.LevelDebug
		opts.AddSource = true
	case slog.LevelInfo.String():
		opts.Level = slog.LevelInfo
	case slog.LevelWarn.String():
		opts.Level = slog.LevelWarn
	case slog.LevelError.String():
		opts.Level = slog.LevelError
	default:
		opts.Level = DefaultLevel
		defer func() {
			logger.Warn("log level not set or invalid: setting default level",
				slog.String("requested_level", requestedLogLevel),
				slog.String("default_level", DefaultLevel.String()),
			)
		}()
	}
	// Create the logger
	sysdInvocationID, sysdStarted := sysd.GetInvocationID()
	switch {
	case sysdStarted:
		logger = slog.New(sysdjdslog.NewHandler(opts))
		logger.Debug("systemd launch detected, using journald logger format",
			slog.String("invocation_id", sysdInvocationID),
		)
	case isatty.IsTerminal(os.Stderr.Fd()):
		logger = slog.New(slog.NewTextHandler(os.Stderr, &opts))
	default:
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &opts))
	}
	return
}
