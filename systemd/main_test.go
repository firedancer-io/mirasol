package systemd

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("github.com/coreos/go-systemd/v22/dbus.(*Conn).SubscribeUnitsCustom.func1"))
}
