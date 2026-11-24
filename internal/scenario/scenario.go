// Package scenario generates synthetic multi-node training-job logs with one
// injected root fault, its causal cascade, benign noise, and ground truth.
package scenario

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Known faults form the label space every diagnoser answers in.
var Known = []string{
	"gpu_fell_off_bus",
	"gpu_ecc_uncorrectable",
	"ib_link_down",
	"host_oom",
	"gpu_thermal_slowdown",
	"storage_full",
}

// Other is the answer expected for a fault outside Known.
const Other = "other"

// IsKnown reports whether f is in the known label space.
func IsKnown(f string) bool {
	for _, k := range Known {
		if k == f {
			return true
		}
	}
	return false
}

// Spec identifies one scenario.
type Spec struct {
	Name    string `json:"name"`
	Split   string `json:"split"`
	Fault   string `json:"fault"`
	Variant string `json:"variant"`
	Seed    int64  `json:"seed"`
}

// Truth is the ground truth written to truth.json.
type Truth struct {
	Spec
	Expected     string   `json:"expected"` // label a correct diagnoser answers (Other for held-out faults)
	Node         string   `json:"node"`
	GPU          int      `json:"gpu"`
	RootEvidence []string `json:"root_evidence"`
	FaultTime    string   `json:"fault_time"`
}

const (
	nodes       = 4
	gpusPerNode = 8
	timeoutSec  = 300
	jobLimit    = 40 * time.Minute
)

var busIDs = []string{"18", "2a", "3a", "5d", "9a", "ab", "ba", "db"}

func pciShort(gpu int) string { return "0000:" + busIDs[gpu] + ":00" }
func pciFull(gpu int) string  { return "0000:" + busIDs[gpu] + ":00.0" }
func nodeName(i int) string   { return fmt.Sprintf("node%02d", i+1) }
func nodeIP(i int) string     { return fmt.Sprintf("10.10.%d.%d", i+1, 11+i) }

type entry struct {
	t    time.Time // true time; skew is applied when formatting
	text string    // fully formatted later by the file kind
	root bool
	cont []string // continuation lines (tracebacks)
	lvl  string   // train level
	rank int
}

type file struct {
	kind    string // kern, train, metrics, scheduler, hw
	node    int
	entries []entry
}

type gen struct {
	spec   Spec
	rng    *rand.Rand
	t0     time.Time
	tf     time.Time
	end    time.Time
	boot   time.Time
	skew   [nodes]time.Duration
	jobID  int
	files  map[string]*file
	fNode  int
	fGPU   int
	crash  bool              // job dies by watchdog timeout
	stopAt map[int]time.Time // rank -> time its step logging stops
}

// Generate writes the scenario into dir (which must not exist) and returns its truth.
func Generate(spec Spec, dir string) (Truth, error) {
	g := &gen{spec: spec, rng: rand.New(rand.NewSource(spec.Seed)), files: map[string]*file{}, stopAt: map[int]time.Time{}}
	g.t0 = time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC).Add(time.Duration(g.rng.Intn(600)) * time.Minute)
	g.tf = g.t0.Add(8*time.Minute + time.Duration(g.rng.Intn(12*60))*time.Second)
	g.boot = g.t0.Add(-time.Duration(48+g.rng.Intn(72)) * time.Hour)
	g.jobID = 4000 + g.rng.Intn(900)
	for i := range g.skew {
		g.skew[i] = time.Duration(g.rng.Intn(6001)-3000) * time.Millisecond
	}
	g.fNode = g.rng.Intn(nodes)
	g.fGPU = g.rng.Intn(gpusPerNode)
	for n := 0; n < nodes; n++ {
		for _, k := range []string{"kern", "train", "metrics", "hw"} {
			g.files[fileKey(n, k)] = &file{kind: k, node: n}
		}
	}
	g.files["scheduler.log"] = &file{kind: "scheduler", node: -1}

	truth := Truth{Spec: spec, Expected: spec.Fault, Node: nodeName(g.fNode), GPU: g.fGPU, FaultTime: g.tf.Format(time.RFC3339Nano)}
	if !IsKnown(spec.Fault) {
		truth.Expected = Other
	}
	switch spec.Fault {
	case "host_oom", "storage_full":
		truth.GPU = -1
	case "ib_link_down":
		truth.GPU = -1
	}
	if spec.Fault == "storage_full" {
		g.fNode = 0 // rank 0 writes checkpoints
		truth.Node = nodeName(0)
	}

	g.crash = spec.Fault != "gpu_thermal_slowdown"
	if g.crash {
		g.end = g.tf.Add(timeoutSec*time.Second + 5*time.Second)
	} else {
		g.end = g.t0.Add(jobLimit)
	}

	g.hardware()
	g.noise()
	if err := g.inject(); err != nil {
		return Truth{}, err
	}
	g.training()
	g.metrics()
	g.scheduler()

	ids, err := g.write(dir)
	if err != nil {
		return Truth{}, err
	}
	truth.RootEvidence = ids
	if len(ids) == 0 {
		return Truth{}, fmt.Errorf("%s: no root evidence emitted", spec.Name)
	}
	b, _ := json.MarshalIndent(truth, "", "  ")
	return truth, os.WriteFile(filepath.Join(dir, "truth.json"), append(b, '\n'), 0o644)
}

