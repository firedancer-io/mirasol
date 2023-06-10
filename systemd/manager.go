package systemd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
)

var (
	// ErrDisconnected means D-Bus API client is disconnected.
	ErrDisconnected = errors.New("systemd D-Bus API client is disconnected")

	ErrFailedStart = errors.New("failed to start unit")
)

const done string = "done"

// Manager controls the lifecycle of a single systemd unit.
type Manager interface {
	Restart(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Uptime(ctx context.Context, unit string) (time.Duration, error)
	Watch(ctx context.Context, unit string, updatesChan chan<- *dbus.UnitStatus) error
}

// manager manages units via a D-Bus connection to systemd.
type manager struct {
	dbusConn *dbus.Conn
	mutex    sync.RWMutex
}

// Assert manager fulfills the Manager interface.
var _ Manager = (*manager)(nil)

// New returns an initialized D-Bus unit manager.
// TODO repair connection on failure.
func New(ctx context.Context) (Manager, error) {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(ctx, "New")
	defer span.End()

	// Connect to dbusConn D-Bus API.
	dbusConn, err := dbus.NewWithContext(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "failed setting up systemd manager")

		return nil, err
	}

	// Ensure the systemd D-Bus API client disconnects when done.
	go func(conn *dbus.Conn) {
		<-ctx.Done()
		conn.Close()
	}(dbusConn)

	mgr := manager{
		dbusConn: dbusConn,
		mutex:    sync.RWMutex{},
	}

	return &mgr, nil
}

// serviceProperty returns the property of the named unit.
func (m *manager) serviceProperty(ctx context.Context, unit string, property string) (string, error) {
	// Ensure connection to D-Bus API.
	if !m.dbusConn.Connected() {
		return "", ErrDisconnected
	}

	p, err := m.dbusConn.GetServicePropertyContext(ctx, unit, property)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", nil
	}
	// these value string encode the type with @<Char><Space>, if so remove it before returning
	vs := p.Value.String()
	if vs[0] == '@' {
		return vs[3:], nil
	}

	return vs, nil
}

// Restart synchronously reloads and restarts the named unit.
func (m *manager) Restart(parentCtx context.Context, unit string) error {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Restart")
	span.SetAttributes(otelattr.String("unit", unit))
	defer span.End()

	// Ensure connection to D-Bus API.
	if !m.dbusConn.Connected() {
		span.RecordError(ErrDisconnected)
		span.SetStatus(otelcodes.Error, "failed to restart unit %q, can't reach systemd D-Bus API")

		return ErrDisconnected
	}

	// An error is expected when reload a unit that is not started, so ignore
	// any error.
	_, _ = m.dbusConn.ReloadUnitContext(ctx, unit, "replace", nil)

	// Restart the unit.
	resultChan := make(chan string, 1)
	_, err := m.dbusConn.RestartUnitContext(ctx, unit, "replace", resultChan)
	if err != nil {
		err := fmt.Errorf("failed to restart unit %q: %w", unit, err)
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())

		return err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		span.SetStatus(otelcodes.Error, err.Error())
		return ctx.Err()
	case result := <-resultChan:
		if result != done {
			err := fmt.Errorf("failed to restart unit %q with result %q", unit, result)
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())

			return err
		}
	}
	span.SetStatus(otelcodes.Ok, fmt.Sprintf("successfully restarted unit %q", unit))

	return err
}

// Start synchronously starts a named unit.
func (m *manager) Start(parentCtx context.Context, unit string) error {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Start")
	span.SetAttributes(otelattr.String("unit", unit))
	defer span.End()

	// Ensure connection to D-Bus API.
	if !m.dbusConn.Connected() {
		span.RecordError(ErrDisconnected)
		span.SetStatus(otelcodes.Error, "failed to start unit %q, can't reach systemd D-Bus API")

		return ErrDisconnected
	}

	resultChan := make(chan string, 1)
	_, err := m.dbusConn.StartUnitContext(ctx, unit, "replace", resultChan)
	if err != nil {
		err = fmt.Errorf("failed to start unit %q: %w", unit, err)
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())

		return err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		span.SetStatus(otelcodes.Error, ctx.Err().Error())

		return ctx.Err()
	case result := <-resultChan:
		if result != done {
			err := fmt.Errorf("failed to start unit %q with result %q", unit, result)
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())

			return err
		}
	}
	span.SetStatus(otelcodes.Ok, fmt.Sprintf("successfully started unit %q", unit))

	return nil
}

