package protocol

import "time"

const Version = 1

type Snapshot struct {
	Version       int        `json:"version"`
	Timestamp     time.Time  `json:"timestamp"`
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Cores         int        `json:"cores"`
	UptimeSeconds float64    `json:"uptime_seconds"`
	CPUPercent    float64    `json:"cpu_percent"`
	Memory        Memory     `json:"memory"`
	Swap          Memory     `json:"swap"`
	Disk          Disk       `json:"disk"`
	Load          [3]float64 `json:"load"`
	Network       Network    `json:"network"`
}

type Memory struct {
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
}

type Disk struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
}

type Network struct {
	ReceiveBytes           uint64  `json:"receive_bytes"`
	TransmitBytes          uint64  `json:"transmit_bytes"`
	ReceiveBytesPerSecond  float64 `json:"receive_bytes_per_second"`
	TransmitBytesPerSecond float64 `json:"transmit_bytes_per_second"`
}

func Percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}
