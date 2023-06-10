package watchdog

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	soljsonrpc "github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/jumpcrypto/mirasol/systemd"
	"github.com/looplab/fsm"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.uber.org/atomic"
)

// Watchdog specifies the watchdog behavior.
type Watchdog interface {
	Run(context.Context) error
}

// watchdog is the Watchdog implementation.
type watchdog struct {
	config *config

	// dryRun set to true means the watchdog will not take actions, such as
	// restarting the underlying node, just communicate to the user that such
	// action would take place.
	dryRun bool

	// mapTrackedNodes maps node identities to PubSub endpoints.
	mapTrackedNodes map[string]netip.AddrPort

	// board keeps the record of slot height for all tracked nodes.
	board slotBoard

	// unitManager interacts w/ systemd.
	unitManager systemd.Manager

	// sm is the state machine.
	sm *fsm.FSM

	// smTimeLastTransition is the time of the last recorded state transition.
	smTimeLastTransition *atomic.Time

	// ctxApp is the context.Context passed to Run(context.Context). We keep
	// a reference here so we can create child contexts in places where we
	// wouldn't have access to the root context, such as on state-machine
	// callbacks.
	ctxApp *context.Context

	// ctxRun is the context.Context used in the current watchdog run.
	ctxRun *context.Context

	// ctxCancel keeps the ability to cancel runCtx, the context.Context used
	// in the current watchdog run.
	// This is the only other way to stop the watchdog, other than terminating
	// the app, and comes to play when, for instance, the state-machine
	// transitions back to state STATE_STOPPED.
	ctxCancel context.CancelFunc

	// setNodeHealthFunc signals whether the underlying node is ready for service.
	setNodeHealthFunc func(isHealthy bool)
}

// Ensure watchdog fulfills the Watchdog interface.
var _ Watchdog = (*watchdog)(nil)

// wg is used to keep track of watchdog goroutines.
var wg sync.WaitGroup

// Run is a blocking function. It runs until context is cancelled.
// It takes a function that will signal the caller whether the underlying node
// is ready for service or not.
//
// TODO respect threshold (default to 25)
// TODO making the decision to restart should rely on a trend,
// ie numSlotsBehind grows over a finite amount of time, which boosts our
// confidence the node won't be able to catchup, and has to be restarted.
func (w *watchdog) Run(ctx context.Context) error {
	// Ensure setNodeHealthFunc is set.
	if w.setNodeHealthFunc == nil {
		return errors.New("setNodeHealthFunc is set to nil")
	}

	// Keep a reference to the context.
	w.ctxApp = &ctx

	// Set-up tracing context.
	_, span := otel.Tracer(name).Start(ctx, "Run")
	defer span.End()

	// Set-up process manager.
	unitManager, err := systemd.New(ctx)
	if err != nil {
		// Set span status.
		err = fmt.Errorf("failed to setup the systemd manager: %w", err)
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())

		return err
	}
	w.unitManager = unitManager

	// Initialize FSM.
	w.initializeStateMachine()

	// Trigger state machine to start the watchdog.
	_ = w.sm.Event(ctx, eventStart)

	// Wait for context to be cancelled.
	<-ctx.Done()
	// And for all goroutines to exit.
	wg.Wait()

	return ctx.Err()
}

