// Package jsonlog writes one JSON object per log line, suitable for
// ingestion by a structured log aggregator (Loki, Elasticsearch,
// CloudWatch, etc.). The shape is intentionally minimal: a timestamp
// in RFC3339 nanoseconds, a level string, a message string, and an
// optional `attrs` flat map for per-line fields. Aggregators slice
// on `attrs.<key>` cleanly.
//
// The package avoids pulling in zap / zerolog / slog as a dependency
// at the v0.0.x baseline: traffic-gen targets Go 1.18 (slog landed
// in 1.21), and the volume of logs emitted by the poster is bounded
// by the QPS budget rather than per-rule. A homegrown encoder keeps
// the dependency surface small while emitting the same wire shape a
// production aggregator expects.
//
// Concurrency: a Logger is safe for many goroutines. Writes are
// serialized via an internal sync.Mutex held only for the encode +
// write window.
package jsonlog

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Level is one of the four standard log levels. The string form is
// what aggregators slice on, so the wire values are lowercased and
// stable across versions.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Logger writes one JSON object per Log call. Construct via New;
// callers may share a Logger across goroutines.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
	now func() time.Time // overrideable for tests
}

// New returns a Logger that writes to out. The writer is held by
// reference; closing it before the logger is done is the caller's
// responsibility. now defaults to time.Now and can be overridden
// for tests.
func New(out io.Writer) *Logger {
	return &Logger{out: out, now: time.Now}
}

// Entry is the JSON shape written per Log call. Time uses
// RFC3339Nano so sub-millisecond ordering survives ingestion;
// attrs is omitted when empty so a one-key log line is two keys
// (time + level + msg) rather than four.
type Entry struct {
	Time  string                 `json:"time"`
	Level Level                  `json:"level"`
	Msg   string                 `json:"msg"`
	Attrs map[string]interface{} `json:"attrs,omitempty"`
}

// Log writes one entry. attrs may be nil for a bare message. When
// attrs is non-nil and non-empty the keys appear under a single
// "attrs" object so the top-level schema stays {time, level, msg,
// attrs} regardless of caller-side field choices.
func (l *Logger) Log(level Level, msg string, attrs map[string]interface{}) {
	entry := Entry{
		Time:  l.now().UTC().Format(time.RFC3339Nano),
		Level: level,
		Msg:   msg,
	}
	if len(attrs) > 0 {
		entry.Attrs = attrs
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = json.NewEncoder(l.out).Encode(entry)
}

// Info / Warn / Error / Debug are thin convenience wrappers so
// callers don't repeat the level constant at every site.
func (l *Logger) Info(msg string, attrs map[string]interface{}) {
	l.Log(LevelInfo, msg, attrs)
}
func (l *Logger) Warn(msg string, attrs map[string]interface{}) {
	l.Log(LevelWarn, msg, attrs)
}
func (l *Logger) Error(msg string, attrs map[string]interface{}) {
	l.Log(LevelError, msg, attrs)
}
func (l *Logger) Debug(msg string, attrs map[string]interface{}) {
	l.Log(LevelDebug, msg, attrs)
}
