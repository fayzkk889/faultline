package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fayzkk889/faultline/internal/logs"
)

// Tools exposes read-only views of one scenario. Every view is generic: none
// of them knows the fault catalog, so their output cannot leak the answer.
type Tools struct {
	set *logs.Set
}

const maxLine = 240

func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > maxLine {
		return s[:maxLine] + "..."
	}
	return s
}

func stamp(e logs.Event) string {
	if e.TS.IsZero() {
		return "-"
	}
	return e.TS.Format("15:04:05.000")
}

// Overview lists files, line counts, time ranges and severity counts.
func (t *Tools) Overview() string {
	type agg struct {
		n, warn, err int
		first, last  time.Time
	}
	by := map[string]*agg{}
	var files []string
	for _, e := range t.set.Events {
		a := by[e.File]
		if a == nil {
			a = &agg{}
			by[e.File] = a
			files = append(files, e.File)
		}
		a.n++
		switch e.Level {
		case logs.Warn:
			a.warn++
		case logs.Error:
			a.err++
		}
		if !e.TS.IsZero() {
			if a.first.IsZero() || e.TS.Before(a.first) {
				a.first = e.TS
			}
			if e.TS.After(a.last) {
				a.last = e.TS
			}
		}
	}
	var b strings.Builder
	b.WriteString("file | records | warn | error | first | last\n")
	for _, f := range files {
		a := by[f]
		fr, la := "-", "-"
		if !a.first.IsZero() {
			fr, la = a.first.Format("2006-01-02 15:04:05"), a.last.Format("15:04:05")
		}
		fmt.Fprintf(&b, "%s | %d | %d | %d | %s | %s\n", f, a.n, a.warn, a.err, fr, la)
	}
	b.WriteString("\nscheduler.log:\n")
	for _, e := range t.set.Events {
		if e.Source == "scheduler" {
			fmt.Fprintf(&b, "%s %s %s\n", e.ID, stamp(e), clip(e.Text))
		}
	}
	b.WriteString("\nNote: per-node clocks may be skewed by a few seconds.\n")
	return b.String()
}

// Anomalies clusters warning-or-worse records by template.
func (t *Tools) Anomalies() string {
	type cl struct {
		tmpl        string
		count       int
		first, last logs.Event
		nodes       map[string]bool
	}
	by := map[string]*cl{}
	var order []*cl
	for _, e := range t.set.Events {
		if e.Level < logs.Warn {
			continue
		}
		k := logs.Template(e)
		c := by[k]
		if c == nil {
			c = &cl{tmpl: k, first: e, nodes: map[string]bool{}}
			by[k] = c
			order = append(order, c)
		}
		c.count++
		c.last = e
		if e.Node != "" {
			c.nodes[e.Node] = true
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].first.TS.Before(order[j].first.TS) })
	start := t.jobStart()
	var b strings.Builder
	if !start.IsZero() {
		fmt.Fprintf(&b, "job start (scheduler): %s\n", start.Format("2006-01-02 15:04:05"))
	}
	// Templates that only occurred before the job started go in their own
	// section, so they are not read as candidates alongside in-job events.
	var during, before []*cl
	for _, c := range order {
		if !start.IsZero() && c.last.TS.Before(start.Add(-10*time.Second)) {
			before = append(before, c)
		} else {
			during = append(during, c)
		}
	}
	write := func(list []*cl) {
		for i, c := range list {
			if i == 40 {
				fmt.Fprintf(&b, "... %d more\n", len(list)-40)
				break
			}
			var ns []string
			for n := range c.nodes {
				ns = append(ns, n)
			}
			sort.Strings(ns)
			fmt.Fprintf(&b, "- x%d first=%s %s%s last=%s nodes=%s\n  example: %s\n", c.count, c.first.ID, stamp(c.first), relStart(start, c.first), c.last.ID, strings.Join(ns, ","), clip(c.first.Text))
		}
	}
	fmt.Fprintf(&b, "DURING the job: %d warning/error templates, ordered by first occurrence:\n", len(during))
	write(during)
	if len(before) > 0 {
		fmt.Fprintf(&b, "\nBEFORE the job started: %d templates\n", len(before))
		write(before)
	}
	return b.String()
}

var jobStartRe = regexp.MustCompile(`job \d+ step \d+ started`)

func (t *Tools) jobStart() time.Time {
	for _, e := range t.set.Events {
		if e.Source == "scheduler" && jobStartRe.MatchString(e.Text) {
			return e.TS
		}
	}
	return time.Time{}
}

// relStart renders an offset from job start: " (+11m6s)" or " (BEFORE job start, -41m0s)".
func relStart(start time.Time, e logs.Event) string {
	if start.IsZero() || e.TS.IsZero() {
		return ""
	}
	d := e.TS.Sub(start).Round(time.Second)
	if d < -10*time.Second {
		return fmt.Sprintf(" (BEFORE job start, %s)", d)
	}
	return fmt.Sprintf(" (+%s)", d)
}

