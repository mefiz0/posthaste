package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logLines returns every line of path, asserting each is a valid JSON object.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		var record map[string]json.RawMessage
		if !json.Valid([]byte(line)) || json.Unmarshal([]byte(line), &record) != nil {
			t.Fatalf("line %d of %s is not a valid JSON record: %q", i+1, path, line)
		}
	}
	return lines
}

func TestNewWritesJSONToLogFile(t *testing.T) {
	dir := t.TempDir()
	logger, closeLog, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("sync finished", "account_id", "acct-42", "attempt", 3)
	if err := closeLog(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := logLines(t, filepath.Join(dir, logFileName))
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1", len(lines))
	}
	for _, want := range []string{"sync finished", "acct-42", `"attempt":3`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("log line missing %q:\n%s", want, lines[0])
		}
	}
}

func TestNewPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs", "nested")
	logger, closeLog, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("perm check")
	if err := closeLog(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir perms = %o, want 700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("file perms = %o, want 600", got)
	}
}

func TestVerboseSwitchesLevel(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
		want    bool
	}{
		{name: "default is info", verbose: false, want: false},
		{name: "verbose enables debug", verbose: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logger, closeLog, err := New(Options{Dir: dir, Verbose: tt.verbose})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			logger.Debug("debug detail", "component", "sync")
			if err := closeLog(); err != nil {
				t.Fatalf("close: %v", err)
			}

			lines := logLines(t, filepath.Join(dir, logFileName))
			if tt.want && len(lines) != 1 {
				t.Errorf("verbose logger wrote %d lines, want 1", len(lines))
			}
			if !tt.want && len(lines) != 0 {
				t.Errorf("non-verbose logger wrote debug lines: %v", lines)
			}
		})
	}
}

func TestRotationAtOpen(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, logFileName)
	rotatedPath := filepath.Join(dir, rotatedFileName)

	// Inject an oversized existing log so New has something to rotate away.
	old := bytes.Repeat([]byte("x"), 2048)
	if err := os.WriteFile(logPath, old, 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	logger, closeLog, err := New(Options{Dir: dir, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("fresh start")
	if err := closeLog(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rotated, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	if !bytes.Equal(rotated, old) {
		t.Errorf("rotated file content changed")
	}
	lines := logLines(t, logPath)
	if len(lines) != 1 || !strings.Contains(lines[0], "fresh start") {
		t.Errorf("new log = %v, want one fresh-start record", lines)
	}
}

func TestRotationReplacesOldGeneration(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, logFileName)
	rotatedPath := filepath.Join(dir, rotatedFileName)

	if err := os.WriteFile(logPath, bytes.Repeat([]byte("first"), 300), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	if _, _, err := New(Options{Dir: dir, MaxBytes: 1024}); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("second"), 300), 0o600); err != nil {
		t.Fatalf("reseed log: %v", err)
	}
	if _, _, err := New(Options{Dir: dir, MaxBytes: 1024}); err != nil {
		t.Fatalf("second New: %v", err)
	}

	rotated, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
	if !bytes.HasPrefix(rotated, []byte("second")) {
		t.Errorf("rotated file holds stale generation, want the latest oversized log")
	}
}

func TestRotationBeforeWriteKeepsRecordsIntact(t *testing.T) {
	dir := t.TempDir()
	logger, closeLog, err := New(Options{Dir: dir, MaxBytes: 256})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const records = 12
	for i := 0; i < records; i++ {
		logger.Info("rotation probe", "record", i)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("close: %v", err)
	}

	current := logLines(t, filepath.Join(dir, logFileName))
	rotated := logLines(t, filepath.Join(dir, rotatedFileName))
	if len(current) == 0 || len(rotated) == 0 {
		t.Fatalf("expected records in both generations, got current=%d rotated=%d",
			len(current), len(rotated))
	}
	if len(current)+len(rotated) > records {
		t.Errorf("%d records survived across two files, want at most %d",
			len(current)+len(rotated), records)
	}

	// Every record is whole: parse each line and read its record number.
	var seen []float64
	for _, line := range append(append([]string{}, rotated...), current...) {
		var record struct {
			Msg    string          `json:"msg"`
			Record json.RawMessage `json:"record"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		var number float64
		if err := json.Unmarshal(record.Record, &number); err != nil {
			t.Fatalf("decode record number from %q: %v", line, err)
		}
		seen = append(seen, number)
	}
	if len(seen) == 0 {
		t.Fatal("no records decoded")
	}
	// The latest generation must hold the newest record; older ones were
	// dropped by the one-generation rotation.
	last := seen[len(seen)-1]
	if last != records-1 {
		t.Errorf("newest record = %v, want %d", last, records-1)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	_, closeLog, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestNewRequiresDir(t *testing.T) {
	if _, _, err := New(Options{Dir: ""}); err == nil {
		t.Error("New with empty Dir succeeded, want error")
	}
}

func TestDefaultMaxBytesApplied(t *testing.T) {
	dir := t.TempDir()
	_, closeLog, err := New(Options{Dir: dir, MaxBytes: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if closeLog == nil {
		t.Fatal("New returned nil CloseFunc")
	}
	if err := closeLog(); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, logFileName)); err != nil {
		t.Errorf("log file missing: %v", err)
	}
}
