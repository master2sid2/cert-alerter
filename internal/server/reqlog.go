package server

import (
	"net/url"
	"strings"
)

// secretPathPrefixes are paths whose last segment is a single-use token
// (invitation, password reset). The token is replaced in logs.
var secretPathPrefixes = []string{"/invite/", "/reset/", "/api/auth/token/"}

// secretQueryParams are query parameters that carry secrets.
var secretQueryParams = []string{"token"}

const redacted = "REDACTED"

// redactURI returns the request URI with tokens removed so that log files
// never contain credentials.
func redactURI(u *url.URL) string {
	path := u.EscapedPath()
	for _, p := range secretPathPrefixes {
		if rest, ok := strings.CutPrefix(path, p); ok && rest != "" {
			path = p + redacted
			break
		}
	}
	if u.RawQuery == "" {
		return path
	}
	q := u.RawQuery
	if vals, err := url.ParseQuery(q); err == nil {
		changed := false
		for _, k := range secretQueryParams {
			if vals.Has(k) {
				vals.Set(k, redacted)
				changed = true
			}
		}
		if changed {
			q = vals.Encode()
		}
	} else {
		// Unparseable query: do not risk logging a secret.
		for _, k := range secretQueryParams {
			if strings.Contains(q, k+"=") {
				q = redacted
				break
			}
		}
	}
	return path + "?" + q
}