var kvRe = regexp.MustCompile(`(\w+)=("[^"]*"|\S+)`)

// MetricsSummary aggregates logfmt samples per node and gpu.
func (t *Tools) MetricsSummary() string {
	type field struct {
		min, max float64
		numeric  bool
		values   map[string]bool
		firstID  map[string]string // categorical value -> first record showing it
	}
	type row struct {
		node, gpu   string
		samples     int
		first, last logs.Event
		fields      map[string]*field
		order       []string
	}
	rows := map[string]*row{}
	var keys []string
	for _, e := range t.set.Events {
		if e.Source != "metrics" {
			continue
		}
		kv := map[string]string{}
		var ord []string
		for _, m := range kvRe.FindAllStringSubmatch(e.Text, -1) {
			kv[m[1]] = strings.Trim(m[2], `"`)
			ord = append(ord, m[1])
		}
		k := kv["node"] + "/gpu" + kv["gpu"]
		r := rows[k]
		if r == nil {
			r = &row{node: kv["node"], gpu: kv["gpu"], first: e, fields: map[string]*field{}}
			rows[k] = r
			keys = append(keys, k)
		}
		r.samples++
		r.last = e
		for _, name := range ord {
			if name == "node" || name == "gpu" {
				continue
			}
			v := kv[name]
			f := r.fields[name]
			if f == nil {
				f = &field{values: map[string]bool{}, firstID: map[string]string{}, numeric: true}
				r.fields[name] = f
				r.order = append(r.order, name)
			}
			if x, err := strconv.ParseFloat(v, 64); err == nil && f.numeric {
				if len(f.values) == 0 || x < f.min {
					f.min = x
				}
				if len(f.values) == 0 || x > f.max {
					f.max = x
				}
			} else {
				f.numeric = false
			}
			if len(f.values) < 4 {
				f.values[v] = true
			}
			if _, ok := f.firstID[v]; !ok && len(f.firstID) < 4 {
				f.firstID[v] = e.ID
			}
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("per GPU over all samples: sample count, last sample id, then each field as min..max (numeric) or its distinct values with the first record showing each\n")
	for _, k := range keys {
		r := rows[k]
		var parts []string
		for _, name := range r.order {
			f := r.fields[name]
			if f.numeric {
				if f.min == f.max {
					parts = append(parts, fmt.Sprintf("%s=%g", name, f.min))
				} else {
					parts = append(parts, fmt.Sprintf("%s=%g..%g", name, f.min, f.max))
				}
				continue
			}
			var vs []string
			for v := range f.values {
				vs = append(vs, v)
			}
			sort.Strings(vs)
			if len(vs) == 1 {
				parts = append(parts, fmt.Sprintf("%s=%s", name, vs[0]))
				continue
			}
			// Several values: say where each first appeared, so a change is citable.
			for i, v := range vs {
				vs[i] = v + " first@" + f.firstID[v]
			}
			parts = append(parts, fmt.Sprintf("%s={%s}", name, strings.Join(vs, "; ")))
		}
		fmt.Fprintf(&b, "%s n=%d last=%s %s\n", k, r.samples, r.last.ID, strings.Join(parts, " "))
	}
	return b.String()
}

// Search runs a case-insensitive regex over one file or all files.
func (t *Tools) Search(pattern, file string, limit int, last bool) string {
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return "invalid regex: " + err.Error()
	}
	var hits []logs.Event
	for _, e := range t.set.Events {
		if file != "" && e.File != file && !strings.HasPrefix(e.File, strings.TrimSuffix(file, "/")+"/") {
			continue
		}
		if re.MatchString(e.Text) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return "no matches"
	}
	shown, which := hits, "all"
	if len(hits) > limit {
		if last {
			shown, which = hits[len(hits)-limit:], fmt.Sprintf("last %d", limit)
		} else {
			shown, which = hits[:limit], fmt.Sprintf("first %d (set last=true for the latest)", limit)
		}
	}
	var b strings.Builder
	for _, e := range shown {
		fmt.Fprintf(&b, "%s %s %s\n", e.ID, stamp(e), clip(e.Text))
	}
	if len(hits) > limit {
		fmt.Fprintf(&b, "... %d matches total, showing %s\n", len(hits), which)
	}
	return b.String()
}

// Context shows records around an id in the same file.
func (t *Tools) Context(id string, before, after int) string {
	i := t.set.Index(id)
	if i < 0 {
		return "unknown id " + id
	}
	if before < 0 || before > 15 {
		before = 5
	}
	if after < 0 || after > 15 {
		after = 5
	}
	file := t.set.Events[i].File
	var b strings.Builder
	for j := i - before; j <= i+after; j++ {
		if j < 0 || j >= len(t.set.Events) || t.set.Events[j].File != file {
			continue
		}
		e := t.set.Events[j]
		mark := "  "
		if j == i {
			mark = "> "
		}
		fmt.Fprintf(&b, "%s%s %s %s\n", mark, e.ID, stamp(e), clip(e.Text))
	}
	return b.String()
}
