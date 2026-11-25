// Package logs parses a scenario directory into a flat, ordered list of events.
//
// Each source has its own line format. Lines that do not start a new record
// (Python traceback continuations) are folded into the previous event, so an
// event id always points at the line where the record starts.
package logs

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Level is a coarse severity. Kernel and metrics lines carry no level of their
// own, so it is inferred from generic wording, never from fault-specific text.
type Level int

const (
	Info Level = iota
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Event is one log record.
type Event struct {
	ID     string    // "<rel file>:<line>"
	File   string    // relative path, forward slashes
	Line   int       // 1-based line where the record starts
	Node   string    // "" for cluster-level files
	Source string    // kern, train, metrics, scheduler, hw
	TS     time.Time // zero when the line has no timestamp (hw.txt)
	Level  Level
	Text   string // message without the timestamp header; continuation lines appended with "\n"
}

// Set is a parsed scenario.
type Set struct {
	Dir      string
	Events   []Event // ordered by file, then line
	byID     map[string]int
	Unparsed int // lines that matched no known header and had no record to attach to
}

// Get returns the event that starts at id.
func (s *Set) Get(id string) (Event, bool) {
	i, ok := s.byID[id]
	if !ok {
		return Event{}, false
	}
	return s.Events[i], true
}

// Index returns the position of id in Events, or -1.
func (s *Set) Index(id string) int {
	i, ok := s.byID[id]
	if !ok {
		return -1
	}
	return i
}

// Load parses every known log file under dir. truth.json is never read.
func Load(dir string) (*Set, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if sourceOf(d.Name()) != "" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	s := &Set{Dir: dir, byID: map[string]int{}}
	for _, p := range files {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil, err
		}
		if err := s.parseFile(p, filepath.ToSlash(rel)); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
	}
	return s, nil
}

func sourceOf(name string) string {
	switch name {
	case "kern.log":
		return "kern"
	case "train.log":
		return "train"
	case "metrics.log":
		return "metrics"
	case "scheduler.log":
		return "scheduler"
	case "hw.txt":
		return "hw"
	}
	return ""
}

func (s *Set) parseFile(path, rel string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	src := sourceOf(filepath.Base(rel))
	node := ""
	if dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." {
		node = dir
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	last := -1 // index of the last event started in this file
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ts, text, lvl, ok := parseLine(src, raw)
		if !ok {
			if last >= 0 && src == "train" {
				s.Events[last].Text += "\n" + raw
				if lvl := inferLevel(raw); lvl > s.Events[last].Level {
					s.Events[last].Level = lvl
				}
				continue
			}
			s.Unparsed++
			continue
		}
		ev := Event{
			ID:     fmt.Sprintf("%s:%d", rel, lineNo),
			File:   rel,
			Line:   lineNo,
			Node:   node,
			Source: src,
			TS:     ts,
			Level:  lvl,
			Text:   text,
		}
		s.byID[ev.ID] = len(s.Events)
		s.Events = append(s.Events, ev)
		last = len(s.Events) - 1
	}
	return sc.Err()
}

var (
	// 2026-03-14T10:02:11.123456+00:00 node02 kern.err kernel: [ 1234.567890] message
	// (rsyslog template with facility.priority)
	kernRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d+[+-]\d\d:\d\d) \S+ kern\.(\w+) kernel: \[\s*[\d.]+\] (.*)$`)
	// 2026-03-14 10:02:11,123 [rank 12] INFO message
	trainRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d,\d{3}) (\[rank \d+\]) (INFO|WARNING|ERROR|CRITICAL) (.*)$`)
	// ts=2026-03-14T10:02:11Z node=node02 gpu=3 ...
	metricsRe = regexp.MustCompile(`^ts=(\S+) (.*)$`)
	// [2026-03-14T10:02:11.123] message
	schedRe = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3})\] (.*)$`)
)

func parseLine(src, raw string) (time.Time, string, Level, bool) {
	switch src {
	case "kern":
		m := kernRe.FindStringSubmatch(raw)
		if m == nil {
			return time.Time{}, "", Info, false
		}
		ts, err := time.Parse("2006-01-02T15:04:05.999999-07:00", m[1])
		if err != nil {
			return time.Time{}, "", Info, false
		}
		lvl := inferLevel(m[3])
		switch m[2] {
		case "emerg", "alert", "crit", "err":
			lvl = Error
		case "warning":
			if lvl < Warn {
				lvl = Warn
			}
		}
		return ts.UTC(), m[3], lvl, true
	case "train":
		m := trainRe.FindStringSubmatch(raw)
		if m == nil {
			return time.Time{}, "", Info, false
		}
		ts, err := time.Parse("2006-01-02 15:04:05,000", m[1])
		if err != nil {
			return time.Time{}, "", Info, false
		}
		lvl := Info
		switch m[3] {
		case "WARNING":
			lvl = Warn
		case "ERROR", "CRITICAL":
			lvl = Error
		}
		return ts.UTC(), m[2] + " " + m[4], lvl, true
	case "metrics":
		m := metricsRe.FindStringSubmatch(raw)
		if m == nil {
			return time.Time{}, "", Info, false
		}
		ts, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			return time.Time{}, "", Info, false
		}
		return ts.UTC(), m[2], inferLevel(m[2]), true
	case "scheduler":
		m := schedRe.FindStringSubmatch(raw)
		if m == nil {
			return time.Time{}, "", Info, false
		}
		ts, err := time.Parse("2006-01-02T15:04:05.000", m[1])
		if err != nil {
			return time.Time{}, "", Info, false
		}
		return ts.UTC(), m[2], inferLevel(m[2]), true
	case "hw":
		return time.Time{}, raw, Info, true
	}
	return time.Time{}, "", Info, false
}

var (
	errorWords = regexp.MustCompile(`(?i)\b(error|errors|fatal|failed|failure|killed|panic|exception|unable|timed out|timeout|exceeded|abort(ed)?)\b`)
	// throttl/slowdown match inside identifiers such as hw_slowdown, where \b
	// does not split on the underscore.
	warnWords = regexp.MustCompile(`(?i)\b(warn(ing)?|down|lost|retry|retrying|degraded|drain)\b|throttl|slowdown`)
)

// inferLevel uses generic operational wording only. It must stay free of
// fault-specific signatures so the anomaly digest does not leak the answer.
func inferLevel(s string) Level {
	if errorWords.MatchString(s) {
		return Error
	}
	if warnWords.MatchString(s) {
		return Warn
	}
	return Info
}

var (
	maskHex = regexp.MustCompile(`0x[0-9a-fA-F]+|\b[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}(\.\d)?\b`)
	maskNum = regexp.MustCompile(`\d+(\.\d+)?`)
)

// Template masks numbers, hex values and PCI addresses in a record's first
// line, so records that differ only in values cluster together.
func Template(e Event) string {
	first := strings.SplitN(e.Text, "\n", 2)[0]
	s := maskHex.ReplaceAllString(first, "<hex>")
	s = maskNum.ReplaceAllString(s, "<n>")
	return e.Source + ": " + s
}

// HasNode reports whether any record comes from node.
func (s *Set) HasNode(node string) bool {
	if node == "" {
		return false
	}
	for _, e := range s.Events {
		if e.Node == node {
			return true
		}
	}
	return false
}
