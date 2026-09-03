package trx

import (
	"testing"

	"arb/internal/collect"
)

func TestCollectorInterface(t *testing.T) {
	var _ collect.Collector = (*AccountCollector)(nil)
	var _ collect.Collector = (*TxsCollector)(nil)
	var _ collect.Collector = (*WitnessCollector)(nil)
}
