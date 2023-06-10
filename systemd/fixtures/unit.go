//go:build linux

package fixtures

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/coreos/go-systemd/v22/dbus"
)

func InstallUnit(ctx context.Context, unit string) error {
	// Find fixture.
	if !strings.HasSuffix(unit, ".service") {
		unit = unit + ".service"
	}
	fixtureAbsoluteFilepath, err := filepath.Abs(unit)
	if err != nil {
		return fmt.Errorf("failed to determine absolute path for unit %q: %w", unit, err)
	}
	if !strings.Contains(fixtureAbsoluteFilepath, "/fixtures/") {
		fixtureAbsoluteFilepath, err = filepath.Abs("fixtures/" + unit)
		if err != nil {
			return fmt.Errorf("failed to determine absolute path for unit %q: %w", unit, err)
		}
	}

	// Set-up systemd D-Bus API client.
	conn, err := dbus.NewWithContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to set-up systemd D-Bus connection: %w", err)
	}
	defer conn.Close()

	// Blindly kill the unit in case it is running.
	_ = UninstallUnit(ctx, unit)

	// Blindly remove the symlink in case it exists.
	runPath := filepath.Join("/run/systemd/system/", unit)
	_ = os.Remove(runPath)

	// Link unit.
	changes, err := conn.LinkUnitFilesContext(ctx, []string{fixtureAbsoluteFilepath}, true, true)
	if err != nil {
		return fmt.Errorf("failed to link %q: %w", fixtureAbsoluteFilepath, err)
	}
	if len(changes) < 1 {
		return fmt.Errorf("expected one change when linking %q, got %v", fixtureAbsoluteFilepath, changes)
	}
	if changes[0].Filename != runPath {
		return fmt.Errorf("expected %q, got %q", runPath, changes[0].Filename)
	}

	return nil
}

func UninstallUnit(ctx context.Context, unit string) error {
	// Figure out running unit path.
	if !strings.HasSuffix(unit, ".service") {
		unit = unit + ".service"
	}

	// Set-up systemd D-Bus API client.
	conn, err := dbus.NewWithContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to set-up systemd D-Bus connection: %w", err)
	}
	defer conn.Close()

	// Blindly stop the unit in case it is running.
	_, _ = conn.StopUnitContext(ctx, unit, "replace", nil)

	// Unink unit.
	changes, err := conn.DisableUnitFilesContext(ctx, []string{unit}, true)
	if err != nil {
		return fmt.Errorf("failed to disable unit %q: %w", unit, err)
	}
	if len(changes) < 1 {
		return fmt.Errorf("expected one change when unlinking %q, got %v", unit, changes)
	}
	runPath := filepath.Join("/run/systemd/system/", unit)
	if changes[0].Filename != runPath {
		return fmt.Errorf("expected %q, got %q", runPath, changes[0].Filename)
	}

	// Blindly remove the symlink in case it exists.
	_ = os.Remove(runPath)

	return nil
}
