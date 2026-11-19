package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const chartSeconds = 300 // the admin charts cover the last 5 minutes

// Sample is one second of server load.
type Sample struct {
	T          int64    `json:"t"`       // unix ms
	TickAvg    float64  `json:"tickAvg"` // ms, over the last 10 s
	TickP99    float64  `json:"tickP99"` // ms, over the last 10 s
	CPU        *float64 `json:"cpu"`     // % of one core; null where /proc is unavailable
	RAM        float64  `json:"ram"`     // MB obtained from the OS
	Goroutines int      `json:"goroutines"`
	Conns      int      `json:"conns"`
	MsgsIn     float64  `json:"msgsIn"`  // per second
	MsgsOut    float64  `json:"msgsOut"` // per second
	KBOut      float64  `json:"kbOut"`   // per second
	Entities   int      `json:"entities"`
}

type Stats struct {
	mu      sync.Mutex
	samples []Sample
}

// Run takes a sample every second, forever.
func (s *Stats) Run(g *Game) {
	var prevIn, prevOut, prevBytes int64
	prevCPU, hasCPU := cpuSeconds()
	prev := time.Now()
	for now := range time.Tick(time.Second) {
		dt := now.Sub(prev).Seconds()
		prev = now

		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		in, out, bytes := msgsIn.Load(), msgsOut.Load(), bytesOut.Load()
		v := Sample{
			T:          now.UnixMilli(),
			RAM:        round1(float64(mem.Sys) / (1 << 20)),
			Goroutines: runtime.NumGoroutine(),
			MsgsIn:     round1(float64(in-prevIn) / dt),
			MsgsOut:    round1(float64(out-prevOut) / dt),
			KBOut:      round1(float64(bytes-prevBytes) / 1024 / dt),
		}
		prevIn, prevOut, prevBytes = in, out, bytes
		v.TickAvg, v.TickP99, v.Entities, v.Conns = g.Load()
		if hasCPU {
			cpu, _ := cpuSeconds()
			percent := round1((cpu - prevCPU) / dt * 100)
			v.CPU, prevCPU = &percent, cpu
		}

		s.mu.Lock()
		s.samples = append(s.samples, v)
		if len(s.samples) > chartSeconds {
			s.samples = s.samples[1:]
		}
		s.mu.Unlock()
	}
}

func (s *Stats) History() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sample(nil), s.samples...)
}

func (s *Stats) Last() Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return Sample{T: time.Now().UnixMilli()}
	}
	return s.samples[len(s.samples)-1]
}

// cpuSeconds reads the CPU time used by this process from /proc/self/stat
// (Linux only): utime and stime are fields 14 and 15, in 1/100 s ticks.
func cpuSeconds() (float64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// Field 2 (the command name) may contain spaces, so count from its closing bracket.
	stat := string(data)
	fields := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return (utime + stime) / 100, true
}
