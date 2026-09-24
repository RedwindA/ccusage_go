package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableOutputWidthAndExports(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	path := fixtureReport(t)
	args := []string{"claude", "daily", "--data-path", path, "--offline", "--timezone", "UTC", "--no-color"}
	stdout, stderr, err := sourceCLIRun(args...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "╭") || !strings.Contains(stdout, "┌") || !strings.Contains(stdout, "$9.00") || !strings.Contains(stderr, "Running in Compact Mode") {
		t.Fatalf("missing table layout: %s %s", stdout, stderr)
	}
	if strings.Contains(stdout, "Cache") || strings.Contains(stdout, "\x1b") {
		t.Fatalf("unexpected compact columns/color: %s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line exceeds COLUMNS: %s", line)
		}
	}
	stdout, stderr, err = sourceCLIRun(append(args, "--responsive=false")...)
	if err != nil || !strings.Contains(stdout, "Cache Create") || strings.Contains(stderr, "Compact Mode") {
		t.Fatalf("responsive=false: %v %s %s", err, stdout, stderr)
	}
	stdout, stderr, err = sourceCLIRun(append(args, "--json")...)
	if err != nil || !json.Valid([]byte(stdout)) || strings.Contains(stderr, "Compact Mode") {
		t.Fatalf("JSON polluted by table output: %v %s %s", err, stdout, stderr)
	}
	stdout, stderr, err = sourceCLIRun(append(args, "--format", "csv")...)
	if err != nil || !strings.HasPrefix(stdout, "daily,agent,") || strings.Contains(stdout, "╭") || strings.Contains(stderr, "Compact Mode") {
		t.Fatalf("CSV polluted by table output: %v %s %s", err, stdout, stderr)
	}
}

func TestTableColorFlagsAndSessionDetail(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	// NO_COLOR takes precedence even when it is present with an empty value.
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	path := fixtureReport(t)
	args := []string{"claude", "session", "--id", "one", "--data-path", path, "--offline", "--color"}
	stdout, _, err := sourceCLIRun(args...)
	if err != nil || strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, "Source file breakdown") {
		t.Fatalf("session detail does not respect output settings: %v %s", err, stdout)
	}
}
