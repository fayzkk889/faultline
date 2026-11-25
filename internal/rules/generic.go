package rules

import (
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/fayzkk889/faultline/internal/eval"
	"github.com/fayzkk889/faultline/internal/logs"
	"github.com/fayzkk889/faultline/internal/scenario"
)

var (
	pciInTextRe  = regexp.MustCompile(`\b([0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2})(\.\d)?\b`)
	gpuFieldText = regexp.MustCompile(`\bgpu=(\d+)\b`)
	nodeInText   = regexp.MustCompile(`\b(node\d+)\b`)
)

// Generic localizes without any fault vocabulary: the earliest error-level
// record during the job whose message template occurs exactly once in the
// scenario. It always answers Other, so it measures localization only.
// Suggested by the independent review as a stronger floor than the frozen
// rules' "earliest watchdog timeout" fallback.
func Generic(set *logs.Set) eval.Diagnosis {
	start := jobStart(set)
	count := map[string]int{}
	for _, e := range set.Events {
		count[logs.Template(e)]++
	}
	var cands []logs.Event
	for _, e := range set.Events {
		if e.Level != logs.Error || e.Source == "hw" {
			continue
		}
		if !start.IsZero() && e.TS.Before(start.Add(-10*time.Second)) {
			continue
		}
		if count[logs.Template(e)] == 1 {
			cands = append(cands, e)
		}
	}
	if len(cands) == 0 {
		return eval.Diagnosis{Fault: scenario.Other, GPU: -1, Explanation: "generic: no unique in-job error"}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].TS.Before(cands[j].TS) })
	e := cands[0]
	node := e.Node
	if node == "" {
		if m := nodeInText.FindStringSubmatch(e.Text); m != nil {
			node = m[1]
		}
	}
	gpu := -1
	if m := pciInTextRe.FindStringSubmatch(e.Text); m != nil {
		gpu = lookup(pciMap(set), node, m[1])
	} else if m := gpuFieldText.FindStringSubmatch(e.Text); m != nil {
		gpu, _ = strconv.Atoi(m[1])
	}
	return eval.Diagnosis{Fault: scenario.Other, Node: node, GPU: gpu, Evidence: []string{e.ID}, Explanation: "generic: earliest unique in-job error"}
}
