package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/jumpcrypto/mirasol/watchdog"
)

// Flag names.
const (
	flagAddressRPC                string = "local-rpc-address"
	flagAddressPubSub             string = "local-pubsub-address"
	flagAddressesForTrustedPubSub string = "trusted-pubsub-addresses"
	flagDryRun                    string = "dry-run"
	flagSystemdUnitName           string = "systemd-unit-name"
	flagTimeoutRpcAvailable       string = "timeout-for-rpc-available"
)

// Actual flag values we care about.
var (
	// addressRPC is the ip:port pair where the underlying Solana node is
	// serving the JSON-RPC HTTP endpoint.
	addressRPC string

	// addressPubSub is the ip:port pair where the underlying Solana node is
	// serving the PubSub WebSocket endpoint.
	addressPubSub string

	// addressesTrustedPubSub is a list of PubSub WebSocket endpoints to
	// subscribe for slot updates.
	addressesTrustedPubSub []string

	// dryRun is whether the watchdog will operate quietly, meaning it won't affect
	// the underlying node, ie restart.
	dryRun bool

	// systemdUnitName is the filename (without path) of the unit to manage.
	systemdUnitName string

	//timeoutRpcAvailable is the maximum amount of time to allow the underlying
	// node RPC endpoint to remain unavailable after it has started, ie uptime.
	timeoutRpcAvailable time.Duration

	// version is to be replaced at build-time.
	version = "unknown"
)

var watchdogBuilder *watchdog.Builder = watchdog.NewBuilder()

// root cmd is the default command.
var rootCmd = &cobra.Command{
	Use:     "mirasol",
	Short:   "mirasol - a watchdog for Solana nodes",
	Version: version,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Validate persistent flags.

		// Ensure dry-run mode.
		watchdogBuilder = watchdogBuilder.WithDryRun(dryRun)

		// Ensure addresses are valid.
		parsed, err := netip.ParseAddrPort(addressRPC)
		if err != nil {
			return fmt.Errorf("flag %q value %q is invalid: %w", flagAddressRPC, addressRPC, err)
		}
		watchdogBuilder = watchdogBuilder.WithLocalRPC(parsed)
		parsed, err = netip.ParseAddrPort(addressPubSub)
		if err != nil {
			return fmt.Errorf("flag %q value %q is invalid: %w", flagAddressPubSub, addressPubSub, err)
		}
		watchdogBuilder = watchdogBuilder.WithLocalPubSub(parsed)
		if len(addressesTrustedPubSub) < 1 {
			return fmt.Errorf("%q flag didn't specify any values", flagAddressesForTrustedPubSub)
		}
		addresses := make([]netip.AddrPort, len(addressesTrustedPubSub))
		for pos, addressTrustedPubSub := range addressesTrustedPubSub {
			parsed, err := netip.ParseAddrPort(addressTrustedPubSub)
			if err != nil {
				return fmt.Errorf("%q flag specifies an invalid PubSub address %q: %w", flagAddressesForTrustedPubSub, addressTrustedPubSub, err)
			}
			addresses[pos] = parsed
		}
		watchdogBuilder = watchdogBuilder.WithTrustedPubSubs(addresses)

		// Ensure systemd unit name is set.
		// First remove any leading or trailing whitespaces.
		systemdUnitName = strings.TrimSpace(systemdUnitName)
		// Check for empty string.
		if len(systemdUnitName) == 0 {
			return fmt.Errorf("flag %q specifies an empty value", flagSystemdUnitName)
		}
		// Then check for service suffix.
		if !strings.HasSuffix(strings.ToLower(systemdUnitName), ".service") {
			return fmt.Errorf("flag %q specifies an invalid value %q, MUST be suffixed with %q", flagSystemdUnitName, systemdUnitName, ".service")
		}
		watchdogBuilder.WithSystemdUnitName(systemdUnitName)

		// Ensure timeout before RPC is available is set.
		watchdogBuilder.WithTimeoutRpcAvailable(timeoutRpcAvailable)

		// TODO(pires) set-up healthz endpoint and replace dummy below.
		dummy := func(isHealthy bool) {}
		watchdogBuilder.WithSetNodeHealthFunc(&dummy)

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// trap Ctrl+C and cancel the context.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		// Set-up logger.
		// TODO(pires) make logger configurable, eg log level and output.
		//
		// Output to stderr, UTC time.
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
		// And with source file name and line.
		zerolog.CallerMarshalFunc = func(pc uintptr, file string, line int) string {
			short := file
			for i := len(file) - 1; i > 0; i-- {
				if file[i] == '/' {
					short = file[i+1:]
					break
				}
			}
			file = short
			return file + ":" + strconv.Itoa(line)
		}

		// Inject logger into context.
		ctx = log.Logger.With().Caller().Logger().WithContext(ctx)
		// And get it back for use here.
		logger := log.Ctx(ctx)
		// Start logging.
		logger.Info().Msg("starting mirasol")
		defer logger.Info().Msg("stopped mirasol")

		// Set-up telemetry.
		// TODO(pires) make telemetry configurable, eg where to write to.
		//
		// Write telemetry data to a file.
		fileTraces, err := os.Create("traces.txt")
		if err != nil {
			logger.Fatal().Err(err).Send()
		}
		defer fileTraces.Close()
		spanExporter, err := newConsoleExporter(fileTraces)
		if err != nil {
			logger.Fatal().Err(err).Send()
		}
		tp := trace.NewTracerProvider(
			trace.WithBatcher(spanExporter),
			trace.WithResource(newResource()),
		)
		defer func() {
			if err := tp.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Fatal().Err(err).Send()
			}
		}()
		otel.SetTracerProvider(tp)

		// Start the watchdog.
		// Context being canceled is not an error but rather intended behavior,
		// ie Ctrl+C keys were pressed.
		if err := watchdogBuilder.Build().Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}

		return nil
	},
}

func main() {
	// Set root command flags.

	// Underlying Solana node addresses.
	rootCmd.PersistentFlags().StringVar(
		&addressRPC,
		flagAddressRPC,
		watchdog.DefaultLocalAddressRPC.String(),
		"ip:port pair where the underlying Solana node is serving the JSON-RPC HTTP endpoint")

	rootCmd.PersistentFlags().StringVar(
		&addressPubSub,
		flagAddressPubSub,
		watchdog.DefaultLocalAddressPubSub.String(),
		"ip:port pair where the underlying Solana node is serving the PubSub Websocket endpoint")

	// Trusted Solana nodes to track.
	rootCmd.PersistentFlags().StringSliceVar(
		&addressesTrustedPubSub,
		flagAddressesForTrustedPubSub,
		[]string{},
		"list of trusted Solana nodes to track")
	_ = rootCmd.MarkFlagRequired(flagAddressesForTrustedPubSub)

	// Dry-run?
	rootCmd.PersistentFlags().BoolVar(
		&dryRun,
		flagDryRun,
		false,
		"set flag to disable operations on the underlying Solana process, ie restart",
	)

	// systemd unit to track.
	rootCmd.PersistentFlags().StringVar(
		&systemdUnitName,
		flagSystemdUnitName, "",
		"the systemd unit name managing the solana-validator process",
	)
	_ = rootCmd.MarkFlagRequired(flagSystemdUnitName)

	// Maximum uptime allowed before RPC becomes available.
	rootCmd.PersistentFlags().DurationVar(
		&timeoutRpcAvailable,
		flagTimeoutRpcAvailable,
		watchdog.DefaultTimeoutRpcAvailable,
		"the maximum uptime allowed before RPC becomes available",
	)

	// Execute.
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Send()
	}
}
