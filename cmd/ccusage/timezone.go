package main

import (
	"encoding/binary"
	"regexp"
	"strings"
	"time"

	"github.com/RedwindA/ccusage_go/internal/commands"
)

// tzLocation returns the timezone TZ sets as jiff, upstream's source of the
// system timezone, reads it on every platform: empty is UTC, and a POSIX
// rule or, after an optional ':', a zone name in any case is that zone. Go
// reads TZ only on Unix, and a zone name only in its own case.
func tzLocation(tz string) (*time.Location, bool) {
	if tz == "" {
		return time.UTC, true
	}
	if loc, ok := posixLocation(tz); ok {
		return loc, true
	}
	loc, err := commands.LoadTimezone(strings.TrimPrefix(tz, ":"))
	return loc, err == nil
}

// posixTZ matches the POSIX TZ rules that jiff, upstream's source of the
// system timezone, reads TZ as: a standard name and offset, and a daylight
// name only with the rules for when daylight time starts and ends. Go reads
// TZ only as a zone name and takes UTC for anything else.
var posixTZ = func() *regexp.Regexp {
	name := `([A-Za-z]{3,}|<[A-Za-z0-9+-]{3,}>)`
	offset := `[+-]?([01]?\d|2[0-4])(:[0-5]\d(:[0-5]\d)?)?`
	rule := `,(J\d{1,3}|\d{1,3}|M(1[0-2]|[1-9])\.[1-5]\.[0-6])(/[+-]?\d{1,3}(:[0-5]\d(:[0-5]\d)?)?)?`
	return regexp.MustCompile(`^` + name + offset + `(` + name + `(` + offset + `)?` + rule + rule + `)?$`)
}()

// posixLocation returns the timezone the POSIX TZ rule tz describes, such as
// CST-8 or EST5EDT,M3.2.0,M11.1.0.
func posixLocation(tz string) (*time.Location, bool) {
	if !posixTZ.MatchString(tz) {
		return nil, false
	}
	// Go applies the rule in a TZif file's footer after its last transition,
	// so a file without transitions applies it to all time. Its one zone,
	// UTC, stands in for a rule Go cannot apply, as jiff takes UTC then.
	var data []byte
	for range 2 { // version 1 and version 2 headers and data
		data = append(data, "TZif2"+strings.Repeat("\x00", 15)...)
		for _, count := range []uint32{0, 0, 0, 0, 1, 1} { // UT/local, standard/wall, leap seconds, transitions, zones, name bytes
			data = binary.BigEndian.AppendUint32(data, count)
		}
		data = append(data, make([]byte, 7)...) // the zone: offset 0, no DST, an empty name
	}
	loc, err := time.LoadLocationFromTZData(tz, append(data, "\n"+tz+"\n"...))
	return loc, err == nil
}
