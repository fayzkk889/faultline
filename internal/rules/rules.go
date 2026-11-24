// Package rules is the signature-based baseline: what a careful SRE writes
// after reading the dev-split logs. It was committed before the test-split
// phrasings existed (see DESIGN.md, "Methodology guard").
package rules

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

// candidate is one signature hit.
type candidate struct {
	prio  int // lower wins
	fault string
	node  string
	gpu   int
	ts    time.Time
	ids   []string
	why   string
}

var (
	xidRe      = regexp.MustCompile(`NVRM: Xid \(PCI:([0-9a-f:]+)\): (\d+),`)
	offBusRe   = regexp.MustCompile(`NVRM: GPU ([0-9a-f:.]+): GPU has fallen off the bus`)
	linkRe     = regexp.MustCompile(`mlx5_core \S+ (\S+): Link (down|up)`)
	oomRe      = regexp.MustCompile(`Out of memory: Killed process`)
	enospcRe   = regexp.MustCompile(`No space left on device|Errno 28`)
	gpuLostRe  = regexp.MustCompile(`gpu=(\d+) error=".*(GPU is lost|Unable to determine the device handle)`)
	gpuFieldRe = regexp.MustCompile(`\bgpu=(\d+)\b`)
	clocksRe   = regexp.MustCompile(`clocks_event=(\S+)`)
	dbeRe      = regexp.MustCompile(`ecc_dbe=([1-9]\d*)`)
	hwGPURe    = regexp.MustCompile(`^gpu (\d+) pci (\S+)`)
	jobStartRe = regexp.MustCompile(`job \d+ step 0 started`)
	watchdogRe = regexp.MustCompile(`Watchdog caught collective operation timeout`)
)

// xidFault maps the Xids seen in dev logs. 13 is an application fault from a
// previous job and is deliberately ignored.
var xidFault = map[int]string{
	79: "gpu_fell_off_bus",
	48: "gpu_ecc_uncorrectable",
	63: "gpu_ecc_uncorrectable",
}

