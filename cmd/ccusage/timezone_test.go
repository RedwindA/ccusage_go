package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain runs main instead of the tests when CCUSAGE_RUN_MAIN is set, so a
// test can run the command as the binary does, with its own environment.
func TestMain(m *testing.M) {
	if os.Getenv("CCUSAGE_RUN_MAIN") != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Like upstream, whose jiff reads a POSIX TZ rule as the system timezone,
// TZ=CST-8 groups usage by days eight hours ahead of UTC.
func TestPOSIXTimezoneGroupsReports(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects/p"), 0700); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-07-15T20:00:00.000Z","sessionId":"s","requestId":"r","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":20}}}`
	if err := os.WriteFile(filepath.Join(root, "projects/p/s.jsonl"), []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "claude", "daily", "--data-path", root, "--offline", "--json")
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "CCUSAGE_RUN_MAIN=1", "TZ=CST-8", "CLAUDE_CONFIG_DIR="+root, "HOME="+home, "USERPROFILE="+home)
	cmd.Dir = home
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct{ Daily []struct{ Date string } }
	if err := json.Unmarshal(out, &report); err != nil || len(report.Daily) != 1 || report.Daily[0].Date != "2026-07-16" {
		t.Fatalf("TZ=CST-8: %v %s", err, out)
	}
}

// A POSIX TZ rule needs a name of at least three letters and an offset of at
// most 24 hours, and a daylight name needs the rules for when daylight time
// starts and ends, as jiff reads TZ.
func TestPOSIXLocations(t *testing.T) {
	at := time.Date(2026, 7, 15, 22, 30, 0, 0, time.UTC)
	for tz, want := range map[string]string{
		"JST-9":                      "2026-07-16T07:30:00+09:00",
		"UTC+3":                      "2026-07-15T19:30:00-03:00",
		"<+0530>-5:30":               "2026-07-16T04:00:00+05:30",
		"XYZ-24:59":                  "2026-07-16T23:29:00+24:59",
		"EST5EDT,M3.2.0,M11.1.0":     "2026-07-15T18:30:00-04:00",
		"CET-1CEST,M3.5.0,M10.5.0/3": "2026-07-16T00:30:00+02:00",
		"ABC-1DST,J60,J300":          "2026-07-16T00:30:00+02:00",
	} {
		loc, ok := posixLocation(tz)
		if !ok {
			t.Errorf("%s: not a POSIX rule", tz)
		} else if got := at.In(loc).Format(time.RFC3339); got != want {
			t.Errorf("%s: %s, want %s", tz, got, want)
		}
	}
	for _, tz := range []string{"", "UTC", "Asia/Tokyo", ":CST-8", "CET-1CEST", "ABC-1DST,M3.5.0", "XYZ-25", "AB-1", "<+1>-1"} {
		if _, ok := posixLocation(tz); ok {
			t.Errorf("%s: read as a POSIX rule", tz)
		}
	}
}
