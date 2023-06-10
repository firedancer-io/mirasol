package watchdog

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gagliardetto/solana-go/rpc/ws"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

var (
	errNoSlotNotificationsWithinTimeWindow = errors.New("didn't receive slot data within the expected time window")
)

// tracker defines the behavior of tracking a node's slot height.
type tracker interface {
	Track(ctx context.Context, slotBoard slotBoard) error
}

// slotTracker tracks slot height for a certain node.
type slotTracker struct {
	addressPubSub netip.AddrPort
	nodeID        string
}

// Assess slotTracker fulfills the tracker interface.
var _ tracker = (*slotTracker)(nil)

// newTracker returns a new tracker with the passed RPC and PubSub addresses.
func newTracker(nodeID string, addressPubSub netip.AddrPort) tracker {
	return &slotTracker{
		addressPubSub: addressPubSub,
		nodeID:        nodeID,
	}
}

// Track keeps track of the slot height for a given node.
// When an update is received, the information is written down in the slot
// board.
func (t *slotTracker) Track(parentCtx context.Context, slotBoard slotBoard) error {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Track")
	defer span.End()

	urlPubSub := "ws://" + t.addressPubSub.String()

	logger := log.Ctx(parentCtx).With().Str("remote", urlPubSub).Logger()

	// Let's give some time for the connection to actually happen.
	// But we don't want to wait forever.
	timeout, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	logger.Debug().Msg("(re)connecting")
	clientPubSub, err := ws.Connect(timeout, urlPubSub)
	if err != nil {
		err = fmt.Errorf("can't connect to PubSub on %s: %w", urlPubSub, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	defer clientPubSub.Close()

	logger.Debug().Msg("(re)subscribing to slot notifications")
	subscription, err := clientPubSub.SlotSubscribe()
	if err != nil {
		err = fmt.Errorf("can't subscribe to slot updates through PubSub on %s: %w", urlPubSub, err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	defer subscription.Unsubscribe()
	// Track until the context is cancelled or a recoverable error occurs,
	// such as not receiving data after a certain (short) amount of time.
	defer logger.Debug().Msg("disconnecting")

	// Actively wait for slot notifications.
	chanSlotNotifications := make(chan *ws.SlotResult, 1)
	go func(ctx context.Context, notifications chan<- *ws.SlotResult) {
		defer close(notifications)
		for {
			// Exit if context is closed.
			if ctx.Err() != nil {
				return
			}
			// Wait for data.
			data, _ := subscription.Recv()
			// Send data.
			notifications <- data
		}
	}(ctx, chanSlotNotifications)

	// If no data is received within a certain amount of time, error out.
	heartbeatInterval := time.Second * 2
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			// The span should show success as we're exiting because we were
			// told to, ie context cancelled.
			span.SetStatus(codes.Ok, ctx.Err().Error())
			// Return the proper context error wrapped as a permanent error.
			// This will tell backoff to stop retrying.
			return backoff.Permanent(ctx.Err())
		case <-heartbeat.C:
			span.RecordError(errNoSlotNotificationsWithinTimeWindow)
			span.SetStatus(codes.Error, errNoSlotNotificationsWithinTimeWindow.Error())
			return errNoSlotNotificationsWithinTimeWindow
		case notification := <-chanSlotNotifications:
			// We got data, reset heartbeat ticker.
			heartbeat.Reset(heartbeatInterval)
			// Update the slot board.
			slotBoard.Set(t.nodeID, notification.Slot)
		}
	}
}
