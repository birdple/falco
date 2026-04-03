// Package logger provides a centralized zerolog-based logger for the application.
// All packages should use this logger instead of creating their own.
package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// Logger is the application-wide logger instance.
var Logger zerolog.Logger

func init() {
	// Default: JSON to stdout at info level, with caller disabled.
	// Call Setup() from main to apply user configuration.
	Logger = zerolog.New(os.Stdout).
		With().
		Timestamp().
		Logger().
		Level(zerolog.InfoLevel)
}

// Config holds logger configuration options.
type Config struct {
	Level        string // trace, debug, info, warn, error, fatal, panic
	Format       string // json, text
	Output       string // stdout, stderr, or a file path
	EnableCaller bool
}

// Setup initialises the global Logger from the given Config.
// It should be called once from main before any other package logs.
func Setup(cfg Config) {
	level := parseLevel(cfg.Level)

	var writer io.Writer
	switch strings.ToLower(cfg.Output) {
	case "", "stdout":
		writer = os.Stdout
	case "stderr":
		writer = os.Stderr
	default:
		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			// Fall back to stdout if we can't open the file.
			writer = os.Stdout
		} else {
			writer = f
		}
	}

	if strings.ToLower(cfg.Format) == "text" {
		writer = zerolog.ConsoleWriter{
			Out:        writer,
			TimeFormat: time.RFC3339,
		}
	}

	ctx := zerolog.New(writer).With().Timestamp()
	if cfg.EnableCaller {
		ctx = ctx.Caller()
	}

	Logger = ctx.Logger().Level(level)
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info", "":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	case "panic":
		return zerolog.PanicLevel
	default:
		return zerolog.InfoLevel
	}
}

// Convenience functions that operate on the global Logger.

func Debug() *zerolog.Event { return Logger.Debug() }
func Info() *zerolog.Event  { return Logger.Info() }
func Warn() *zerolog.Event  { return Logger.Warn() }
func Error() *zerolog.Event { return Logger.Error() }
func Fatal() *zerolog.Event { return Logger.Fatal() }

// Err starts a new error-level event with the given error attached.
func Err(err error) *zerolog.Event { return Logger.Err(err) }

// With returns a child logger with the given fields pre-set.
func With() zerolog.Context { return Logger.With() }
