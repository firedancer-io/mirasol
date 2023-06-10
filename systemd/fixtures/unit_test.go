//go:build linux

package fixtures

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const unitDummy = "dummy"

func Test_E2E_InstallUnit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	type args struct {
		ctx  context.Context
		unit string
	}
	tests := []struct {
		name   string
		args   args
		failed bool
	}{
		{
			name: "successfully install unit",
			args: args{
				ctx:  ctx,
				unit: unitDummy,
			},
			failed: false,
		},
		{
			name: "fails to install non-existing unit",
			args: args{
				ctx:  ctx,
				unit: "nonexisting",
			},
			failed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.failed {
				require.Error(t, InstallUnit(tt.args.ctx, tt.args.unit))
			} else {
				require.NoError(t, InstallUnit(tt.args.ctx, tt.args.unit))
			}
		})
	}
}

func Test_E2E_UninstallUnit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	type args struct {
		ctx     context.Context
		unit    string
		install bool
	}
	tests := []struct {
		name   string
		args   args
		failed bool
	}{
		{
			name: "successfully uninstall unit",
			args: args{
				ctx:     ctx,
				unit:    unitDummy,
				install: true, // required for uninstalling
			},
			failed: false,
		},
		{
			name: "fails to uninstall non-existing unit",
			args: args{
				ctx:     ctx,
				unit:    "nonexisting",
				install: false, // required for failing uninstall
			},
			failed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// If installation is required, make sure it is successful.
			if tt.args.install {
				require.NoError(t, InstallUnit(tt.args.ctx, tt.args.unit))
			}
			if tt.failed {
				require.Error(t, UninstallUnit(tt.args.ctx, tt.args.unit))
			} else {
				require.NoError(t, UninstallUnit(tt.args.ctx, tt.args.unit))
			}
		})
	}
}
