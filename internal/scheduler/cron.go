// Package scheduler implements pogod's daemon-side cron and one-shot wakeup
// scheduler. See ARCHITECTURE.md (Scheduler section) and
// docs/sleep-resilience-design.md for the full rationale.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSchedule is a parsed standard 5-field cron expression evaluated in the
// local timezone (same convention as Claude's in-process CronCreate, so a
// migration from CronCreate to pogod schedule keeps the same local-time
// semantics).
//
// Syntax per field:
//
//   - every value
//     N           specific value
//     N-M         range, inclusive
//     */S         every S starting at 0 (or field min)
//     N-M/S       every S in range
//     N,M,O       comma-separated list (each element may itself be a range or step)
//
// Field order: minute hour day-of-month month day-of-week. Day-of-week 0 and 7
// both mean Sunday. When both day-of-month and day-of-week are restricted the
// match is OR (POSIX cron behavior — either matches).
type CronSchedule struct {
	expr    string
	minute  fieldMask
	hour    fieldMask
	dom     fieldMask
	month   fieldMask
	dow     fieldMask
	domStar bool
	dowStar bool
}

// String returns the original expression.
func (c CronSchedule) String() string { return c.expr }

// fieldMask is a bitmask covering [min,max] inclusive. Bit i is set when the
// integer (min+i) matches.
type fieldMask uint64

func (m fieldMask) test(i int) bool { return m&(1<<uint(i)) != 0 }
func (m *fieldMask) set(i int)      { *m |= 1 << uint(i) }

type fieldSpec struct {
	min, max int
	name     string
}

var (
	specMinute = fieldSpec{0, 59, "minute"}
	specHour   = fieldSpec{0, 23, "hour"}
	specDOM    = fieldSpec{1, 31, "day-of-month"}
	specMonth  = fieldSpec{1, 12, "month"}
	specDOW    = fieldSpec{0, 6, "day-of-week"}
)

// ParseCron parses a 5-field cron expression. Returns a descriptive error on
// invalid input — pogod logs and rejects bad cron strings rather than silently
// papering over them.
func ParseCron(expr string) (CronSchedule, error) {
	expr = strings.TrimSpace(expr)
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronSchedule{}, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}
	c := CronSchedule{expr: expr}
	var err error
	if c.minute, _, err = parseField(fields[0], specMinute); err != nil {
		return CronSchedule{}, err
	}
	if c.hour, _, err = parseField(fields[1], specHour); err != nil {
		return CronSchedule{}, err
	}
	if c.dom, c.domStar, err = parseField(fields[2], specDOM); err != nil {
		return CronSchedule{}, err
	}
	if c.month, _, err = parseField(fields[3], specMonth); err != nil {
		return CronSchedule{}, err
	}
	// day-of-week: accept 7 as Sunday alias.
	dowField := strings.ReplaceAll(fields[4], "7", "0")
	if c.dow, c.dowStar, err = parseField(dowField, specDOW); err != nil {
		return CronSchedule{}, err
	}
	return c, nil
}

func parseField(s string, spec fieldSpec) (fieldMask, bool, error) {
	if s == "" {
		return 0, false, fmt.Errorf("cron: empty %s field", spec.name)
	}
	star := s == "*" || strings.HasPrefix(s, "*/")
	var mask fieldMask
	for _, part := range strings.Split(s, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("cron: invalid step %q in %s", part[i+1:], spec.name)
			}
			step = n
			part = part[:i]
		}
		var lo, hi int
		switch {
		case part == "*":
			lo, hi = spec.min, spec.max
		case strings.Contains(part, "-"):
			rangeParts := strings.SplitN(part, "-", 2)
			a, errA := strconv.Atoi(rangeParts[0])
			b, errB := strconv.Atoi(rangeParts[1])
			if errA != nil || errB != nil {
				return 0, false, fmt.Errorf("cron: invalid range %q in %s", part, spec.name)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return 0, false, fmt.Errorf("cron: invalid value %q in %s", part, spec.name)
			}
			lo, hi = n, n
		}
		if lo < spec.min || hi > spec.max || lo > hi {
			return 0, false, fmt.Errorf("cron: %s value %d-%d out of range [%d,%d]", spec.name, lo, hi, spec.min, spec.max)
		}
		for v := lo; v <= hi; v += step {
			mask.set(v - spec.min)
		}
	}
	return mask, star, nil
}

// Next returns the smallest time strictly greater than after at which the
// expression matches, in after's location. The walk is bounded to 4 years
// (longer than the longest possible cron gap) so a malformed schedule never
// loops forever.
func (c CronSchedule) Next(after time.Time) time.Time {
	t := after.Add(time.Minute).Truncate(time.Minute)
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		if !c.month.test(int(t.Month()) - specMonth.min) {
			// Jump to the first of next month at 00:00.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.dayMatches(t) {
			// Jump to next day at 00:00.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
			continue
		}
		if !c.hour.test(t.Hour() - specHour.min) {
			t = t.Add(time.Hour).Truncate(time.Hour)
			continue
		}
		if !c.minute.test(t.Minute() - specMinute.min) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches implements POSIX cron's OR semantics: when both DOM and DOW are
// restricted (neither is `*`), a day matches if either field matches. When
// either is `*`, both must match — which reduces to: the restricted one must
// match.
func (c CronSchedule) dayMatches(t time.Time) bool {
	domHit := c.dom.test(t.Day() - specDOM.min)
	dowHit := c.dow.test(int(t.Weekday()) - specDOW.min)
	if c.domStar && c.dowStar {
		return true
	}
	if c.domStar {
		return dowHit
	}
	if c.dowStar {
		return domHit
	}
	return domHit || dowHit
}
