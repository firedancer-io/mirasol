package watchdog

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// TODO this fails github.com/gagliardetto/solana-go/rpc/ws.(*SlotSubscription).Recv.
	goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("github.com/gagliardetto/solana-go/rpc/ws.(*SlotSubscription).Recv"))
}
