package crashreport

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCaptureScrubsSensitiveValues(t *testing.T) {
	h := Install(Options{Version: "1.2.3", Logger: discardLogger()})

	report := h.Capture(errors.New(
		"delivery failed for jane.doe@example.com using Bearer abcdefghijklmnop"))

	if strings.Contains(report.Panic, "jane.doe@example.com") {
		t.Fatalf("panic value leaked an address: %q", report.Panic)
	}
	if !strings.Contains(report.Panic, "[addr]") {
		t.Fatalf("address was not redacted: %q", report.Panic)
	}
	if strings.Contains(report.Panic, "abcdefghijklmnop") {
		t.Fatalf("token was not redacted: %q", report.Panic)
	}
	if report.Version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", report.Version)
	}
	if report.OS == "" || report.Arch == "" {
		t.Fatalf("runtime facts missing: os=%q arch=%q", report.OS, report.Arch)
	}
	if report.Stack == "" {
		t.Fatal("stack is empty")
	}
}

func TestHandleWritesReportWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	exits := 0
	h := Install(Options{
		Dir:     dir,
		Version: "9.9.9",
		Enabled: func() bool { return true },
		Exit:    func(int) { exits++ },
		Logger:  discardLogger(),
	})

	func() {
		defer h.Handle()
		panic("boom for leak@example.com")
	}()

	if exits != 1 {
		t.Fatalf("exit called %d times, want 1", exits)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read reports: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d reports, want 1", len(entries))
	}
	data, err := os.ReadFile(dir + "/" + entries[0].Name())
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if strings.Contains(string(data), "leak@example.com") {
		t.Fatalf("report leaked an address: %s", data)
	}
}

func TestHandleSkipsReportWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	exits := 0
	h := Install(Options{
		Dir:     dir,
		Enabled: func() bool { return false },
		Exit:    func(int) { exits++ },
		Logger:  discardLogger(),
	})

	func() {
		defer h.Handle()
		panic("boom")
	}()

	if exits != 1 {
		t.Fatalf("exit called %d times, want 1", exits)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read reports: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %d reports while disabled, want 0", len(entries))
	}
}

func TestHandleNoPanicIsNoop(t *testing.T) {
	exits := 0
	h := Install(Options{Exit: func(int) { exits++ }, Logger: discardLogger()})
	h.Handle()
	if exits != 0 {
		t.Fatalf("exit called %d times without a panic", exits)
	}
}
