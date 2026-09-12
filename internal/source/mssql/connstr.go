package mssql

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

// ConnParams is what the connect page posts. docs/specs/2026-09-11-connect-page-design.md
// section 5.
type ConnParams struct {
	Server   string `json:"server"`   // host, host\instance, host,port, as SSMS takes them
	Auth     string `json:"auth"`     // sql, windows or domain
	Login    string `json:"login"`    // sql: the login; domain: DOMAIN\user
	Password string `json:"password"` // sql and domain only
	Database string `json:"database"` // optional
	Encrypt  string `json:"encrypt"`  // "", "true" or "strict"
	Trust    bool   `json:"trust"`    // read only when Encrypt is "true"
}

// AuthMethod is one way of logging in that the connect page offers. Fields
// names which of login and password the form shows for it.
type AuthMethod struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Fields []string `json:"fields"`
	Note   string   `json:"note,omitempty"`
}

// AuthMethods lists the methods this binary can use, in the order the page
// shows them.
func AuthMethods() []AuthMethod { return authMethods(runtime.GOOS) }

// authMethods takes the platform as a parameter so every platform's list is
// testable anywhere. The current account needs winsspi, which go-mssqldb
// registers only in Windows builds.
func authMethods(goos string) []AuthMethod {
	ms := []AuthMethod{{ID: "sql", Label: "SQL Server login", Fields: []string{"login", "password"}}}
	if goos == "windows" {
		ms = append(ms, AuthMethod{ID: "windows", Label: "Windows, current account", Fields: []string{}})
	}
	domain := AuthMethod{ID: "domain", Label: "Windows, domain account", Fields: []string{"login", "password"}}
	if goos != "windows" {
		domain.Note = "Sent as NTLM. A domain policy can refuse NTLM; Kerberos then needs a connection string written by hand, see the README."
	}
	return append(ms, domain)
}

func offered(auth string) bool {
	for _, m := range AuthMethods() {
		if m.ID == auth {
			return true
		}
	}
	return false
}

// splitServer reads the server field the way SSMS does.
func splitServer(s string) (host, instance string, port int, err error) {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && strings.EqualFold(s[:4], "tcp:") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, ","); i >= 0 {
		n, err := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err != nil || n < 1 || n > 65535 {
			return "", "", 0, errors.New("the port after the comma must be a number from 1 to 65535")
		}
		port, s = n, strings.TrimSpace(s[:i])
	}
	host = s
	if h, inst, ok := strings.Cut(s, `\`); ok {
		host, instance = strings.TrimSpace(h), strings.TrimSpace(inst)
		if instance == "" {
			return "", "", 0, errors.New(`nothing follows the backslash: write host\instance, or leave the instance out`)
		}
	}
	if host == "." || strings.EqualFold(host, "(local)") {
		host = "localhost"
	}
	if host == "" {
		return "", "", 0, errors.New("the server is empty")
	}
	// These would end the host inside the URL, and the driver would then
	// fail with a bare "invalid URL format" instead of a word about the field.
	if strings.ContainsAny(host+instance, " \t/?#@") {
		return "", "", 0, errors.New("the server name cannot contain a space or any of / ? # @")
	}
	return host, instance, port, nil
}

// BuildDSN returns a sqlserver:// URL, or an error the page can show under
// the field at fault. Assembled with net/url, which is what escapes a
// password or a DOMAIN\user correctly.
func BuildDSN(p ConnParams) (string, error) {
	host, instance, port, err := splitServer(p.Server)
	if err != nil {
		return "", err
	}
	if !offered(p.Auth) {
		return "", fmt.Errorf("authentication %q is not offered by this build", p.Auth)
	}
	u := url.URL{Scheme: "sqlserver"}
	login := strings.TrimSpace(p.Login)
	switch p.Auth {
	case "sql":
		if login == "" {
			return "", errors.New("the login is empty")
		}
		u.User = url.UserPassword(login, p.Password)
	case "domain":
		if login == "" {
			return "", errors.New("the login is empty")
		}
		if !strings.Contains(login, `\`) {
			return "", errors.New(`a domain account is written DOMAIN\user`)
		}
		if p.Password == "" {
			return "", errors.New("a domain account needs its password")
		}
		u.User = url.UserPassword(login, p.Password)
	}
	// No brackets without a port: the driver keeps the host as given, and
	// would dial "[::1]" literally.
	u.Host = host
	if port > 0 {
		u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if instance != "" {
		u.Path = "/" + instance
	}
	q := url.Values{}
	if p.Database != "" {
		q.Set("database", p.Database)
	}
	switch p.Encrypt {
	case "":
	case "true":
		q.Set("encrypt", "true")
		if p.Trust {
			q.Set("trustservercertificate", "true")
		}
	case "strict":
		q.Set("encrypt", "strict")
	default:
		return "", fmt.Errorf("unknown encryption %q", p.Encrypt)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Redacted is dsn with its password replaced, for the page and the log. An
// unparseable string gives nothing rather than itself.
func Redacted(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return u.Redacted()
}