// beforeEvent is called whenever there's a state-machine event.
//
// This provides us with a way to cancel triggering a state transition. For
// instance, it comes in handy when the underlying node is marked as
// unreachable while we're still under the allowed time-window for the node to
// come up, eg after a restart. In such case, the watchdog shouldn't stop, so
// we cancel the transition to the STATE_STOPPED and give the watchdog another
// chance to check on the node before the allowed time-window passes.
//
// This also provides us with a way to take event-based actions that do not
// result in state transitions, eg EVENT_CATCHING_UP starts trend analysis
// on how many slots the underlying node is behind over a certain amount of
// time, in order to infer whether the node is actually catching up or not.
// EVENT_HEALTHY would stop that trend analysis. Another instance of
// EVENT_CATCHING_UP starts the trend, and so on.
func (w *watchdog) beforeEvent(ctx context.Context, ev *fsm.Event) {
	logger := log.Ctx(ctx).With().Str("operation", "before-event").Logger()
	logger.Debug().Str("event", ev.Event).Send()

	// Context may have been cancelled.
	if errors.Is(ctx.Err(), context.Canceled) {
		logger.Warn().Msg("context has been cancelled, ignoring event")
		return
	}

	switch ev.Event {
	case eventUnhealthy:
		fallthrough
	case eventUnreachable:
		// Check uptime.
		uptime, err := w.unitManager.Uptime(ctx, w.config.unitName)
		if err != nil {
			err = fmt.Errorf("unable to determine unit %q uptime: %w", w.config.unitName, err)
			logger.Error().Err(err).Send()

			// Trigger state-machine event.
			_ = w.sm.Event(ctx, eventSystemdFailure)
		}

		// Do not change state if the underlying node uptime is less than the
		// acceptable threshold.
		if uptime < w.config.timeoutRpcAvailable {
			logger.Info().
				Str("uptime", uptime.String()).
				Str("timeout", w.config.timeoutRpcAvailable.String()).
				Msg("local node is unhealthy/unreachable but we're still within the allowed time window, retrying")
			ev.Cancel()
		}
	case eventRestart:
		// Respect configured dry-run mode.
		if w.dryRun {
			logger.Info().Msgf("decided to restart systemd unit %q but dry-run was set", w.config.unitName)
		} else {
			logger.Info().Msgf("restarting systemd unit %q", w.config.unitName)
			if err := w.unitManager.Restart(ctx, w.config.unitName); err != nil {
				logger.Err(err).Msgf("failed to restart systemd unit %q", w.config.unitName)
				// Do not cancel the state transition. This will give the watchdog
				// another chance to restart.
			}
		}
		logger.Info().Msg("retrying")
	}
	// default is to do nothing and just proceed with the transition.
}

// enterState is where state transitions are handled.
// It takes place *after* transitioning between the old and new state.
func (w *watchdog) enterState(ctx context.Context, ev *fsm.Event) {
	logger := log.Ctx(ctx).With().Str("operation", "enter-state").Logger()
	logger.Debug().Str("state", ev.Dst).Send()

	// Context may have been cancelled.
	if errors.Is(ctx.Err(), context.Canceled) {
		logger.Warn().Msg("context has been cancelled, ignoring state transition")
		return
	}

	// Update last state transition.
	w.smTimeLastTransition.Store(time.Now().UTC())

	// Compute state transition.
	switch ev.Dst {
	case stateStopped:
		// Set healthz to false.
		w.setNodeHealthFunc(false)

		// Stop the watchdog.
		w.ctxCancel()

		// Check if the event that triggered transition to this state is
		// recoverable.
		if ev.Event == eventNotEnoughRemotesBeingTracked ||
			ev.Event == eventSystemdFailure {
			// The events above are irrecoverable.
			logger.Error().Msgf("state is STOPPED due to the irrecoverable event %q. Mirasol is expected to exit", ev.Event)
			return
		}

		// Trigger a restart to the systemd unit and a new watchdog run.
		// ctx has just been cancelled so we have to rely on the watchdog
		// parent context to send this event and, subsequently trigger a state
		// transition.
		_ = w.sm.Event(*w.ctxApp, eventRestart)
	case stateStarted:
		// Set healthz to false.
		w.setNodeHealthFunc(false)

		// (Re)Start the watchdog.
		//
		// We know ctx is the watchdog parent context, which allow us to set-up
		// a new child context just for this new run, and respective cancel
		//function.
		runCtx, cancel := context.WithCancel(*w.ctxApp)
		w.ctxRun = &runCtx
		w.ctxCancel = cancel
		go w.checkLocalNode(*w.ctxRun)
	case stateTracking:
		// Set healthz to true.
		w.setNodeHealthFunc(true)

		// Start tracking local node.
		go w.trackLocalNode(*w.ctxRun)

		// Start tracking trusted nodes.
		go w.trackTrustedNodes(*w.ctxRun)
	}
}

