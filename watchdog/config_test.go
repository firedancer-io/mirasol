package watchdog

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	testAddressesTrustedPubSub []netip.AddrPort = func() []netip.AddrPort {
		const max int = 10
		addresses := make([]netip.AddrPort, max)
		for i := 0; i < max; i++ {
			addressFormat := "127.0.0.1:%d"
			addresses[i] = netip.MustParseAddrPort(fmt.Sprintf(addressFormat, 7000+i))
		}

		return addresses
	}()
)

func Test_Unit_WatchdogBuilder(t *testing.T) {
	setNodeHealth := func(isHealthy bool) {}

	tests := []struct {
		name string
		have *Builder
		want Builder
	}{
		{
			name: "ensure empty values do not overwrite configuration",
			have: NewBuilder().
				WithLocalRPC(netip.AddrPort{}).
				WithLocalPubSub(netip.AddrPort{}).
				WithSystemdUnitName("").
				WithTimeoutRpcAvailable(0).
				WithTrustedPubSubs(nil),
			want: Builder{
				config: &config{
					addressLocalRPC:        DefaultLocalAddressRPC,
					addressLocalPubSub:     DefaultLocalAddressPubSub,
					addressesTrustedPubSub: []netip.AddrPort{},
					dryRyn:                 false,
					setNodeHealth:          nil,
					timeoutRpcAvailable:    DefaultTimeoutRpcAvailable,
					unitName:               "",
				},
			},
		},
		{
			name: "ensure all configuration is set properly",
			have: NewBuilder().
				WithDryRun(true).
				WithLocalRPC(DefaultLocalAddressRPC).
				WithLocalPubSub(DefaultLocalAddressPubSub).
				WithSetNodeHealthFunc(&setNodeHealth).
				WithSystemdUnitName("solana.service").
				WithTimeoutRpcAvailable(time.Microsecond * 5).
				WithTrustedPubSubs(testAddressesTrustedPubSub),
			want: Builder{
				config: &config{
					addressLocalRPC:        DefaultLocalAddressRPC,
					addressLocalPubSub:     DefaultLocalAddressPubSub,
					addressesTrustedPubSub: testAddressesTrustedPubSub,
					dryRyn:                 true,
					setNodeHealth:          &setNodeHealth,
					unitName:               "solana.service",
					timeoutRpcAvailable:    time.Microsecond * 5,
				},
			},
		},
		{
			name: "ensure WithSystemdUnitName is case-sensitive and adds missing suffix",
			have: NewBuilder().
				WithLocalRPC(netip.AddrPort{}).
				WithLocalPubSub(netip.AddrPort{}).
				WithTrustedPubSubs(nil).
				WithSystemdUnitName("SoLaNa"),
			want: Builder{
				config: &config{
					addressLocalRPC:        DefaultLocalAddressRPC,
					addressLocalPubSub:     DefaultLocalAddressPubSub,
					addressesTrustedPubSub: []netip.AddrPort{},
					dryRyn:                 false,
					setNodeHealth:          nil,
					timeoutRpcAvailable:    DefaultTimeoutRpcAvailable,
					unitName:               "SoLaNa.service",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualValues(t, tt.have.config, tt.want.config)
		})
	}
}