func fileKey(n int, kind string) string {
	name := map[string]string{"kern": "kern.log", "train": "train.log", "metrics": "metrics.log", "hw": "hw.txt"}[kind]
	return nodeName(n) + "/" + name
}

func (g *gen) add(key string, e entry) { f := g.files[key]; f.entries = append(f.entries, e) }

func (g *gen) kern(n int, t time.Time, prio, text string, root bool) {
	g.add(fileKey(n, "kern"), entry{t: t, text: text, lvl: prio, root: root})
}

func (g *gen) train(n, rank int, t time.Time, lvl, text string, root bool, cont ...string) {
	g.add(fileKey(n, "train"), entry{t: t, text: text, lvl: lvl, rank: rank, root: root, cont: cont})
}

func (g *gen) sched(t time.Time, text string, root bool) {
	g.add("scheduler.log", entry{t: t, text: text, root: root})
}

func (g *gen) jitter(max time.Duration) time.Duration {
	return time.Duration(g.rng.Int63n(int64(max) + 1))
}

func rankOf(n, gpu int) int { return n*gpusPerNode + gpu }

func (g *gen) hardware() {
	for n := 0; n < nodes; n++ {
		k := fileKey(n, "hw")
		g.add(k, entry{text: fmt.Sprintf("host %s ip %s", nodeName(n), nodeIP(n))})
		for i := 0; i < gpusPerNode; i++ {
			g.add(k, entry{text: fmt.Sprintf("gpu %d pci %s model H100-SXM5-80GB uuid GPU-%08x", i, pciFull(i), g.rng.Uint32())})
		}
		for i := 0; i < 4; i++ {
			g.add(k, entry{text: fmt.Sprintf("nic mlx5_%d pci 0000:%02x:00.0 netdev ibp%ds0 port 1", i, 0x0c+i*0x30, 0x0c+i*0x30)})
		}
	}
}