// Diagnose runs the baseline over one scenario.
func Diagnose(set *logs.Set) eval.Diagnosis {
	pci := pciMap(set)
	start := jobStart(set)
	var cands []candidate
	add := func(c candidate) {
		if !start.IsZero() && c.ts.Before(start.Add(-10*time.Second)) {
			return
		}
		cands = append(cands, c)
	}

	// Link-down events per (node, netdev), to discard flaps that recover.
	type link struct {
		down logs.Event
		up   *logs.Event
	}
	links := map[string]*link{}
	var linkOrder []string

	thermal := map[string][]logs.Event{} // node/gpu -> consecutive throttled samples

	for _, e := range set.Events {
		switch e.Source {
		case "kern":
			if m := xidRe.FindStringSubmatch(e.Text); m != nil {
				x, _ := strconv.Atoi(m[2])
				if f, ok := xidFault[x]; ok {
					add(candidate{prio: 0, fault: f, node: e.Node, gpu: lookup(pci, e.Node, m[1]), ts: e.TS, ids: []string{e.ID}, why: "Xid " + m[2]})
				}
				continue
			}
			if m := offBusRe.FindStringSubmatch(e.Text); m != nil {
				add(candidate{prio: 0, fault: "gpu_fell_off_bus", node: e.Node, gpu: lookup(pci, e.Node, m[1]), ts: e.TS, ids: []string{e.ID}, why: "fell off the bus"})
				continue
			}
			if m := linkRe.FindStringSubmatch(e.Text); m != nil {
				key := e.Node + "/" + m[1]
				if m[2] == "down" {
					links[key] = &link{down: e}
					linkOrder = append(linkOrder, key)
				} else if l := links[key]; l != nil && l.up == nil && e.TS.Sub(l.down.TS) < 10*time.Second {
					ev := e
					l.up = &ev
				}
				continue
			}
			if oomRe.MatchString(e.Text) {
				add(candidate{prio: 1, fault: "host_oom", node: e.Node, gpu: -1, ts: e.TS, ids: []string{e.ID}, why: "oom-killer"})
			}
		case "metrics":
			if m := gpuLostRe.FindStringSubmatch(e.Text); m != nil {
				g, _ := strconv.Atoi(m[1])
				add(candidate{prio: 0, fault: "gpu_fell_off_bus", node: e.Node, gpu: g, ts: e.TS, ids: []string{e.ID}, why: "GPU lost in metrics"})
				continue
			}
			g := -1
			if m := gpuFieldRe.FindStringSubmatch(e.Text); m != nil {
				g, _ = strconv.Atoi(m[1])
			}
			if m := dbeRe.FindStringSubmatch(e.Text); m != nil {
				add(candidate{prio: 0, fault: "gpu_ecc_uncorrectable", node: e.Node, gpu: g, ts: e.TS, ids: []string{e.ID}, why: "ecc_dbe>0"})
			}
			key := e.Node + "/" + strconv.Itoa(g)
			if m := clocksRe.FindStringSubmatch(e.Text); m != nil && m[1] != "none" && (strings.Contains(m[1], "slowdown") || strings.Contains(m[1], "thermal")) {
				thermal[key] = append(thermal[key], e)
				if len(thermal[key]) == 3 {
					s := thermal[key]
					add(candidate{prio: 2, fault: "gpu_thermal_slowdown", node: e.Node, gpu: g, ts: s[0].TS, ids: []string{s[0].ID, s[1].ID, s[2].ID}, why: "throttled 3 samples"})
				}
			} else {
				delete(thermal, key)
			}
		case "train":
			if enospcRe.MatchString(e.Text) {
				add(candidate{prio: 1, fault: "storage_full", node: e.Node, gpu: -1, ts: e.TS, ids: []string{e.ID}, why: "ENOSPC"})
			}
		}
	}
	for _, key := range linkOrder {
		l := links[key]
		if l.up == nil {
			add(candidate{prio: 1, fault: "ib_link_down", node: l.down.Node, gpu: -1, ts: l.down.TS, ids: []string{l.down.ID}, why: "link down, no recovery"})
		}
	}

	if len(cands) > 0 {
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].prio != cands[j].prio {
				return cands[i].prio < cands[j].prio
			}
			return cands[i].ts.Before(cands[j].ts)
		})
		c := cands[0]
		return eval.Diagnosis{Fault: c.fault, Node: c.node, GPU: c.gpu, Evidence: c.ids, Explanation: "rule: " + c.why}
	}

	// No signature: report the earliest watchdog timeout as an unknown fault.
	for _, e := range byTime(set) {
		if watchdogRe.MatchString(e.Text) {
			return eval.Diagnosis{Fault: scenario.Other, Node: e.Node, GPU: -1, Evidence: []string{e.ID}, Explanation: "rule: no signature; earliest collective timeout"}
		}
	}
	return eval.Diagnosis{Fault: scenario.Other, GPU: -1, Explanation: "rule: no signature and no timeout"}
}

func pciMap(set *logs.Set) map[string]int {
	out := map[string]int{}
	for _, e := range set.Events {
		if e.Source != "hw" {
			continue
		}
		if m := hwGPURe.FindStringSubmatch(e.Text); m != nil {
			g, _ := strconv.Atoi(m[1])
			out[e.Node+"|"+strings.TrimSuffix(m[2], ".0")] = g
		}
	}
	return out
}

func lookup(pci map[string]int, node, bus string) int {
	if g, ok := pci[node+"|"+strings.TrimSuffix(bus, ".0")]; ok {
		return g
	}
	return -1
}

func jobStart(set *logs.Set) time.Time {
	for _, e := range set.Events {
		if e.Source == "scheduler" && jobStartRe.MatchString(e.Text) {
			return e.TS
		}
	}
	return time.Time{}
}

func byTime(set *logs.Set) []logs.Event {
	evs := append([]logs.Event(nil), set.Events...)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS.Before(evs[j].TS) })
	return evs
}
