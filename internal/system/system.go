// Package system exposes lightweight host and process information for the
// dashboard. Linux metrics are read from /proc; other platforms fall back to
// process level numbers.
package system

import (
	"runtime"
	"sync"
	"time"
)

// Info is a snapshot of system and process state.
type Info struct {
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	NumCPU      int     `json:"num_cpu"`
	Goroutines  int     `json:"goroutines"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsed     uint64  `json:"mem_used"`
	MemTotal    uint64  `json:"mem_total"`
	HeapAlloc   uint64  `json:"heap_alloc"`
	Uptime      int64   `json:"uptime_seconds"`
	MetricsHost bool    `json:"host_metrics"`
}

// Collector samples host metrics.
type Collector struct {
	mu        sync.Mutex
	startedAt time.Time
	lastTotal uint64
	lastIdle  uint64
	lastCPU   float64
	hasProc   bool
}

// NewCollector creates a collector.
func NewCollector() *Collector {
	c := &Collector{startedAt: time.Now(), hasProc: hasProcStat()}
	if c.hasProc {
		c.lastTotal, c.lastIdle, _ = readProcStat()
	}
	return c
}

// Uptime returns the process uptime.
func (c *Collector) Uptime() int64 {
	return int64(time.Since(c.startedAt).Seconds())
}

// Snapshot returns the current metrics.
func (c *Collector) Snapshot() Info {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	info := Info{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		NumCPU:      runtime.NumCPU(),
		Goroutines:  runtime.NumGoroutine(),
		HeapAlloc:   mem.HeapAlloc,
		Uptime:      c.Uptime(),
		MetricsHost: c.hasProc,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasProc {
		total, idle, err := readProcStat()
		if err == nil {
			deltaTotal := total - c.lastTotal
			deltaIdle := idle - c.lastIdle
			if deltaTotal > 0 {
				c.lastCPU = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
			}
			c.lastTotal, c.lastIdle = total, idle
		}
		used, totalMem, err := readMemInfo()
		if err == nil {
			info.MemUsed = used
			info.MemTotal = totalMem
		}
		info.CPUPercent = c.lastCPU
	} else {
		info.MemUsed = mem.HeapAlloc
	}
	return info
}

var errUnsupported = errNotSupported{}

type errNotSupported struct{}

func (errNotSupported) Error() string { return "unsupported platform" }
