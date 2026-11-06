package plans

import (
	"fmt"
	"path"
	"regexp"
	"time"
)

// digestName is a demo digest's file name in its year's directory under
// digests/ in the notes repository: MM-DD-demo.org, for the day it is
// named for.
var digestName = regexp.MustCompile(`^([0-9]{2})-([0-9]{2})-demo\.org$`)

// DigestFile is the demo digest of day, as a slash path relative to the
// notes repository: digests/YYYY/MM-DD-demo.org.
func DigestFile(day time.Time) string {
	return path.Join("digests", fmt.Sprint(day.Year()), day.Format("01-02")+"-demo.org")
}

// DigestDay is the day, in loc, that the file named name in the directory
// digests/YEAR is the demo digest of; ok is false for any name DigestFile
// does not give, a day that does not exist among them.
func DigestDay(year int, name string, loc *time.Location) (day time.Time, ok bool) {
	m := digestName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%04d-%s-%s", year, m[1], m[2]), loc)
	return day, err == nil
}
