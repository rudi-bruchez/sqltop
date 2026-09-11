package mssql

import "strings"

// hints is read in order and the first match wins: a certificate failure
// reads "TLS Handshake failed: ... x509: ...", so x509 comes before the
// handshake row.
var hints = []struct{ in, hint string }{
	{"no instance matching", "SQL Server Browser did not return that instance. Type host,port instead; the port is in SQL Server Configuration Manager."},
	{"x509:", `The server certificate was refused. Leave encryption on the driver default, or choose "required" and tick "trust the server certificate".`},
	{"TLS Handshake failed", `The server did not complete the TLS handshake. With "strict", the server must support TDS 8 (SQL Server 2022 and later, with strict encryption configured); otherwise choose another encryption mode.`},
	{"Login failed for user", "The server refused the login. For a SQL Server login, the server must also allow SQL Server authentication."},
}

// Hint is the advice the connect page shows under a failed attempt, or
// nothing when the error is not one it knows.
func Hint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, h := range hints {
		if strings.Contains(msg, h.in) {
			return h.hint
		}
	}
	return ""
}
