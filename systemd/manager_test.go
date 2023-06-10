//go:build linux

package systemd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/jumpcrypto/mirasol/systemd/fixtures"
	"github.com/stretchr/testify/require"
)

// Fixtures
const unitDummy = "manager_dummy.service"

// uninstallUnit is a wrapper for uninstalling units. It is required for
// deferring unit removal while making the errcheck linter happy, since it
// isn't supported to defer AND use blank assignment on functions which return
// error, such as fixtures.UninstallUnit.
var uninstallUnit = func(t *testing.T, ctx context.Context, unit string) {
	if err := fixtures.UninstallUnit(ctx, unit); err != nil {
		err = fmt.Errorf("failed to uninstall unit %q: %w", unit, err)
		t.Logf(err.Error())
	}
}

func Test_E2E_Manager_Watch(t *testing.T) {
	// Tests the Watch function but also Start, Restart, and Stop, since
	// starting and stopping a unit are triggers to unit state transitions,
	// which are the things we want to watch for.
	t.Run("Watch unit started, restarted, and stopped", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		// Install fixture.
		require.NoError(t, fixtures.InstallUnit(ctx, unitDummy))
		// By the time of uninstall, ctx may be cancelled.
		defer uninstallUnit(t, context.Background(), unitDummy)

		// Set-up manager.
		mgr, err := New(ctx)
		require.NoError(t, err)

		// Watch for unit status changes.
		updatesChan := make(chan *dbus.UnitStatus)
		// This is a blocking call so it must be wrapped within a goroutine.
		go func(t *testing.T) {
			// Ensure Watch stops due to context being cancelled.
			require.ErrorIs(t, mgr.Watch(ctx, unitDummy, updatesChan), context.Canceled)
		}(t)

		// Trigger a start status change.
		require.NoError(t, mgr.Start(ctx, unitDummy))

		// Observe and validate the status change.
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case res := <-updatesChan:
			require.NotNil(t, res)
			require.Equal(t, unitDummy, res.Name, "unexpected status change from a unit we don't care about")
			require.Equal(t, "active", res.ActiveState)
		}

		// Trigger a stop status change.
		require.NoError(t, mgr.Stop(ctx, unitDummy))

		// Observe and validate the status change.
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case res := <-updatesChan:
			require.Nil(t, res, "result must be nil when stopping a unit")
		}

		// Trigger a restart status change.
		require.NoError(t, mgr.Restart(ctx, unitDummy))

		// Observe and validate the status change.
		// NOTE seemingly, restarts DO NOT yield status changes if unit
		// is started already.
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case res := <-updatesChan:
			require.NotNil(t, res)
			require.Equal(t, unitDummy, res.Name, "unexpected status change from a unit we don't care about")
			require.Equal(t, "active", res.ActiveState)
		}
	})

	t.Run("Watch unit that isn't installed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()

		// Set-up manager.
		mgr, err := New(ctx)
		require.NoError(t, err)

		// Watch for unit status changes.
		updatesChan := make(chan *dbus.UnitStatus)
		// Ensure Watch stops due to context being cancelled.
		require.ErrorIs(t, mgr.Watch(ctx, "non-existing", updatesChan), context.DeadlineExceeded)
	})

	t.Run("Watch unit that isn't started", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()

		// Install fixture.
		require.NoError(t, fixtures.InstallUnit(ctx, unitDummy))
		// By the time of uninstall, ctx may be cancelled.
		defer uninstallUnit(t, context.Background(), unitDummy)

		// Set-up manager.
		mgr, err := New(ctx)
		require.NoError(t, err)

		// Watch for unit status changes.
		updatesChan := make(chan *dbus.UnitStatus)
		// Ensure Watch stops due to context being cancelled.
		require.ErrorIs(t, mgr.Watch(ctx, unitDummy, updatesChan), context.DeadlineExceeded)
	})
}
