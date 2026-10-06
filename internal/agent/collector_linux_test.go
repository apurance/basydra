//go:build linux

package agent

import "testing"

func TestCPUDoesNotDoubleCountGuest(t *testing.T) {
	total, idle, err := readCPU("cpu 10 20 30 40 5 6 7 8 100 200\ncpu0 0\n")
	if err != nil || total != 126 || idle != 45 {
		t.Fatalf("got %d/%d: %v", total, idle, err)
	}
}
func TestMemoryUsesAvailableAndClamps(t *testing.T) {
	m, s, err := readMemory("MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 100 kB\nSwapFree: 150 kB\n")
	if err != nil || m.Used != 600*1024 || m.Percent != 60 || s.Used != 0 {
		t.Fatalf("%+v %+v %v", m, s, err)
	}
	if _, _, err = readMemory("MemTotal: 10 kB"); err == nil {
		t.Fatal("missing available memory accepted")
	}
}
func TestNetworkSkipsContainerDuplicates(t *testing.T) {
	line := " 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n"
	result, err := readNetwork("eth0:" + line + "lo:" + line + "veth123:" + line + "docker0:" + line + "br-test:" + line)
	if err != nil || len(result) != 1 || result["eth0"] != [2]uint64{100, 200} {
		t.Fatalf("%+v %v", result, err)
	}
}
