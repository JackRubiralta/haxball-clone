package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "": slog.LevelInfo, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
		"INFO": slog.LevelInfo, // case-insensitive
	}
	for s, want := range cases {
		got, err := ParseLevel(s)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v,%v want %v,nil", s, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) should error")
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"", "text", "json", "JSON"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q) errored: %v", s, err)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("ParseFormat(yaml) should error")
	}
}

func TestNewLogsJSON(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New(&buf, "info", "json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("hello", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("json handler output = %q", buf.String())
	}
	if _, err := New(&buf, "bogus", "text"); err == nil {
		t.Error("New with bad level should error")
	}
}