func (g *gen) noise() {
	// Benign periodic kernel lines on every node.
	benign := []string{
		"perf: interrupt took too long (2512 > 2500), lowering kernel.perf_event_max_sample_rate to 79500",
		"nvidia-nvlink: Unregistered Nvlink Core, major device number 234",
		"audit: type=1400 audit(%d.123:%d): apparmor=\"STATUS\" operation=\"profile_replace\" name=\"snap-update-ns\"",
		"NFSD: Using nfsdcld client tracking operations.",
		"systemd-journald[812]: Data hash table of /var/log/journal/system.journal has a fill level at 75.1, suggesting rotation.",
	}
	for n := 0; n < nodes; n++ {
		for t := g.t0.Add(-90 * time.Minute); t.Before(g.end.Add(2 * time.Minute)); t = t.Add(time.Duration(90+g.rng.Intn(240)) * time.Second) {
			msg := benign[g.rng.Intn(len(benign))]
			if strings.Contains(msg, "%d") {
				msg = fmt.Sprintf(msg, t.Unix(), g.rng.Intn(9000)+1000)
			}
			g.kern(n, t, "info", msg, false)
		}
	}
	// A benign Xid 13 from a previous job, before this job started, on some node.
	xn, xg := g.rng.Intn(nodes), g.rng.Intn(gpusPerNode)
	g.kern(xn, g.t0.Add(-time.Duration(20+g.rng.Intn(40))*time.Minute), "err",
		fmt.Sprintf("NVRM: Xid (PCI:%s): 13, pid=%d, name=python3, Graphics SM Warp Exception on (GPC 1, TPC 2, SM 0): Out Of Range Address", pciShort(xg), 60000+g.rng.Intn(9999)), false)
	// A transient NIC flap that recovers, on a node other than the faulty one, before the fault.
	fn := (g.fNode + 1 + g.rng.Intn(nodes-1)) % nodes
	nic := g.rng.Intn(4)
	ft := g.t0.Add(time.Duration(2*60+g.rng.Intn(4*60)) * time.Second)
	g.kern(fn, ft, "info", fmt.Sprintf("mlx5_core 0000:%02x:00.0 ibp%ds0: Link down", 0x0c+nic*0x30, 0x0c+nic*0x30), false)
	g.kern(fn, ft.Add(1200*time.Millisecond+g.jitter(800*time.Millisecond)), "info", fmt.Sprintf("mlx5_core 0000:%02x:00.0 ibp%ds0: Link up", 0x0c+nic*0x30, 0x0c+nic*0x30), false)
}

