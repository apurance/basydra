//go:build !linux

package agent

import (
	"basydra/internal/protocol"
	"fmt"
)

type Collector struct{}

func (*Collector) Collect() (protocol.Snapshot, error) {
	return protocol.Snapshot{}, fmt.Errorf("the Basydra probe requires Linux")
}