// Solana protocol known keys and values.
const (
	// solanaErrNodeIsUnhealthy is a Solana error code that means the node is
	// unhealthy.
	solanaErrNodeIsUnhealthy int = -32005

	// solanaDataKeyNumSlotsBehind is a field name for Solana RPC error payload.
	solanaDataKeyNumSlotsBehind string = "numSlotsBehind"
)

// checkLocalNode checks for:
// - ability to interact with systemd;
// - RPC endpoint is reachable within a pre-configured amount of time;
// - When RPC endpoint is reachable, is node healthy or catchign up.
func (w *watchdog) checkLocalNode(ctx context.Context) {
	// Register to the wait group.
	wg.Add(1)
	// Let the wait group know when we're done.
	defer wg.Done()

	// Set-up logger.
	logger := log.Ctx(ctx).With().Str("operation", "check-local-node").Logger()
	logger.Info().Msg("checking local node")
	defer logger.Info().Msg("stopped checking local node")

	// Periodically, check if node is healthy.
	// This will repeat until context is cancelled.
	tick := time.NewTicker(time.Second * 10)
	defer tick.Stop()
	for {
		// Set-up tracing span.
		_, span := otel.Tracer(name).Start(ctx, "CheckLocalNode")

		select {
		case <-ctx.Done():
			logger.Debug().Msg("context cancelled")
			// Set span status.
			span.SetStatus(otelcodes.Ok, ctx.Err().Error())
			span.End()

			return
		case <-tick.C:
			// Check local RPC endpoint health.
			urlLocalRPC := "http://" + w.config.addressLocalRPC.String()
			// Give it a short amount of time to check the RPC endpoint.
			ctxTimeout, cancel := context.WithTimeout(ctx, time.Second*5)
			isHealthy, numSlotsBehind, err := getNodeHealth(ctxTimeout, urlLocalRPC)
			cancel()
			if isHealthy {
				// Set span status.
				span.SetStatus(otelcodes.Ok, "node is healthy")

				// Trigger state-machine event.
				_ = w.sm.Event(ctx, eventHealthy)

				break
			}
			// At this point, only handle error if it's errNodeIsBehind.
			if errors.Is(err, errNodeIsBehind) {
				logger.Warn().Int64("slots-behind", *numSlotsBehind).Msg("node is lagging behind the cluster")
				span.SetStatus(otelcodes.Ok, "node is lagging behind the cluster but that's fine for now")

				// Trigger state-machine event.
				_ = w.sm.Event(ctx, eventCatchingUp)

				break
			}

			message := "unknown error"
			logger.Err(err).Msg(message)

			// Set span status.
			// TODO logically, this can't be nil but we should protect
			// ourselves.
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, message)

			// Trigger state-machine event.
			// TODO differentiate between unhealthy and unreachable.
			_ = w.sm.Event(ctx, eventUnhealthy)
		}

		// Explcitly end the span before a new one is set-up during the next
		// iteration.
		span.End()
	}
}