func (g *gen) inject() error {
	n, gpu, tf := g.fNode, g.fGPU, g.tf
	r := rankOf(n, gpu)
	pid := 90000 + g.rng.Intn(9000)
	switch g.spec.Fault {
	case "gpu_fell_off_bus":
		if g.b() {
			// Older driver: no Xid line; the re-init attempt fails and the exporter silently drops the GPU.
			g.kern(n, tf.Add(800*time.Millisecond), "err", fmt.Sprintf("NVRM: GPU %s: RmInitAdapter failed! (0x26:0x56:1474)", pciFull(gpu)), true)
			g.kern(n, tf.Add(810*time.Millisecond), "err", fmt.Sprintf("NVRM: GPU %s: rm_init_adapter failed, device minor number %d", pciFull(gpu), gpu), true)
			break
		}
		g.kern(n, tf, "err", fmt.Sprintf("NVRM: Xid (PCI:%s): 79, pid=%d, name=python3, GPU has fallen off the bus.", pciShort(gpu), pid), true)
		g.kern(n, tf.Add(3*time.Millisecond), "err", fmt.Sprintf("NVRM: GPU %s: GPU has fallen off the bus.", pciFull(gpu)), true)
	case "gpu_ecc_uncorrectable":
		if g.b() {
			// Newer architecture: contained ECC error reported as Xid 94; the process sees an illegal access.
			g.kern(n, tf, "err", fmt.Sprintf("NVRM: Xid (PCI:%s): 94, pid=%d, name=python3, Contained: CE User Channel (0x9). RST: No, D-RST: No", pciShort(gpu), pid), true)
			g.crashRank(n, r, tf.Add(200*time.Millisecond+g.jitter(300*time.Millisecond)), false, "CUDA error during forward pass",
				"Traceback (most recent call last):",
				"  File \"/workspace/train.py\", line 412, in train_step",
				"    loss = model(batch).loss",
				"RuntimeError: CUDA error: an illegal memory access was encountered")
			break
		}
		g.kern(n, tf, "err", fmt.Sprintf("NVRM: Xid (PCI:%s): 48, pid=%d, name=python3, An uncorrectable double bit error (DBE) has been detected on GPU in the framebuffer at partition 6, subpartition 0.", pciShort(gpu), pid), true)
		g.kern(n, tf.Add(40*time.Millisecond), "err", fmt.Sprintf("NVRM: Xid (PCI:%s): 63, pid=%d, name=python3, Row Remapper: New row marked for remapping, reset gpu to activate.", pciShort(gpu), pid), true)
		g.crashRank(n, r, tf.Add(200*time.Millisecond+g.jitter(300*time.Millisecond)), true, "CUDA error during forward pass",
			"Traceback (most recent call last):",
			"  File \"/workspace/train.py\", line 412, in train_step",
			"    loss = model(batch).loss",
			"RuntimeError: CUDA error: uncorrectable ECC error encountered",
			"CUDA kernel errors might be asynchronously reported at some other API call, so the stacktrace below might be incorrect.")
	case "ib_link_down":
		nic := g.rng.Intn(4)
		if g.b() {
			// Transceiver fault reported as a module event; no explicit link-down line.
			g.kern(n, tf, "err", fmt.Sprintf("mlx5_core 0000:%02x:00.0: Port module event[error]: module %d, Cable error, Power budget exceeded", 0x0c+nic*0x30, nic), true)
		} else {
			g.kern(n, tf, "info", fmt.Sprintf("mlx5_core 0000:%02x:00.0 ibp%ds0: Link down", 0x0c+nic*0x30, 0x0c+nic*0x30), true)
		}
		g.ibErrors(n, tf)
	case "host_oom":
		if g.b() {
			// Containerized job: the cgroup OOM is reported by the scheduler, not the host kernel log.
			g.sched(tf.Add(2*time.Second), fmt.Sprintf("job %d step 0: Detected 1 oom_kill event in StepId=%d.0 on %s. Some of the step tasks have been OOM Killed.", g.jobID, g.jobID, nodeName(n)), true)
		} else {
			g.kern(n, tf, "warning", fmt.Sprintf("pt_data_worker invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0, oom_score_adj=0"), true)
			g.kern(n, tf.Add(12*time.Millisecond), "err", fmt.Sprintf("Out of memory: Killed process %d (pt_data_worker) total-vm:48123412kB, anon-rss:31234124kB, file-rss:0kB, shmem-rss:0kB, UID:1000 pgtables:61234kB oom_score_adj:0", pid), true)
		}
		g.crashRank(n, rankOf(n, g.rng.Intn(gpusPerNode)), tf.Add(500*time.Millisecond+g.jitter(time.Second)), true, "Exception in training loop",
			"Traceback (most recent call last):",
			"  File \"/usr/lib/python3/dist-packages/torch/utils/data/dataloader.py\", line 1251, in _try_get_data",
			"    data = self._data_queue.get(timeout=timeout)",
			fmt.Sprintf("RuntimeError: DataLoader worker (pid %d) is killed by signal: Killed.", pid))
	case "gpu_thermal_slowdown":
		// Evidence lives in metrics; see metrics().
	case "storage_full":
		errLine := "OSError: [Errno 28] No space left on device"
		if g.b() {
			errLine = "OSError: [Errno 122] Disk quota exceeded"
		}
		g.crashRank(0, 0, tf.Add(30*time.Second+g.jitter(10*time.Second)), true, "Checkpoint save failed",
			"Traceback (most recent call last):",
			"  File \"/workspace/ckpt.py\", line 88, in save",
			"    torch.save(state, path)",
			fmt.Sprintf("%s: '/lustre/ckpt/run-7/step_%06d/model_states.pt'", errLine, 1000*(1+g.rng.Intn(9))))
	case "nvlink_error":
		// Held out: not in Known, so the expected answer is Other.
		g.kern(n, tf, "err", fmt.Sprintf("NVRM: Xid (PCI:%s): 74, pid=%d, name=python3, NVLink: fatal error detected on link 3(0x0, 0x0, 0x10000, 0x0, 0x0, 0x0, 0x0)", pciShort(gpu), pid), true)
		g.crashRank(n, r, tf.Add(150*time.Millisecond+g.jitter(200*time.Millisecond)), false, "NCCL operation failed",
			"Traceback (most recent call last):",
			"  File \"/workspace/train.py\", line 430, in train_step",
			"    dist.all_reduce(grads)",
			"torch.distributed.DistBackendError: NCCL error in: ProcessGroupNCCL.cpp:2033, unhandled cuda error (run with NCCL_DEBUG=INFO for details), NCCL version 2.21.5")
	default:
		return fmt.Errorf("unknown fault %q", g.spec.Fault)
	}
	return nil
}

