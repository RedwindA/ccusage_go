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

// reportDate returns the day claude daily, run as the binary runs with the
// environment variable TZ set to tz, groups a usage line at the time at.
func reportDate(t *testing.T, tz, at string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects/p"), 0700); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"` + at + `","sessionId":"s","requestId":"r","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":20}}}`
	if err := os.WriteFile(filepath.Join(root, "projects/p/s.jsonl"), []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "claude", "daily", "--data-path", root, "--offline", "--json")
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "CCUSAGE_RUN_MAIN=1", "TZ="+tz, "CLAUDE_CONFIG_DIR="+root, "HOME="+home, "USERPROFILE="+home)
	cmd.Dir = home
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var report struct{ Daily []struct{ Date string } }
	if err := json.Unmarshal(out, &report); err != nil || len(report.Daily) != 1 {
		t.Fatalf("TZ=%s: %v %s", tz, err, out)
	}
	return report.Daily[0].Date
}

// Like upstream, whose jiff reads a POSIX TZ rule as the system timezone,
// TZ=CST-8 groups usage by days eight hours ahead of UTC.
func TestPOSIXTimezoneGroupsReports(t *testing.T) {
	if got := reportDate(t, "CST-8", "2026-07-15T20:00:00.000Z"); got != "2026-07-16" {
		t.Fatalf("TZ=CST-8: %s", got)
	}
}

// Like upstream, whose jiff reads TZ on every platform, a zone name in any
// case, after an optional ':', sets the system timezone, and an empty TZ is
// UTC. Go reads TZ only on Unix, and a name only in its own case.
func TestTimezoneNamesGroupReports(t *testing.T) {
	for _, tc := range []struct{ tz, at, want string }{
		{"asia/kolkata", "2026-07-15T20:00:00.000Z", "2026-07-16"},
		{":ASIA/KOLKATA", "2026-07-15T20:00:00.000Z", "2026-07-16"},
		{"America/New_York", "2026-07-15T02:00:00.000Z", "2026-07-14"},
		{"", "2026-07-15T20:00:00.000Z", "2026-07-15"},
	} {
		if got := reportDate(t, tc.tz, tc.at); got != tc.want {
			t.Errorf("TZ=%s: %s, want %s", tc.tz, got, tc.want)
		}
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
