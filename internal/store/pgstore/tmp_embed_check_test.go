package pgstore

import (
	"os"
	"testing"

	"arb/migrations"
)

func TestTmpEmbedCheck(t *testing.T) {
	os.WriteFile("/tmp/embed116.txt", []byte(migrations.XassetComboNotionalSQL), 0o644)
}