// crashRank makes one rank fail with an error and traceback; the cascade follows in training().
func (g *gen) crashRank(n, r int, t time.Time, root bool, msg string, tb ...string) {
	g.stopAt[r] = t
	g.train(n, r, t, "ERROR", msg, root, tb...)
}

func (g *gen) b() bool { return g.spec.Variant == "B" }

// ibErrors emits IB completion errors on the faulty node and on two peers, all within ~2 s.
func (g *gen) ibErrors(n int, tf time.Time) {
	peers := []int{n}
	for len(peers) < 3 {
		p := g.rng.Intn(nodes)
		dup := false
		for _, q := range peers {
			dup = dup || q == p
		}
		if !dup {
			peers = append(peers, p)
		}
	}
	for i, p := range peers {
		remote := n
		if p == n {
			remote = peers[1+g.rng.Intn(2)]
		}
		r := rankOf(p, g.rng.Intn(gpusPerNode))
		t := tf.Add(time.Duration(50+g.rng.Intn(1500))*time.Millisecond + time.Duration(i)*time.Millisecond)
		g.train(p, r, t, "WARNING", fmt.Sprintf("NCCL WARN NET/IB : Got completion from peer %s<%d> with status=12 opcode=0 len=0 vendor err 129 (Recv) localGid ::ffff:%s remoteGids::ffff:%s",
			nodeIP(remote), 40000+g.rng.Intn(9999), nodeIP(p), nodeIP(remote)), false)
	}
}

func (g *gen) training() {
	stepTime := 0.84
	total := nodes * gpusPerNode
	for n := 0; n < nodes; n++ {
		for l := 0; l < gpusPerNode; l++ {
			r := rankOf(n, l)
			start := g.t0.Add(time.Duration(8+g.rng.Intn(4000)) * time.Millisecond)
			g.train(n, r, start, "INFO", fmt.Sprintf("Initialized process group: backend=nccl world_size=%d local_rank=%d", total, l), false)
			g.train(n, r, start.Add(400*time.Millisecond), "WARNING", "NCCL WARN NET/Plugin: Could not find: libnccl-net.so. Using internal network plugin.", false)
		}
	}
	g.train(0, 0, g.t0.Add(6*time.Second), "INFO", "Loaded model config: params=13.1B seq_len=4096 global_batch=1024 grad_accum=4", false)

	// Step logs every 50 steps. All ranks log the same step at nearly the same time.
	step := 0
	t := g.t0.Add(20 * time.Second)
	for {
		dt := stepTime
		if !g.crash && t.After(g.tf) {
			dt = 1.55 // a throttled GPU slows every collective
		}
		t = t.Add(time.Duration(50*dt*float64(time.Second)) + g.jitter(time.Second))
		step += 50
		if g.crash && !t.Before(g.tf) {
			break
		}
		if !t.Before(g.end) {
			break
		}
		loss := 3.1 - 0.9*float64(step)/6000 + g.rng.Float64()*0.05
		for n := 0; n < nodes; n++ {
			for l := 0; l < gpusPerNode; l++ {
				r := rankOf(n, l)
				if s, ok := g.stopAt[r]; ok && !t.Before(s) {
					continue
				}
				st := dt + (g.rng.Float64()-0.5)*0.04
				g.train(n, r, t.Add(g.jitter(300*time.Millisecond)), "INFO", fmt.Sprintf("step %d loss %.4f step_time %.3fs tokens/s %d", step, loss, st, int(4096*1024/4/st/float64(total)*8)), false)
			}
		}
		if step%1000 == 0 {
			g.train(0, 0, t.Add(time.Second), "INFO", fmt.Sprintf("Saving checkpoint to /lustre/ckpt/run-7/step_%06d", step), false)
			g.train(0, 0, t.Add(42*time.Second), "INFO", fmt.Sprintf("Checkpoint saved in %.1fs", 38+g.rng.Float64()*6), false)
		}
	}
	if g.spec.Fault == "storage_full" {
		g.train(0, 0, g.tf, "INFO", fmt.Sprintf("Saving checkpoint to /lustre/ckpt/run-7/step_%06d", step), false)
	}
	if !g.crash {
		return
	}
	// Watchdog cascade: every rank not already dead times out.
	seq := 40000 + g.rng.Intn(20000)
	for n := 0; n < nodes; n++ {
		for l := 0; l < gpusPerNode; l++ {
			r := rankOf(n, l)
			if _, dead := g.stopAt[r]; dead {
				continue
			}
			wt := g.tf.Add(timeoutSec*time.Second + g.jitter(2*time.Second))
			g.train(n, r, wt, "ERROR", fmt.Sprintf("[PG 0 Rank %d] Watchdog caught collective operation timeout: WorkNCCL(SeqNum=%d, OpType=ALLREDUCE, NumelIn=262144, NumelOut=262144, Timeout(ms)=%d) ran for %d milliseconds before timing out.", r, seq, timeoutSec*1000, timeoutSec*1000+g.rng.Intn(90)), false)
			g.train(n, r, wt.Add(5*time.Millisecond), "ERROR", fmt.Sprintf("[PG 0 Rank %d] To avoid data inconsistency, we are taking the entire process down.", r), false)
		}
	}
}

