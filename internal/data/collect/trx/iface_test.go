package trx

import (
	"testing"

	"nox/internal/data/collect"
)

func TestCollectorInterface(t *testing.T) {
	var _ collect.Collector = (*AccountCollector)(nil)
	var _ collect.Collector = (*TxsCollector)(nil)
	var _ collect.Collector = (*WitnessCollector)(nil)
}