// trackLocalNode tracks the slot height for the local node.
func (w *watchdog) trackLocalNode(ctx context.Context) {
	// Register to the trackers wait group.
	wg.Add(1)
	// Let the wait group know when we're done.
	defer wg.Done()

	// Set-up logger with operation and nodeID.
	logger := log.Ctx(ctx).With().Str("operation", "track-node").Str("nodeID", "local").Logger()
	logger.Info().Msg("tracking local node")
	defer logger.Info().Msg("stopped tracking local node")

	// Set-up tracing span.
	_, span := otel.Tracer(name).Start(ctx, "TrackLocalNode")
	defer span.End()

	attrNodeID := otelattr.String("nodeID", "local")
	span.SetAttributes([]otelattr.KeyValue{attrNodeID}...)

	// Track.
	localTracker := newTracker("local", w.config.addressLocalPubSub)
	if err := localTracker.Track(ctx, w.board); err != nil {
		err = fmt.Errorf("stopping tracking slot height for local node: %w", err)
		if errors.Is(err, context.Canceled) {
			span.SetStatus(otelcodes.Ok, err.Error())
		} else {
			logger.Err(err).Msg("unexpected error")
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())
		}
		// Send EVENT_UNREACHABLE.
		_ = w.sm.Event(ctx, eventUnreachable)
	}
}

// trackTrustedNodes tracks the slot height (at most) for the trusted nodes.
func (w *watchdog) trackTrustedNodes(ctx context.Context) {
	// Keep a count of how many trackers are active.
	tracking := atomic.NewUint32(0)
	// The minimum number of trackers to be active, n/2 +1.
	minimumTracking := uint32(len(w.config.addressesTrustedPubSub)/2 + 1)

	// Track each of the known trusted addresses.
	for _, trustedPubSubAddress := range w.config.addressesTrustedPubSub {
		// nodeID is used to identify the slot height for this node in the
		// slot board.
		nodeID := trustedPubSubAddress.String()

		// Register to the wait group.
		wg.Add(1)
		go func(nodeID string, trustedPubSubAddress netip.AddrPort) {
			// Let the wait group know we're done.
			defer wg.Done()

			// Set-up tracing span.
			_, span := otel.Tracer(name).Start(ctx, "TrackRemoteNode")
			defer span.End()

			attrNodeID := otelattr.String("nodeID", nodeID)
			span.SetAttributes([]otelattr.KeyValue{attrNodeID}...)

			// Create child logger with operation and nodeID.
			logger := log.Ctx(ctx).With().Str("operation", "track-node").Str("nodeID", nodeID).Logger()

			// Retry forever with exponential backoff.
			bo := backoff.NewExponentialBackOff()
			bo.MaxElapsedTime = 0
			// Or until context is cancelled.
			boctx := backoff.WithContext(bo, ctx)

			// Tracking should survive issues with the stream coming from the
			// underlying node, eg a network blip that can be eventually recovered
			// from. However, context cancellation means tracking should stop
			// entirely.
			// err is only returned if the error is backoff.PermanentError.
			remoteTracker := newTracker(nodeID, trustedPubSubAddress)
			err := backoff.RetryNotify(func() error {
				// Increase tracking counter.
				tracking.Inc()
				// Decrease tracking counter when done.
				defer tracking.Dec()

				return remoteTracker.Track(boctx.Context(), w.board)
			}, boctx, func(err error, d time.Duration) {
				// This notify func is only called if the error is not permanent.
				logger.Err(err).Time("approx-retry-time", time.Now().UTC().Add(d)).Msg("can't track remote node")
			})
			// A permanent error is expected only if the context is
			// cancelled.
			err = fmt.Errorf("permanently stopping tracking slot height for remote node: %w", err)
			if errors.Is(err, context.Canceled) {
				logger.Debug().Msg(err.Error())
				span.SetStatus(otelcodes.Ok, err.Error())
			} else {
				logger.Err(err).Msg("unexpected error")
				span.RecordError(err)
				span.SetStatus(otelcodes.Error, err.Error())
			}
		}(nodeID, trustedPubSubAddress)
	}
	// Ensure minimum number of active trackers.
	go func(ctx context.Context, minimumTracking uint32, tracking *atomic.Uint32) {
		logger := log.Ctx(ctx).With().Logger()
		// Don't be too strict and add more chances of success by checking on
		// an interval.
		tick := time.NewTicker(time.Second * 10)
		defer tick.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				activeTrackers := tracking.Load()
				if activeTrackers < minimumTracking {
					logger.Error().
						Uint32("active-trackers", activeTrackers).
						Uint32("minimum-trackers", minimumTracking).
						Msg("can't track most trusted nodes")
					_ = w.sm.Event(ctx, eventNotEnoughRemotesBeingTracked)
				}
			}
		}
	}(ctx, minimumTracking, tracking)
}

