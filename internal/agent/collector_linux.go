//go:build linux

package agent

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"basydra/internal/protocol"
)

type Collector struct {
	previousTotal, previousIdle uint64
	previousNetwork             map[string][2]uint64
	previousTime                time.Time
}

func readCPU(data string) (total, idle uint64, err error) {
	fields := strings.Fields(strings.SplitN(data, "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid /proc/stat")
	}
	// guest/guest_nice are already included in user/nice.
	for i := 1; i < len(fields) && i <= 8; i++ {
		v, e := strconv.ParseUint(fields[i], 10, 64)
		if e != nil {
			return 0, 0, e
		}
		total += v
		if i == 4 || i == 5 {
			idle += v
		}
	}
	return
}

func readMemory(data string) (protocol.Memory, protocol.Memory, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return protocol.Memory{}, protocol.Memory{}, err
		}
		values[strings.TrimSuffix(f[0], ":")] = v * 1024
	}
	total := values["MemTotal"]
	available, ok := values["MemAvailable"]
	if total == 0 || !ok {
		return protocol.Memory{}, protocol.Memory{}, fmt.Errorf("missing memory totals")
	}
	if available > total {
		available = total
	}
	swapTotal, swapFree := values["SwapTotal"], values["SwapFree"]
	if swapFree > swapTotal {
		swapFree = swapTotal
	}
	return protocol.Memory{Total: total, Used: total - available, Percent: protocol.Percent(total-available, total)}, protocol.Memory{Total: swapTotal, Used: swapTotal - swapFree, Percent: protocol.Percent(swapTotal-swapFree, swapTotal)}, nil
}

func readNetwork(data string) (map[string][2]uint64, error) {
	result := map[string][2]uint64{}
	for _, line := range strings.Split(data, "\n") {
		name, counters, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		// Count host links only; container bridge/veth counters would double-count traffic.
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			continue
		}
		f := strings.Fields(counters)
		if len(f) < 16 {
			return nil, fmt.Errorf("invalid counters for %s", name)
		}
		rx, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return nil, err
		}
		tx, err := strconv.ParseUint(f[8], 10, 64)
		if err != nil {
			return nil, err
		}
		result[name] = [2]uint64{rx, tx}
	}
	return result, nil
}

func (c *Collector) Collect() (protocol.Snapshot, error) {
	now := time.Now().UTC()
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return protocol.Snapshot{}, err
	}
	total, idle, err := readCPU(string(stat))
	if err != nil {
		return protocol.Snapshot{}, err
	}
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return protocol.Snapshot{}, err
	}
	memory, swap, err := readMemory(string(mem))
	if err != nil {
		return protocol.Snapshot{}, err
	}
	net, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return protocol.Snapshot{}, err
	}
	interfaces, err := readNetwork(string(net))
	if err != nil {
		return protocol.Snapshot{}, err
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return protocol.Snapshot{}, err
	}
	upFields := strings.Fields(string(uptime))
	if len(upFields) == 0 {
		return protocol.Snapshot{}, fmt.Errorf("invalid uptime")
	}
	up, err := strconv.ParseFloat(upFields[0], 64)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	loadData, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return protocol.Snapshot{}, err
	}
	loadFields := strings.Fields(string(loadData))
	if len(loadFields) < 3 {
		return protocol.Snapshot{}, fmt.Errorf("invalid load")
	}
	var load [3]float64
	for i := range load {
		load[i], err = strconv.ParseFloat(loadFields[i], 64)
		if err != nil {
			return protocol.Snapshot{}, err
		}
	}
	var fs syscall.Statfs_t
	if err = syscall.Statfs("/", &fs); err != nil {
		return protocol.Snapshot{}, err
	}
	diskTotal := fs.Blocks * uint64(fs.Bsize)
	diskUsed := (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
	host, err := os.Hostname()
	if err != nil {
		return protocol.Snapshot{}, err
	}
	s := protocol.Snapshot{Version: protocol.Version, Timestamp: now, Hostname: host, OS: "linux", Cores: runtime.NumCPU(), UptimeSeconds: up, Memory: memory, Swap: swap, Load: load, Disk: protocol.Disk{Path: "/", Total: diskTotal, Used: diskUsed, Percent: protocol.Percent(diskUsed, diskTotal)}}
	if !c.previousTime.IsZero() && total > c.previousTotal && idle >= c.previousIdle {
		delta, idleDelta := total-c.previousTotal, idle-c.previousIdle
		if idleDelta <= delta {
			s.CPUPercent = float64(delta-idleDelta) / float64(delta) * 100
		}
	}
	seconds := now.Sub(c.previousTime).Seconds()
	for name, current := range interfaces {
		s.Network.ReceiveBytes += current[0]
		s.Network.TransmitBytes += current[1]
		if prev, ok := c.previousNetwork[name]; ok && seconds > 0 {
			if current[0] >= prev[0] {
				s.Network.ReceiveBytesPerSecond += float64(current[0]-prev[0]) / seconds
			}
			if current[1] >= prev[1] {
				s.Network.TransmitBytesPerSecond += float64(current[1]-prev[1]) / seconds
			}
		}
	}
	c.previousTotal, c.previousIdle, c.previousNetwork, c.previousTime = total, idle, interfaces, now
	return s, nil
}