func (g *gen) metrics() {
	hotN, hotG := g.rng.Intn(nodes), g.rng.Intn(gpusPerNode)
	if hotN == g.fNode && hotG == g.fGPU {
		hotG = (hotG + 1) % gpusPerNode
	}
	sbeN, sbeG := g.rng.Intn(nodes), g.rng.Intn(gpusPerNode)
	sbe := 0
	for t := g.t0.Add(-60 * time.Second); t.Before(g.end.Add(60 * time.Second)); t = t.Add(30 * time.Second) {
		running := t.After(g.t0) && t.Before(g.end)
		for n := 0; n < nodes; n++ {
			for gp := 0; gp < gpusPerNode; gp++ {
				isF := n == g.fNode && gp == g.fGPU
				after := !t.Before(g.tf)
				util, temp, clk, pw := 0, 34+g.rng.Intn(6), 1980, 90+g.rng.Intn(20)
				if running && !(g.crash && after) {
					util, temp, pw = 96+g.rng.Intn(4), 58+g.rng.Intn(10), 610+g.rng.Intn(80)
				}
				if n == hotN && gp == hotG && util > 0 {
					temp = 86 + g.rng.Intn(4)
				}
				reasons := "none"
				root := false
				if isF && g.spec.Fault == "gpu_thermal_slowdown" && after && running {
					temp, clk, reasons, root = 92+g.rng.Intn(4), 1080+g.rng.Intn(120), "hw_slowdown,sw_thermal", true
				}
				dbe := 0
				if isF && g.spec.Fault == "gpu_ecc_uncorrectable" && after && !g.b() {
					dbe, root = 1, true
				}
				if n == sbeN && gp == sbeG && g.rng.Intn(6) == 0 {
					sbe++
				}
				sb := 0
				if n == sbeN && gp == sbeG {
					sb = sbe
				}
				if g.b() {
					// Newer exporter: different ECC field names and throttle reasons as an NVML bitmask.
					if isF && g.spec.Fault == "gpu_fell_off_bus" && after {
						continue // the lost GPU silently disappears from the samples
					}
					if isF && g.spec.Fault == "gpu_ecc_uncorrectable" && after {
						dbe, root = 1, true
					}
					mask := 0x0
					if util == 0 {
						mask = 0x1 // GPU idle
					}
					if reasons != "none" {
						mask = 0x68 // HW slowdown | SW thermal | HW thermal
					}
					line := fmt.Sprintf("node=%s gpu=%d temp_c=%d sm_mhz=%d power_w=%d util=%d ecc_vol_corr=%d ecc_vol_uncorr=%d clocks_event_reasons=0x%016x", nodeName(n), gp, temp, clk, pw, util, sb, dbe, mask)
					g.add(fileKey(n, "metrics"), entry{t: t, text: line, root: root})
					continue
				}
				line := fmt.Sprintf("node=%s gpu=%d temp_c=%d sm_mhz=%d power_w=%d util=%d ecc_sbe=%d ecc_dbe=%d clocks_event=%s", nodeName(n), gp, temp, clk, pw, util, sb, dbe, reasons)
				if isF && g.spec.Fault == "gpu_fell_off_bus" && after {
					line, root = fmt.Sprintf("node=%s gpu=%d error=\"Unable to determine the device handle for GPU %s: GPU is lost\"", nodeName(n), gp, pciFull(gp)), true
				}
				g.add(fileKey(n, "metrics"), entry{t: t, text: line, root: root})
			}
		}
	}
}