// unknownRPCError signals that the operation should not be retried.
type unknownRPCError struct {
	Err error
}

func (e *unknownRPCError) Error() string {
	description := ""
	if e.Err != nil {
		description = e.Err.Error()
	}

	return description
}

func (e *unknownRPCError) Unwrap() error {
	return e.Err
}

func (e *unknownRPCError) Is(target error) bool {
	_, ok := target.(*unknownRPCError)
	return ok
}

var errNodeIsUnhealthy error = errors.New("node is unhealthy")

// errNodeIsBehind signals the node is lagging behind the cluster.
var errNodeIsBehind error = errors.New("node is lagging behind cluster")

// getNodeHealth checks the underlying node health by calling the 'getHealth'
// RPC. The node is considered healthy only when this RPC returns healthy.
// When the node is considered unhealthy, an error is returned. Optionally,
// the number of slots is also returned, which only happens when the RPC is
// successful and the node reports to be lagging behind the cluster.
func getNodeHealth(ctx context.Context, urlLocalRPC string) (isHealthy bool, numSlotsBehind *int64, err error) {
	_, span := otel.Tracer(name).Start(ctx, "getNodeHealth")
	defer span.End()

	// Check local RPC endpoint health.
	rpcLocal := solrpc.New(urlLocalRPC)
	defer rpcLocal.Close()
	out, err := rpcLocal.GetHealth(ctx)
	if err != nil {
		// Decode the error.
		if rpcErr, ok := err.(*soljsonrpc.RPCError); ok {
			// If the node is just lagging behind, this is fine for now.
			if rpcErr.Code == solanaErrNodeIsUnhealthy {
				// TODO check rpcErr.Data type in Solana's code and
				// respect it? For now, we just need numSlotsBehind so
				// map[string]int works fine.
				//
				// NOTE same error code has different data structures:
				// - {"jsonrpc":"2.0","error":{"code":-32005,"message":"Node is unhealthy","data":{"numSlotsBehind":null}},"id":"x"}
				// - {"jsonrpc":"2.0","error":{"code":-32005,"message":"Node is behind by 3542 slots","data":{"numSlotsBehind":3542}},"id":"x"}
				if rpcErr.Data != nil {
					if data, ok := rpcErr.Data.(map[string]interface{}); ok {
						if v, exists := data[solanaDataKeyNumSlotsBehind]; exists {
							if rawNumSlotsBehind, ok := v.(stdjson.Number); ok {
								if numSlotsBehind, err := rawNumSlotsBehind.Int64(); err == nil {
									span.RecordError(errNodeIsBehind)
									span.SetStatus(otelcodes.Error, "node is lagging behind")
									return false, &numSlotsBehind, errNodeIsBehind
								}
							}
						}
					}
				}
				// Getting here should never happen, unless we somehow failed
				// to decode response data, eg Solana bug borks getHealth.
				return false, nil, errNodeIsUnhealthy
			}
			err = &unknownRPCError{Err: rpcErr}
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, "unable to determine node health, marking unhealthy")
			return false, nil, err
		}
		err = &unknownRPCError{Err: err}
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "unable to determine node health, marking unhealthy")
		return false, nil, err
	}
	if out == solrpc.HealthOk {
		span.SetStatus(otelcodes.Ok, "node is healthy")
		return true, nil, nil
	}

	// This should never happen so we return an unknown error and a crazy-high
	// number of slots behind.
	err = &unknownRPCError{}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, "unable to determine node health, marking unhealthy")
	return false, nil, err
}