// Stop synchronously stops a named unit.
func (m *manager) Stop(parentCtx context.Context, unit string) error {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Stop")
	span.SetAttributes(otelattr.String("unit", unit))
	defer span.End()

	// Ensure connection to D-Bus API.
	if !m.dbusConn.Connected() {
		span.RecordError(ErrDisconnected)
		span.SetStatus(otelcodes.Error, "failed to stop unit %q, can't reach systemd D-Bus API")

		return ErrDisconnected
	}

	resultChan := make(chan string, 1)
	_, err := m.dbusConn.StopUnitContext(ctx, unit, "replace", resultChan)
	if err != nil {
		err = fmt.Errorf("failed to stop unit %q: %w", unit, err)
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())

		return err
	}

	select {
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		span.SetStatus(otelcodes.Error, ctx.Err().Error())

		return ctx.Err()
	case result := <-resultChan:
		if result != done {
			err := fmt.Errorf("failed to stop unit %q with result %q", unit, result)
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())

			return err
		}
	}
	span.SetStatus(otelcodes.Ok, fmt.Sprintf("successfully stopped unit %q", unit))

	return nil
}

// Uptime returns the duration since a unit started.
func (m *manager) Uptime(parentCtx context.Context, unit string) (time.Duration, error) {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Uptime")
	span.SetAttributes(otelattr.String("unit", unit))
	defer span.End()

	const attrStartTimestamp string = "ExecMainStartTimestamp"

	// There's an implicit check for connectivity to D-Bus API, so there's
	// no need to check here.
	propStartTime, err := m.serviceProperty(ctx, unit, attrStartTimestamp)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, fmt.Sprintf("failed to retrieve attribute %q for unit %q", attrStartTimestamp, unit))

		return -1, err
	}
	startTime, err := propertyTimestampToTime(propStartTime)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, fmt.Sprintf("failed to convert %q to time.Duration for unit %q uptime", propStartTime, unit))

		return -1, err
	}
	span.SetStatus(otelcodes.Ok, "retrieved unit uptime")

	return time.Now().UTC().Sub(startTime), nil
}

// Watch subscribes to a named unit status changes, which when found are sent
// to updatesChan. This is a blocking function.
func (m *manager) Watch(parentCtx context.Context, unit string, updatesChan chan<- *dbus.UnitStatus) error {
	// Set-up tracing context.
	ctx, span := otel.Tracer(name).Start(parentCtx, "Watch")
	span.SetAttributes(otelattr.String("unit", unit))
	defer span.End()

	// Ensure a non-nil channel is provided.
	if updatesChan == nil {
		err := fmt.Errorf("a chan is required for Watch to write unit status changes to")
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())

		return err
	}

	// Subscribe to status changes for the desired unit alone.
	subset := m.dbusConn.NewSubscriptionSet()
	subset.Add(unit)
	// TODO understand if such errors are critical and handle
	// them if it turns out to be the case.
	updateChan, _ := subset.Subscribe()

	for {
		select {
		case <-ctx.Done():
			// Set span status.
			span.RecordError(ctx.Err())
			span.SetStatus(otelcodes.Error, ctx.Err().Error())

			return ctx.Err()
		case changes := <-updateChan:
			// Filter changes by the desired unit.
			unitChanges, ok := changes[unit]
			if ok {
				updatesChan <- unitChanges
			}
		}
	}
}

func propertyTimestampToTime(s string) (time.Time, error) {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	// i is microseconds as per systemd D-Bus documentation:
	// "Note that properties exposing time values are usually encoded in
	// microseconds (usec) on the bus, even if their corresponding settings
	// in the unit files are in seconds."
	return time.Unix(i/1000000, i%1000000).UTC(), nil
}
