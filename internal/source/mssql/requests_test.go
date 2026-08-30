package mssql

import (
	"strings"
	"testing"

	"github.com/rudi-bruchez/sqltop/internal/model"
)

// The built query has four shapes and a local SQL Server 2022 only ever
// exercises one of them. These check the gates rather than the SQL, which is
// proven against the container by the integration tests.
func TestBuiltQueryGates(t *testing.T) {
	const dop = "r.dop"
	const tempdb = "dm_db_task_space_usage"
	tempdbCaps := model.Caps(model.CapTempdbPerTask)
	dopCaps := model.Caps(model.CapRequestDOP)

	cases := []struct {
		name      string
		info      model.ServerInfo
		caps      model.Capabilities
		wantDOP   bool
		wantTempd bool
	}{
		{"no dop column on this server", model.ServerInfo{MajorVersion: 12}, tempdbCaps, false, true},
		{"dop column present", model.ServerInfo{MajorVersion: 16}, tempdbCaps | dopCaps, true, true},
		{"no rights on the tempdb dmv", model.ServerInfo{MajorVersion: 16}, dopCaps, true, false},
		{"the zero value asks for nothing it cannot have", model.ServerInfo{}, 0, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := buildRequestsQuery(c.info, c.caps)
			if got := strings.Contains(q, dop); got != c.wantDOP {
				t.Errorf("dop column present = %v, want %v", got, c.wantDOP)
			}
			if got := strings.Contains(q, tempdb); got != c.wantTempd {
				t.Errorf("tempdb join present = %v, want %v", got, c.wantTempd)
			}
			if !strings.HasSuffix(strings.TrimSpace(q), "OPTION (RECOMPILE, MAXDOP 1)") {
				t.Error("every shape has to keep the hint, or a degraded server is the one that pollutes the plan cache")
			}
			if n := strings.Count(q, ","); n == 0 {
				t.Fatal("the query lost its column list")
			}
		})
	}
}

// The version matrix that decides the dop column now lives in one place, so
// it is tested in one place. buildRequestsQuery above only asks whether the
// capability is set; this is what sets it.
func TestSupportsRequestDOP(t *testing.T) {
	cases := []struct {
		name string
		info model.ServerInfo
		want bool
	}{
		{"2012", model.ServerInfo{MajorVersion: 11}, false},
		{"2014", model.ServerInfo{MajorVersion: 12}, false},
		{"2016, the first with the column", model.ServerInfo{MajorVersion: 13}, true},
		{"2022", model.ServerInfo{MajorVersion: 16}, true},
		// Both Azure engines report 12.0.x while sitting at or above the
		// newest boxed release, so the version alone would strip a column
		// they have.
		{"azure sql database reports 12", model.ServerInfo{MajorVersion: 12, IsAzureSQLDB: true}, true},
		{"managed instance reports 12", model.ServerInfo{MajorVersion: 12, IsAzureMI: true}, true},
		{"nothing known yet", model.ServerInfo{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := supportsRequestDOP(c.info); got != c.want {
				t.Errorf("supportsRequestDOP = %v, want %v", got, c.want)
			}
		})
	}
}
