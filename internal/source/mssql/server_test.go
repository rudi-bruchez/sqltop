package mssql

import (
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// A local SQL Server 2022 only ever exercises the "granted, on-premises"
// branch of hasInstanceWideView. These check the gate rather than the SQL,
// which the integration tests already prove against the container.
func TestHasInstanceWideView(t *testing.T) {
	cases := []struct {
		name string
		info model.ServerInfo
		caps model.Capabilities
		want bool
	}{
		{"on-premises with the right", model.ServerInfo{MajorVersion: 16}, model.Caps(model.CapInstanceWideView), true},
		{"on-premises without the right", model.ServerInfo{MajorVersion: 16}, 0, false},
		{"managed instance without the right", model.ServerInfo{MajorVersion: 16, IsAzureMI: true}, 0, false},
		{
			// probe never tests HAS_PERMS_BY_NAME for a scoped single
			// database, so the capability is always absent here even though
			// the view answers for the current database under a different,
			// commonly-granted permission.
			"azure sql database, capability never probed", model.ServerInfo{MajorVersion: 12, IsAzureSQLDB: true}, 0, true,
		},
		{"the zero value has neither", model.ServerInfo{}, 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasInstanceWideView(c.info, c.caps); got != c.want {
				t.Errorf("hasInstanceWideView(%+v, %v) = %v, want %v", c.info, c.caps, got, c.want)
			}
		})
	}
}
