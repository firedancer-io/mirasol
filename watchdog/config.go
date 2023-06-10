package watchdog

import (
	"net/netip"
	"strings"
	"time"
)

var (
	// DefaultLocalAddressRPC is localhost plus the default RPC port.
	DefaultLocalAddressRPC netip.AddrPort = netip.MustParseAddrPort("127.0.0.1:8899")

	// DefaultLocalAddressPubSub is localhost plus the default PubSub port.
	DefaultLocalAddressPubSub netip.AddrPort = netip.MustParseAddrPort("127.0.0.1:8900")

	// DefaultTimeoutRpcAvailable is a value determined by tribal knowledge.
	DefaultTimeoutRpcAvailable time.Duration = time.Minute * 20
)

// config carries watchdog configuration.
type config struct {
	addressLocalRPC        netip.AddrPort
	addressLocalPubSub     netip.AddrPort
	addressesTrustedPubSub []netip.AddrPort
	dryRyn                 bool
	setNodeHealth          *func(isHealthy bool)
	timeoutRpcAvailable    time.Duration
	unitName               string
}

// defaultConfig returns a config populated with defaults.
func defaultConfig() *config {
	return &config{
		addressLocalRPC:        DefaultLocalAddressRPC,
		addressLocalPubSub:     DefaultLocalAddressPubSub,
		addressesTrustedPubSub: []netip.AddrPort{},
		dryRyn:                 false,
		timeoutRpcAvailable:    DefaultTimeoutRpcAvailable,
	}
}

// Builder is a fluent constructor of watchdog.
type Builder struct {
	config *config
}

// NewBuilder returns a Builder populated with defaults.
func NewBuilder() *Builder {
	return &Builder{
		config: defaultConfig(),
	}
}

func (b *Builder) WithDryRun(dryRun bool) *Builder {
	b.config.dryRyn = dryRun

	return b
}

// WithLocalRPC sets the address for the underlying's node RPC endpoint.
func (b *Builder) WithLocalRPC(address netip.AddrPort) *Builder {
	if address != (netip.AddrPort{}) {
		b.config.addressLocalRPC = address
	}
	return b
}

// WithLocalPubSub sets the address for the underlying's node PubSub endpoint.
func (b *Builder) WithLocalPubSub(address netip.AddrPort) *Builder {
	if address != (netip.AddrPort{}) {
		b.config.addressLocalPubSub = address
	}

	return b
}

func (b *Builder) WithSetNodeHealthFunc(setNodeHealth *func(isHealthy bool)) *Builder {
	if setNodeHealth != nil {
		b.config.setNodeHealth = setNodeHealth
	}

	return b
}

// WithSystemdUnitName sets the systemd unit name that owns running the
// underlying node.
func (b *Builder) WithSystemdUnitName(unitName string) *Builder {
	if len(unitName) > 0 {
		// Ensure .service.
		if !strings.HasSuffix(unitName, ".service") {
			unitName = unitName + ".service"
		}
		b.config.unitName = unitName
	}

	return b
}

func (b *Builder) WithTimeoutRpcAvailable(timeoutRpcAvailable time.Duration) *Builder {
	if timeoutRpcAvailable > 0 {
		b.config.timeoutRpcAvailable = timeoutRpcAvailable
	}

	return b
}

// WithTrustedPubSubs sets the addresses for the remote PubSub endpoints.
func (b *Builder) WithTrustedPubSubs(addresses []netip.AddrPort) *Builder {
	if len(addresses) > 0 {
		b.config.addressesTrustedPubSub = addresses
	}

	return b
}

// Build returns a fully rendered Watchdog.
func (b *Builder) Build() Watchdog {
	return &watchdog{
		config:            b.config,
		dryRun:            b.config.dryRyn,
		mapTrackedNodes:   make(map[string]netip.AddrPort),
		board:             newSlotBoard(len(b.config.addressesTrustedPubSub)),
		setNodeHealthFunc: *b.config.setNodeHealth,
	}
}
