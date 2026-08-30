package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilterLogic checks the five filter operators of spec section 8.1 as
// logic rather than as something once tried in a browser. parseFilter and
// matches are pure, so the region between the filter-logic markers in
// app.js is lifted out verbatim and run against a table of cases.
//
// Lifting the real source rather than restating it is the point: a test that
// carried its own copy of the parser would go on passing after the shipped
// one changed. The region markers are read verbatim, so moving them fails
// here loudly instead of quietly testing nothing.
func TestFilterLogic(t *testing.T) {
	deno := findDeno()
	if deno == "" {
		t.Skip("deno not installed; this test runs the shipped JavaScript rather than a Go transcription of it")
	}

	src, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	logic, err := regionBetween(string(src), "filter-logic: begin", "filter-logic: end")
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"function parseFilter", "function matches"} {
		if !strings.Contains(logic, must) {
			t.Fatalf("the filter-logic region no longer contains %q; the markers have drifted away from the code they were put around", must)
		}
	}

	cases := []struct {
		name   string
		filter string
		value  any
		want   bool
	}{
		{"contains matches anywhere", "update", "UPDATE dbo.Stock SET x = 1", true},
		{"contains is case-insensitive", "STOCK", "update dbo.stock", true},
		{"contains that does not match", "delete", "UPDATE dbo.Stock", false},
		{"equals is exact", "=SELECT", "SELECT", true},
		{"equals is case-insensitive", "=select", "SELECT", true},
		{"equals rejects a prefix", "=SEL", "SELECT", false},
		{"equals rejects a superstring", "=SELECT", "SELECT INTO", false},
		{"greater than", ">1000", 1001, true},
		{"greater than, equal is not greater", ">1000", 1000, false},
		{"less than", "<50", 49, true},
		{"less than, equal is not less", "<50", 50, false},
		{"numeric filter on text matches nothing", ">1000", "SELECT", false},
		{"non-numeric bound matches nothing", ">abc", 5000, false},
		{"in, first value", "CRM,Ventes", "CRM", true},
		{"in, second value", "CRM,Ventes", "Ventes", true},
		{"in is case-insensitive", "crm,ventes", "CRM", true},
		{"in rejects an absent value", "CRM,Ventes", "Archive", false},
		{"in ignores spacing", " CRM , Ventes ", "Ventes", true},
		{"in is exact per value, not contains", "CRM,Ventes", "CRM2", false},
		{"a number compared as text still contains", "100", 2100, true},
		{"empty value against contains", "x", "", false},
		{"null value does not throw", "x", nil, false},
	}

	var b strings.Builder
	b.WriteString(logic)
	b.WriteString("\nconst CASES = [\n")
	for _, c := range cases {
		b.WriteString("  {name:" + jsString(c.name) + ", filter:" + jsString(c.filter) + ", value:" + jsValue(c.value) + ", want:" + boolLit(c.want) + "},\n")
	}
	b.WriteString(`];
let bad = 0;
for (const c of CASES) {
  const f = parseFilter(c.filter);
  const got = f === null ? false : matches(f, c.value);
  if (got !== c.want) {
    console.log("FAIL " + c.name + ": filter " + JSON.stringify(c.filter) + " against " + JSON.stringify(c.value) + " gave " + got + ", want " + c.want);
    bad++;
  }
}
// An empty or blank box has to parse to nothing, or every row is filtered
// out the moment somebody types a space and deletes it.
for (const empty of ["", "   ", ">", "<", "=", ">   ", ",", " , "]) {
  if (parseFilter(empty) !== null) {
    console.log("FAIL empty: " + JSON.stringify(empty) + " parsed to " + JSON.stringify(parseFilter(empty)) + ", want null");
    bad++;
  }
}
console.log(bad === 0 ? "OK" : "FAILURES " + bad);
`)

	script := filepath.Join(t.TempDir(), "filterlogic.js")
	if err := os.WriteFile(script, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(deno, "run", "--quiet", script).CombinedOutput()
	if err != nil {
		t.Fatalf("running the lifted logic failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "OK") {
		t.Errorf("the shipped filter logic does not behave as spec section 8.1 describes:\n%s", out)
	}
}

func regionBetween(src, begin, end string) (string, error) {
	i := strings.Index(src, begin)
	j := strings.Index(src, end)
	if i < 0 || j < 0 || j < i {
		return "", &markerError{begin, end}
	}
	return src[i+len(begin) : j], nil
}

type markerError struct{ begin, end string }

func (e *markerError) Error() string {
	return "could not find the " + e.begin + " / " + e.end + " markers in assets/app.js; this test lifts the shipped filter logic out between them and cannot run without them"
}

func findDeno() string {
	if p, err := exec.LookPath("deno"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".deno", "bin", "deno")
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p
	}
	return ""
}

func jsString(s string) string {
	return "\"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + "\""
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func jsValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return jsString(t)
	case int:
		return itoa(t)
	}
	return "null"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}
