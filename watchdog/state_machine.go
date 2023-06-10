package watchdog

import (
	"context"

	"github.com/looplab/fsm"
	"go.uber.org/atomic"
)

// Known states.
const (
	// stateStopped means the watchdog is stopped and all related operations
	// are terminated. Moving from this state to a different one, means all
	// said operations are started from scratch.
	stateStopped string = "STATE_STOPPED"

	// stateStarted means the watchdog is started and working to determine
	// whether the underlying node is operating as expected or not.
	stateStarted string = "STATE_STARTED"

	// stateTracking means the local node is healthy and tracking the cluster.
	stateTracking string = "STATE_TRACKING"
)

// Known events that can result in state transitions.
// Some events may be raised multiple successive times, e.g. eventLocalNodeHealthy.
const (
	// eventSystemdFailure is raised when the watchdog is unable to interact
	// with systemd.
	eventSystemdFailure string = "EVENT_SYSTEMD_FAILURE"

	// eventUnreachable is raised when either of the local node endpoints
	// (RPC or PubSub) is unreachable.
	// NOTE(pires) if any of the endpoints is not working, it's safe to
	// assume the node is misbehaving and may, eventually, grind to an halt.
	eventUnreachable string = "EVENT_UNREACHABLE"

	// eventUnhealthy is raised when the local node is unhealthy and seemingly
	// can't self-repair, eg catch up.
	eventUnhealthy string = "EVENT_UNHEALTHY"

	// eventCatchingUp is raised when the local node is unhealthy but seemingly
	// catching-up.
	eventCatchingUp string = "EVENT_CATCHING_UP"

	// eventHealthy is raised when the local node is healthy, meaning it is
	// caught up with the cluster.
	eventHealthy string = "EVENT_HEALTHY"

	// eventNotEnoughRemotesBeingTracked is raised when most trusted nodes
	// can't be tracked.
	eventNotEnoughRemotesBeingTracked string = "EVENT_NOT_ENOUGH_REMOTES_BEING_TRACKED"

	// eventRestart is raised when the decision has been made to
	// restart the underlying node.
	eventRestart string = "EVENT_RESTART"

	// eventStart is raised when either:
	// - the watchdog is starting for the first time, or
	// - after the watchdog has been stopped.
	eventStart string = "EVENT_START"
)

// initializeStateMachine sets-up the state machine transitions.
func (w *watchdog) initializeStateMachine() {
	w.smTimeLastTransition = &atomic.Time{}
	w.sm = fsm.NewFSM(
		stateStopped,
		fsm.Events{
			{
				Name: eventSystemdFailure,
				Src:  []string{stateStarted, stateTracking},
				Dst:  stateStopped,
			},
			{
				Name: eventUnreachable,
				Src:  []string{stateStarted, stateTracking},
				Dst:  stateStopped,
			},
			{
				Name: eventUnhealthy,
				Src:  []string{stateStarted, stateTracking},
				Dst:  stateStopped,
			},
			{
				Name: eventNotEnoughRemotesBeingTracked,
				Src:  []string{stateTracking},
				Dst:  stateStopped,
			},
			{
				Name: eventRestart,
				Src:  []string{stateStopped},
				Dst:  stateStarted,
			},
			{
				Name: eventStart,
				Src:  []string{stateStopped},
				Dst:  stateStarted,
			},
			{
				Name: eventHealthy,
				Src:  []string{stateStarted, stateTracking},
				Dst:  stateTracking,
			},
		},
		fsm.Callbacks{
			"before_event": func(ctx context.Context, ev *fsm.Event) {
				w.beforeEvent(ctx, ev)
			},
			"enter_state": func(ctx context.Context, ev *fsm.Event) {
				w.enterState(ctx, ev)
			},
		},
	)
}
