package jsonlog_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/jsonlog"
)

func TestLogProducesExpectedJSONShape(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf)
	l.Info("hello", map[string]interface{}{"qps": 100, "target": "http://x"})

	var entry jsonlog.Entry
	if err := json.NewDecoder(&buf).Decode(&entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry.Level != jsonlog.LevelInfo {
		t.Errorf("level = %q, want info", entry.Level)
	}
	if entry.Msg != "hello" {
		t.Errorf("msg = %q, want hello", entry.Msg)
	}
	if entry.Time == "" {
		t.Error("time is empty")
	}
	if got, ok := entry.Attrs["target"].(string); !ok || got != "http://x" {
		t.Errorf("attrs.target = %v, want http://x", entry.Attrs["target"])
	}
}

func TestEmptyAttrsAreOmitted(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf)
	l.Info("bare", nil)
	line := buf.String()
	if strings.Contains(line, `"attrs"`) {
		t.Errorf("bare log line contains attrs: %q", line)
	}

	buf.Reset()
	l.Info("also-bare", map[string]interface{}{})
	line = buf.String()
	if strings.Contains(line, `"attrs"`) {
		t.Errorf("empty-map log line contains attrs: %q", line)
	}
}

func TestEachLevelEmitsCorrespondingString(t *testing.T) {
	cases := []struct {
		fn   func(*jsonlog.Logger, string)
		want jsonlog.Level
	}{
		{func(l *jsonlog.Logger, s string) { l.Debug(s, nil) }, jsonlog.LevelDebug},
		{func(l *jsonlog.Logger, s string) { l.Info(s, nil) }, jsonlog.LevelInfo},
		{func(l *jsonlog.Logger, s string) { l.Warn(s, nil) }, jsonlog.LevelWarn},
		{func(l *jsonlog.Logger, s string) { l.Error(s, nil) }, jsonlog.LevelError},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		l := jsonlog.New(&buf)
		tc.fn(l, "x")
		var entry jsonlog.Entry
		_ = json.Unmarshal(buf.Bytes(), &entry)
		if entry.Level != tc.want {
			t.Errorf("level = %q, want %q", entry.Level, tc.want)
		}
	}
}

// TestConcurrentLogsAreSerialized asserts the Mutex serialization
// produces well-formed lines under contention. Without the mutex,
// interleaved writes would produce un-parseable JSON. The test
// fails if any line in the output fails to decode.
func TestConcurrentLogsAreSerialized(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf)

	const goroutines = 8
	const perGoroutine = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				l.Info("contended", map[string]interface{}{"g": g, "i": i})
			}
		}(g)
	}
	wg.Wait()

	scanner := json.NewDecoder(&buf)
	count := 0
	for {
		var entry jsonlog.Entry
		err := scanner.Decode(&entry)
		if err != nil {
			break
		}
		count++
	}
	if count != goroutines*perGoroutine {
		t.Errorf("decoded %d entries; want %d (some lines must have torn under contention)",
			count, goroutines*perGoroutine)
	}
}

func TestTimeFormatIsRFC3339Nano(t *testing.T) {
	var buf bytes.Buffer
	l := jsonlog.New(&buf)
	l.Info("ts", nil)
	var entry jsonlog.Entry
	_ = json.Unmarshal(buf.Bytes(), &entry)
	if _, err := time.Parse(time.RFC3339Nano, entry.Time); err != nil {
		t.Errorf("time %q does not parse as RFC3339Nano: %v", entry.Time, err)
	}
}
