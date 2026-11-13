package agent

// The `activity` operation: what a sandbox is doing right now (RFC D9).
//
// An idle reaper has to answer one question before it takes a sandbox's CPU and
// memory away: is this instance waiting on something, or is it finished? Its CPU
// use cannot answer it — a sandbox blocked on a remote API costs almost no CPU
// while being the most important thing on the node not to interrupt — so the
// guest has to say what it is holding open. Only the agent knows that: it is the
// component that started the workload and the one that can see which of its own
// operations have not finished.
//
// The tracker counts operations the agent is serving, which is what "busy" means
// here, and it deliberately does not count the activity request itself: asking a
// sandbox what it is doing must not make it look busy, or the question would
// never be answerable twice.
//
// Time is measured with the monotonic clock. A wall clock is what a restore
// leaves wrong, and the point of this operation is a scheduler that keeps working
// across restores.

import (
	"os"
	"sort"
	"sync"
	"time"
)

// Activity is a guest's own report of what it is doing.
type Activity struct {
	// UptimeSeconds is how long this agent has been serving, on the monotonic
	// clock.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// IdleSeconds is how long it has been since an operation last finished. An
	// agent that has served nothing since it started is idle since it started.
	IdleSeconds float64 `json:"idle_seconds"`
	// LastOp names the last operation that finished, and is empty when none has.
	LastOp string `json:"last_op,omitempty"`
	// Inflight are the operations being served right now, most recent first,
	// with how long each has been running. The activity request is not among
	// them.
	Inflight []InflightOp `json:"inflight"`
	// Processes is how many processes the guest has, read from /proc. It counts
	// the agent itself and everything the workload started, and is the one number
	// here that says whether anything is running at all.
	Processes int `json:"processes"`
}

// InflightOp is one operation being served.
type InflightOp struct {
	// Op is the operation's name, as it is advertised in caps.
	Op string `json:"op"`
	// Seconds is how long it has been running.
	Seconds float64 `json:"seconds"`
}

// tracker records the operations one agent is serving. The agent serves every
// connection from its own goroutine, so a long operation on one connection is
// visible to an activity request on another — which is exactly the case this
// operation exists for.
type tracker struct {
	mu sync.Mutex
	// start is when this agent began serving, on the monotonic clock.
	start time.Time
	// inflight maps each running operation to its name and start, keyed by a
	// sequence number so two identical operations are two entries.
	inflight map[uint64]inflight
	seq      uint64
	// lastOp and lastEnd describe the last operation that finished. Activity
	// itself is not one: it observes the agent, it does not work.
	lastOp  string
	lastEnd time.Time
}

type inflight struct {
	op    string
	start time.Time
}

// newTracker returns a tracker whose uptime starts now.
func newTracker() *tracker {
	now := time.Now()
	return &tracker{start: now, lastEnd: now, inflight: make(map[uint64]inflight)}
}

// sharedTracker is the tracker every connection this agent serves reports
// through, so `activity` describes the sandbox and not one of its sessions.
var sharedTracker = newTracker()

// begin records that the operation op has started, and returns the function that
// records its end. The activity operation is not recorded: it is an observation
// of the agent, and counting it would make every question mark the sandbox busy.
//
// The returned function is safe to call more than once.
func (t *tracker) begin(op string) func() {
	if op == CapActivity {
		return func() {}
	}
	t.mu.Lock()
	t.seq++
	id := t.seq
	t.inflight[id] = inflight{op: op, start: time.Now()}
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.inflight, id)
			t.lastOp = op
			t.lastEnd = time.Now()
			t.mu.Unlock()
		})
	}
}

// snapshot is what the operation answers with: the operations in flight, the idle
// time, and the guest's process count from procDir (procRoot in the guest).
func (t *tracker) snapshot(procDir string) Activity {
	now := time.Now()

	t.mu.Lock()
	inflight := make([]InflightOp, 0, len(t.inflight))
	for _, op := range t.inflight {
		inflight = append(inflight, InflightOp{Op: op.op, Seconds: now.Sub(op.start).Seconds()})
	}
	lastOp, lastEnd := t.lastOp, t.lastEnd
	start := t.start
	t.mu.Unlock()

	// Most recent first, then by name: two operations of the same age are ordered
	// deterministically so two reads of an unchanged guest agree.
	sort.Slice(inflight, func(i, j int) bool {
		if inflight[i].Seconds != inflight[j].Seconds {
			return inflight[i].Seconds < inflight[j].Seconds
		}
		return inflight[i].Op < inflight[j].Op
	})

	return Activity{
		UptimeSeconds: now.Sub(start).Seconds(),
		IdleSeconds:   now.Sub(lastEnd).Seconds(),
		LastOp:        lastOp,
		Inflight:      inflight,
		Processes:     countProcesses(procDir),
	}
}

// countProcesses counts the processes the guest has, by counting the numeric
// entries of /proc. It reports 0 when the directory cannot be read: the count is
// one number in a report whose point is the operations in flight, and failing the
// whole answer over it would take the operation away exactly when a guest is
// already in trouble.
func countProcesses(procDir string) int {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if isNumericName(entry.Name()) {
			count++
		}
	}
	return count
}

// isNumericName reports whether name is a decimal process id.
func isNumericName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
