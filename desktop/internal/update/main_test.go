package update

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	quitDelay = 0
	os.Exit(m.Run())
}