func (g *gen) scheduler() {
	g.sched(g.t0, fmt.Sprintf("sched: Allocate JobId=%d NodeList=node[01-%02d] #CPUs=%d Partition=train", g.jobID, nodes, nodes*96), false)
	g.sched(g.t0.Add(time.Second), fmt.Sprintf("job %d step 0 started ntasks=%d", g.jobID, nodes*gpusPerNode), false)
	if g.crash {
		g.sched(g.end.Add(-2*time.Second), fmt.Sprintf("job %d step 0 FAILED exit_code=1 (one or more tasks exited with non-zero status)", g.jobID), false)
		g.sched(g.end, fmt.Sprintf("_job_complete: JobId=%d WEXITSTATUS 1", g.jobID), false)
	} else {
		g.sched(g.end, fmt.Sprintf("job %d TIMEOUT: time limit %d minutes reached", g.jobID, int(jobLimit.Minutes())), false)
	}
}

// write formats, orders and writes every file, returning root-evidence ids.
func (g *gen) write(dir string) ([]string, error) {
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("%s already exists", dir)
	}
	var keys []string
	for k := range g.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var roots []string
	for _, k := range keys {
		f := g.files[k]
		if f.kind != "hw" {
			sort.SliceStable(f.entries, func(i, j int) bool { return f.entries[i].t.Before(f.entries[j].t) })
		}
		var b strings.Builder
		line := 0
		for _, e := range f.entries {
			line++
			if e.root {
				roots = append(roots, fmt.Sprintf("%s:%d", k, line))
			}
			b.WriteString(g.format(f, e))
			b.WriteByte('\n')
			for _, c := range e.cont {
				line++
				b.WriteString(c)
				b.WriteByte('\n')
			}
		}
		p := filepath.Join(dir, filepath.FromSlash(k))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			return nil, err
		}
	}
	return roots, nil
}

func (g *gen) format(f *file, e entry) string {
	local := e.t
	if f.node >= 0 {
		local = e.t.Add(g.skew[f.node])
	}
	switch f.kind {
	case "kern":
		up := local.Sub(g.boot).Seconds()
		return fmt.Sprintf("%s %s kern.%s kernel: [%12.6f] %s", local.Format("2006-01-02T15:04:05.000000-07:00"), nodeName(f.node), e.lvl, up, e.text)
	case "train":
		return fmt.Sprintf("%s [rank %d] %s %s", local.Format("2006-01-02 15:04:05,000"), e.rank, e.lvl, e.text)
	case "metrics":
		return fmt.Sprintf("ts=%s %s", local.Format(time.RFC3339), e.text)
	case "scheduler":
		return fmt.Sprintf("[%s] %s", local.Format("2006-01-02T15:04:05.000"), e.text)
	}
	return e.text
}

// Suite lists the scenarios of a split. Seeds are fixed so suites are reproducible.
func Suite(split string) ([]Spec, error) {
	var out []Spec
	switch split {
	case "dev":
		for i, f := range Known {
			for s := 0; s < 3; s++ {
				out = append(out, Spec{Name: fmt.Sprintf("dev-%s-%d", f, s), Split: "dev", Fault: f, Variant: "A", Seed: int64(1000 + 10*i + s)})
			}
		}
	case "test":
		for i, f := range Known {
			for s := 0; s < 3; s++ {
				out = append(out, Spec{Name: fmt.Sprintf("test-%s-%d", f, s), Split: "test", Fault: f, Variant: "B", Seed: int64(2000 + 10*i + s)})
			}
		}
		for s := 0; s < 4; s++ {
			out = append(out, Spec{Name: fmt.Sprintf("test-nvlink_error-%d", s), Split: "test", Fault: "nvlink_error", Variant: "A", Seed: int64(2100 + s)})
		}
	default:
		return nil, fmt.Errorf("unknown split %q", split)
	}
	return out, nil
}
