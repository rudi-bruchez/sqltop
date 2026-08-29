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

	cases := []struct {
		name      string
		info      model.ServerInfo
		caps      model.Capabilities
		wantDOP   bool
		wantTempd bool
	}{
		{"2014 has no dop column", model.ServerInfo{MajorVersion: 12}, tempdbCaps, false, true},
		{"2016 has one", model.ServerInfo{MajorVersion: 13}, tempdbCaps, true, true},
		{"2022", model.ServerInfo{MajorVersion: 16}, tempdbCaps, true, true},
		{
			// Both Azure engines report 12.0.x while sitting above the
			// newest boxed release, so the version alone would strip a
			// column they have.
			"azure sql database reports 12 and still has dop",
			model.ServerInfo{MajorVersion: 12, IsAzureSQLDB: true}, tempdbCaps, true, true,
		},
		{
			"managed instance reports 12 and still has dop",
			model.ServerInfo{MajorVersion: 12, IsAzureMI: true}, tempdbCaps, true, true,
		},
		{"no rights on the tempdb dmv", model.ServerInfo{MajorVersion: 16}, 0, true, false},
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
