package forward

import (
	"net/url"
	"path"
	"strings"
)

/*
	outpost.go

	This script implements the "outpost passthrough" convenience preset for forward auth.

	Some forward-auth providers (notably Authentik in single-application / per-app mode)
	require the browser to reach an auth callback subpath that is served on the *protected
	application's own domain* (e.g. /outpost.goauthentik.io/callback). That subpath must be
	reverse-proxied to the auth outpost AND excluded from the auth check, otherwise the
	OAuth callback is intercepted by the forward-auth verify request and the login loops
	(see issue #895).

	When the preset is enabled, any host using forward auth transparently routes + bypasses
	the configured outpost path prefix, so single-application setups work without configuring
	a virtual directory per host. The outpost upstream is derived from the (global) forward
	auth Address, so no additional configuration is required.
*/

// requestPathWithinPrefix reports whether the given request URI falls *within* the supplied
// path prefix using normalized path boundaries. It is hardened against:
//   - boundary tricks    e.g. "/outpost.goauthentik.io.evil" must NOT match "/outpost.goauthentik.io"
//   - path traversal     e.g. "/outpost.goauthentik.io/../admin" must NOT match (resolves to "/admin")
//   - encoded traversal  e.g. "/outpost.goauthentik.io/%2e%2e/admin"
//
// This matters because a false positive would route a request to the outpost and skip
// authentication for a path the operator did not intend to expose.
func requestPathWithinPrefix(requestURI string, prefix string) bool {
	requestPath := requestURI
	if u, err := url.ParseRequestURI(requestURI); err == nil {
		//Use the decoded path only, dropping any query string / fragment
		requestPath = u.Path
	}
	requestPath = path.Clean("/" + requestPath)
	cleanedPrefix := path.Clean("/" + prefix)
	return requestPath == cleanedPrefix || strings.HasPrefix(requestPath, cleanedPrefix+"/")
}

// deriveOutpostBaseURL derives the outpost upstream base URL from the configured forward
// auth address by locating the public outpost prefix within it. For example, given the
// address "http://authentik:9000/outpost.goauthentik.io/auth/traefik" and the prefix
// "/outpost.goauthentik.io", it returns "http://authentik:9000/outpost.goauthentik.io".
//
// It returns ok=false when either argument is empty or the prefix is not present in the
// address (in which case the outpost base cannot be determined and the preset is inert).
func deriveOutpostBaseURL(address string, prefix string) (string, bool) {
	address = strings.TrimSpace(address)
	prefix = strings.TrimSpace(prefix)
	if address == "" || prefix == "" {
		return "", false
	}
	idx := strings.Index(address, prefix)
	if idx < 0 {
		return "", false
	}
	return address[:idx+len(prefix)], true
}

// MatchOutpostPassthrough reports whether the given request should be transparently
// reverse-proxied to the auth provider's outpost (and excluded from authentication) under
// the outpost passthrough preset. When ok is true, baseURL is the outpost upstream the
// request should be proxied to and prefix is the matched public path prefix.
//
// It returns ok=false when the preset is disabled, no prefix is configured, the request
// does not fall within the prefix, or the outpost base cannot be derived from the address.
func (ar *AuthRouter) MatchOutpostPassthrough(requestURI string) (baseURL string, prefix string, ok bool) {
	if ar == nil || !ar.options.OutpostPassthrough {
		return "", "", false
	}
	prefix = strings.TrimSpace(ar.options.OutpostPathPrefix)
	if prefix == "" {
		return "", "", false
	}
	if !requestPathWithinPrefix(requestURI, prefix) {
		return "", "", false
	}
	baseURL, ok = deriveOutpostBaseURL(ar.options.Address, prefix)
	if !ok {
		return "", "", false
	}
	return baseURL, prefix, true
}
