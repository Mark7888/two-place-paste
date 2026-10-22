package update

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	quitDelay = 0
	// Short enough that the stall tests end quickly, long enough that a
	// healthy local download never trips it.
	stallTimeout = time.Second
	os.Exit(m.Run())
}
