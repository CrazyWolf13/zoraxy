# Forward Auth — Outpost Passthrough preset (zero-config Authentik per-app)

This document describes a **convenience preset** for Forward Auth that makes
**single-application** setups — most notably **Authentik per-application proxy providers** —
work with **no per-host configuration**. It resolves the `ERR_TOO_MANY_REDIRECTS` loop from
[issue #895](https://github.com/tobychui/zoraxy/issues/895) with a single global switch.

> This branch is an **independent, standalone** solution built directly on `main`. It does not
> depend on the per-virtual-directory "Skip Authentication" primitive (a separate proposal).
> Either approach fixes #895; this one optimizes for the least possible setup.

---

## TL;DR

Authentik per-app forward auth needs the browser to reach the outpost callback path
(`/outpost.goauthentik.io/*`) **on the protected app's own domain**. Zoraxy evaluated forward
auth before routing that path, so the OAuth callback looped forever.

With this preset, you flip **one global toggle** in **Security → SSO/Forward Auth**. After that,
**every** host whose authentication method is **Forward Auth** automatically:

1. reverse-proxies `/outpost.goauthentik.io/*` to the outpost, and
2. excludes that subpath from authentication.

The outpost upstream is **derived from the Forward Auth address you already configured**, so
there is nothing else to set up — no virtual directory per host, no per-entry toggle.

```
Forward Auth address:  http://10.10.20.213:9000/outpost.goauthentik.io/auth/traefik
                                               └────────── derived ──────────┘
Outpost upstream:      http://10.10.20.213:9000/outpost.goauthentik.io
Passed-through path:   /outpost.goauthentik.io   (excluded from auth, routed to the outpost)
```

## The problem (issue #895)

In Authentik **single-application** mode the OAuth callback lands on the *protected app's*
domain (`https://app.example.com/outpost.goauthentik.io/callback?code=…`). Zoraxy ran the
forward-auth verify call (`/outpost.goauthentik.io/auth/traefik`) **before** it could route that
callback to the outpost. The verify endpoint only answers "authenticated?" — it does not complete
the OAuth code exchange — so it returned `302` and the browser looped until `ERR_TOO_MANY_REDIRECTS`.

Domain-level mode worked because the callback lands on Authentik's own subdomain (no forward auth
in front of it) and the cookie is shared across the parent domain.

## The fix — global "Outpost Passthrough" preset

Forward auth in Zoraxy is configured **globally** (one address shared by all hosts), and the
outpost path is identical for every app. So a single global setting is enough:

- **`OutpostPassthrough`** (toggle) — enable the preset.
- **`OutpostPathPrefix`** (defaults to `/outpost.goauthentik.io`) — the public callback prefix.

When enabled, for any host using Forward Auth, `Server.go` checks each request **before**
authentication: if the (normalized) request path falls within `OutpostPathPrefix`, the request is
reverse-proxied to the derived outpost base and authentication is skipped. The reverse proxy is
the same `dpcore` machinery used by virtual directories, cached and rebuilt only if the address
changes.

### Why global, and why derived from the address

- Forward auth is already global, and the outpost path is the same for every app → a global
  switch needs **zero** per-host input (you just pick *Forward Auth* on the host as before).
- The outpost upstream is derived by locating `OutpostPathPrefix` inside the verify address, so
  there is no second URL to enter and no chance of it drifting out of sync.
- It only ever proxies to the **trusted auth upstream** (never the protected app backend), and
  only for hosts that actually use Forward Auth.

### Security: hardened path matching

The passthrough decision uses normalized matching (`requestPathWithinPrefix` in
`mod/auth/sso/forward/outpost.go`): the path is decoded and `path.Clean`-ed, and must **equal**
the prefix or start with `prefix + "/"`. This fails closed against:

| Input | Result |
| --- | --- |
| `/outpost.goauthentik.io.evil/x` (boundary) | not matched → auth still runs |
| `/outpost.goauthentik.io/../admin` (traversal → `/admin`) | not matched → auth still runs |
| `/outpost.goauthentik.io/%2e%2e/admin` (encoded) | not matched → auth still runs |

If the prefix cannot be located within the configured address, the preset is treated as disabled
(fails closed).

## How to enable

1. **Authentik** — Proxy Provider in *Forward auth (single application)* mode; *External host* =
   your app's public URL; assign it to your outpost.
2. **Zoraxy → Security → SSO/Forward Auth:**
   - **Address:** `http://<authentik-host>:9000/outpost.goauthentik.io/auth/traefik` (X-Original-\* OFF)
   - **Authentik per-application passthrough:** **ON**
   - **Outpost Callback Path:** `/outpost.goauthentik.io` (pre-filled default)
3. **Per host:** just set the authentication method to **Forward Auth**. Nothing else.

Visiting `https://app.example.com/` now redirects to Authentik, logs in, and returns to the app
with no loop — for every Forward-Auth host, automatically.

## Known limitations / caveats

- **Provider-aware default.** The prefix defaults to Authentik's `/outpost.goauthentik.io`. It is
  editable for other providers/paths; leave the toggle off for providers that don't need it
  (e.g. Authelia, whose portal lives on its own domain).
- **CAPTCHA / Block Common Exploits are not bypassed.** They run before authentication and are not
  affected by this preset. If enabled on a host, disable them for it (or the callback's long
  `state`/`code` query may be challenged/flagged).
- Applies to **all** Forward-Auth hosts. A per-host opt-out could be added later if needed.

## Changes

### Backend (Go)

| File | Change |
| --- | --- |
| `src/mod/auth/sso/forward/const.go` | Add `outpostPassthrough` / `outpostPathPrefix` DB keys + `DefaultOutpostPathPrefix`. |
| `src/mod/auth/sso/forward/forward.go` | Add `OutpostPassthrough` / `OutpostPathPrefix` options; load/GET/POST/DELETE/log them. |
| `src/mod/auth/sso/forward/outpost.go` | New: `requestPathWithinPrefix` (hardened matcher), `deriveOutpostBaseURL`, `MatchOutpostPassthrough`. |
| `src/mod/auth/sso/forward/outpost_test.go` | New: unit tests for the matcher and derivation (boundary, traversal, encoded, endpoint variants). |
| `src/mod/dynamicproxy/typedef.go` | Add cached outpost reverse-proxy fields to `Router`. |
| `src/mod/dynamicproxy/router.go` | Add `getForwardOutpostProxy` (lazy, rebuild-on-change cache). |
| `src/mod/dynamicproxy/Server.go` | Before auth, route the outpost passthrough for Forward-Auth hosts. |

### Frontend

| File | Change |
| --- | --- |
| `src/web/components/sso.html` | Add the passthrough toggle + callback-path field; load and save them. |

### Backward compatibility

Both settings default to off / the Authentik path. Existing setups are unchanged until the toggle
is enabled. No config migration is required.

## Testing

- **Unit:** `go test ./mod/auth/sso/forward/` — covers `requestPathWithinPrefix` (incl. boundary
  and path-traversal cases) and `deriveOutpostBaseURL` (traefik/nginx/https endpoints, prefix
  absent, empty inputs).
- **Build:** `go build ./...` from `src/`.
- **Manual (recommended for the test branch):** enable the toggle, set a host to Forward Auth with
  no virtual directory, and confirm the full Authentik login round-trip completes with no loop.

## Related

- Issue: [#895 — \[HELP\] Forward Auth with Authentik](https://github.com/tobychui/zoraxy/issues/895)
- Alternative approach (separate branch): per-virtual-directory "Skip Authentication" flag — the
  generic, explicit primitive. This preset is the zero-config convenience.

---

## Full diff

The complete set of changes is shown below (this documentation file is omitted). New files: `src/mod/auth/sso/forward/outpost.go` and `src/mod/auth/sso/forward/outpost_test.go`.

```diff
diff --git a/src/mod/auth/sso/forward/const.go b/src/mod/auth/sso/forward/const.go
index 6902e58..3da04bb 100644
--- a/src/mod/auth/sso/forward/const.go
+++ b/src/mod/auth/sso/forward/const.go
@@ -15,6 +15,12 @@ const (
 	DatabaseKeyRequestExcludedCookies = "requestExcludedCookies"
 	DatabaseKeyRequestIncludeBody     = "requestIncludeBody"
 	DatabaseKeyUseXOriginalHeaders    = "useXOriginalHeaders"
+	DatabaseKeyOutpostPassthrough     = "outpostPassthrough"
+	DatabaseKeyOutpostPathPrefix      = "outpostPathPrefix"
+
+	// DefaultOutpostPathPrefix is the public path prefix served by the Authentik outpost.
+	// It is used as the default value for the outpost passthrough preset.
+	DefaultOutpostPathPrefix = "/outpost.goauthentik.io"
 
 	HeaderXForwardedProto  = "X-Forwarded-Proto"
 	HeaderXForwardedHost   = "X-Forwarded-Host"
diff --git a/src/mod/auth/sso/forward/forward.go b/src/mod/auth/sso/forward/forward.go
index 368408d..30493b1 100644
--- a/src/mod/auth/sso/forward/forward.go
+++ b/src/mod/auth/sso/forward/forward.go
@@ -43,6 +43,17 @@ type AuthRouterOptions struct {
 	// X-Forwarded-* headers.
 	UseXOriginalHeaders bool
 
+	// OutpostPassthrough enables automatic reverse-proxying + authentication bypass of the
+	// auth provider's outpost/callback subpath (OutpostPathPrefix) for every host using
+	// forward auth. This makes single-application setups (e.g. Authentik per-app proxy
+	// providers) work without configuring a virtual directory per host.
+	OutpostPassthrough bool
+
+	// OutpostPathPrefix is the public path prefix routed to the outpost (derived from
+	// Address) and excluded from authentication when OutpostPassthrough is enabled.
+	// Defaults to DefaultOutpostPathPrefix.
+	OutpostPathPrefix string
+
 	Logger   *logger.Logger
 	Database *database.Database
 }
@@ -68,6 +79,11 @@ func NewAuthRouter(options *AuthRouterOptions) *AuthRouter {
 	options.Database.Read(DatabaseTable, DatabaseKeyRequestExcludedCookies, &requestExcludedCookies)
 	options.Database.Read(DatabaseTable, DatabaseKeyRequestIncludeBody, &options.RequestIncludeBody)
 	options.Database.Read(DatabaseTable, DatabaseKeyUseXOriginalHeaders, &options.UseXOriginalHeaders)
+	options.Database.Read(DatabaseTable, DatabaseKeyOutpostPassthrough, &options.OutpostPassthrough)
+	options.Database.Read(DatabaseTable, DatabaseKeyOutpostPathPrefix, &options.OutpostPathPrefix)
+	if strings.TrimSpace(options.OutpostPathPrefix) == "" {
+		options.OutpostPathPrefix = DefaultOutpostPathPrefix
+	}
 
 	options.ResponseHeaders = cleanSplit(responseHeaders)
 	options.ResponseClientHeaders = cleanSplit(responseClientHeaders)
@@ -113,6 +129,8 @@ func (ar *AuthRouter) handleOptionsGET(w http.ResponseWriter, r *http.Request) {
 		DatabaseKeyRequestExcludedCookies: ar.options.RequestExcludedCookies,
 		DatabaseKeyRequestIncludeBody:     ar.options.RequestIncludeBody,
 		DatabaseKeyUseXOriginalHeaders:    ar.options.UseXOriginalHeaders,
+		DatabaseKeyOutpostPassthrough:     ar.options.OutpostPassthrough,
+		DatabaseKeyOutpostPathPrefix:      ar.options.OutpostPathPrefix,
 	})
 
 	utils.SendJSONResponse(w, string(js))
@@ -137,6 +155,8 @@ func (ar *AuthRouter) handleOptionsPOST(w http.ResponseWriter, r *http.Request)
 	requestExcludedCookies, _ := utils.PostPara(r, DatabaseKeyRequestExcludedCookies)
 	requestIncludeBody, _ := utils.PostPara(r, DatabaseKeyRequestIncludeBody)
 	useXOriginalHeaders, _ := utils.PostPara(r, DatabaseKeyUseXOriginalHeaders)
+	outpostPassthrough, _ := utils.PostPara(r, DatabaseKeyOutpostPassthrough)
+	outpostPathPrefix, _ := utils.PostPara(r, DatabaseKeyOutpostPathPrefix)
 
 	// Write changes to runtime
 	ar.options.Address = address
@@ -147,6 +167,11 @@ func (ar *AuthRouter) handleOptionsPOST(w http.ResponseWriter, r *http.Request)
 	ar.options.RequestExcludedCookies = cleanSplit(requestExcludedCookies)
 	ar.options.RequestIncludeBody, _ = strconv.ParseBool(requestIncludeBody)
 	ar.options.UseXOriginalHeaders, _ = strconv.ParseBool(useXOriginalHeaders)
+	ar.options.OutpostPassthrough, _ = strconv.ParseBool(outpostPassthrough)
+	ar.options.OutpostPathPrefix = strings.TrimSpace(outpostPathPrefix)
+	if ar.options.OutpostPathPrefix == "" {
+		ar.options.OutpostPathPrefix = DefaultOutpostPathPrefix
+	}
 
 	// Write changes to database
 	ar.options.Database.Write(DatabaseTable, DatabaseKeyAddress, address)
@@ -157,6 +182,8 @@ func (ar *AuthRouter) handleOptionsPOST(w http.ResponseWriter, r *http.Request)
 	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestExcludedCookies, requestExcludedCookies)
 	ar.options.Database.Write(DatabaseTable, DatabaseKeyRequestIncludeBody, ar.options.RequestIncludeBody)
 	ar.options.Database.Write(DatabaseTable, DatabaseKeyUseXOriginalHeaders, ar.options.UseXOriginalHeaders)
+	ar.options.Database.Write(DatabaseTable, DatabaseKeyOutpostPassthrough, ar.options.OutpostPassthrough)
+	ar.options.Database.Write(DatabaseTable, DatabaseKeyOutpostPathPrefix, ar.options.OutpostPathPrefix)
 
 	ar.logOptions()
 
@@ -172,6 +199,8 @@ func (ar *AuthRouter) handleOptionsDelete(w http.ResponseWriter, r *http.Request
 	ar.options.RequestExcludedCookies = nil
 	ar.options.RequestIncludeBody = false
 	ar.options.UseXOriginalHeaders = false
+	ar.options.OutpostPassthrough = false
+	ar.options.OutpostPathPrefix = DefaultOutpostPathPrefix
 
 	ar.options.Database.Delete(DatabaseTable, DatabaseKeyAddress)
 	ar.options.Database.Delete(DatabaseTable, DatabaseKeyResponseHeaders)
@@ -181,6 +210,8 @@ func (ar *AuthRouter) handleOptionsDelete(w http.ResponseWriter, r *http.Request
 	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestExcludedCookies)
 	ar.options.Database.Delete(DatabaseTable, DatabaseKeyRequestIncludeBody)
 	ar.options.Database.Delete(DatabaseTable, DatabaseKeyUseXOriginalHeaders)
+	ar.options.Database.Delete(DatabaseTable, DatabaseKeyOutpostPassthrough)
+	ar.options.Database.Delete(DatabaseTable, DatabaseKeyOutpostPathPrefix)
 
 	utils.SendOK(w)
 }
@@ -272,5 +303,5 @@ func (ar *AuthRouter) handle500Error(w http.ResponseWriter, err error, message s
 }
 
 func (ar *AuthRouter) logOptions() {
-	ar.options.Logger.PrintAndLog(LogTitle, fmt.Sprintf("Forward Authz Options -> Address: %s, Response Headers: %s, Response Client Headers: %s, Request Headers: %s, Request Included Cookies: %s, Request Excluded Cookies: %s, Request Include Body: %t, Use X-Original Headers: %t", ar.options.Address, strings.Join(ar.options.ResponseHeaders, ";"), strings.Join(ar.options.ResponseClientHeaders, ";"), strings.Join(ar.options.RequestHeaders, ";"), strings.Join(ar.options.RequestIncludedCookies, ";"), strings.Join(ar.options.RequestExcludedCookies, ";"), ar.options.RequestIncludeBody, ar.options.UseXOriginalHeaders), nil)
+	ar.options.Logger.PrintAndLog(LogTitle, fmt.Sprintf("Forward Authz Options -> Address: %s, Response Headers: %s, Response Client Headers: %s, Request Headers: %s, Request Included Cookies: %s, Request Excluded Cookies: %s, Request Include Body: %t, Use X-Original Headers: %t, Outpost Passthrough: %t, Outpost Path Prefix: %s", ar.options.Address, strings.Join(ar.options.ResponseHeaders, ";"), strings.Join(ar.options.ResponseClientHeaders, ";"), strings.Join(ar.options.RequestHeaders, ";"), strings.Join(ar.options.RequestIncludedCookies, ";"), strings.Join(ar.options.RequestExcludedCookies, ";"), ar.options.RequestIncludeBody, ar.options.UseXOriginalHeaders, ar.options.OutpostPassthrough, ar.options.OutpostPathPrefix), nil)
 }
diff --git a/src/mod/auth/sso/forward/outpost.go b/src/mod/auth/sso/forward/outpost.go
new file mode 100644
index 0000000..a0fcbc9
--- /dev/null
+++ b/src/mod/auth/sso/forward/outpost.go
@@ -0,0 +1,89 @@
+package forward
+
+import (
+	"net/url"
+	"path"
+	"strings"
+)
+
+/*
+	outpost.go
+
+	This script implements the "outpost passthrough" convenience preset for forward auth.
+
+	Some forward-auth providers (notably Authentik in single-application / per-app mode)
+	require the browser to reach an auth callback subpath that is served on the *protected
+	application's own domain* (e.g. /outpost.goauthentik.io/callback). That subpath must be
+	reverse-proxied to the auth outpost AND excluded from the auth check, otherwise the
+	OAuth callback is intercepted by the forward-auth verify request and the login loops
+	(see issue #895).
+
+	When the preset is enabled, any host using forward auth transparently routes + bypasses
+	the configured outpost path prefix, so single-application setups work without configuring
+	a virtual directory per host. The outpost upstream is derived from the (global) forward
+	auth Address, so no additional configuration is required.
+*/
+
+// requestPathWithinPrefix reports whether the given request URI falls *within* the supplied
+// path prefix using normalized path boundaries. It is hardened against:
+//   - boundary tricks    e.g. "/outpost.goauthentik.io.evil" must NOT match "/outpost.goauthentik.io"
+//   - path traversal     e.g. "/outpost.goauthentik.io/../admin" must NOT match (resolves to "/admin")
+//   - encoded traversal  e.g. "/outpost.goauthentik.io/%2e%2e/admin"
+//
+// This matters because a false positive would route a request to the outpost and skip
+// authentication for a path the operator did not intend to expose.
+func requestPathWithinPrefix(requestURI string, prefix string) bool {
+	requestPath := requestURI
+	if u, err := url.ParseRequestURI(requestURI); err == nil {
+		//Use the decoded path only, dropping any query string / fragment
+		requestPath = u.Path
+	}
+	requestPath = path.Clean("/" + requestPath)
+	cleanedPrefix := path.Clean("/" + prefix)
+	return requestPath == cleanedPrefix || strings.HasPrefix(requestPath, cleanedPrefix+"/")
+}
+
+// deriveOutpostBaseURL derives the outpost upstream base URL from the configured forward
+// auth address by locating the public outpost prefix within it. For example, given the
+// address "http://authentik:9000/outpost.goauthentik.io/auth/traefik" and the prefix
+// "/outpost.goauthentik.io", it returns "http://authentik:9000/outpost.goauthentik.io".
+//
+// It returns ok=false when either argument is empty or the prefix is not present in the
+// address (in which case the outpost base cannot be determined and the preset is inert).
+func deriveOutpostBaseURL(address string, prefix string) (string, bool) {
+	address = strings.TrimSpace(address)
+	prefix = strings.TrimSpace(prefix)
+	if address == "" || prefix == "" {
+		return "", false
+	}
+	idx := strings.Index(address, prefix)
+	if idx < 0 {
+		return "", false
+	}
+	return address[:idx+len(prefix)], true
+}
+
+// MatchOutpostPassthrough reports whether the given request should be transparently
+// reverse-proxied to the auth provider's outpost (and excluded from authentication) under
+// the outpost passthrough preset. When ok is true, baseURL is the outpost upstream the
+// request should be proxied to and prefix is the matched public path prefix.
+//
+// It returns ok=false when the preset is disabled, no prefix is configured, the request
+// does not fall within the prefix, or the outpost base cannot be derived from the address.
+func (ar *AuthRouter) MatchOutpostPassthrough(requestURI string) (baseURL string, prefix string, ok bool) {
+	if ar == nil || !ar.options.OutpostPassthrough {
+		return "", "", false
+	}
+	prefix = strings.TrimSpace(ar.options.OutpostPathPrefix)
+	if prefix == "" {
+		return "", "", false
+	}
+	if !requestPathWithinPrefix(requestURI, prefix) {
+		return "", "", false
+	}
+	baseURL, ok = deriveOutpostBaseURL(ar.options.Address, prefix)
+	if !ok {
+		return "", "", false
+	}
+	return baseURL, prefix, true
+}
diff --git a/src/mod/auth/sso/forward/outpost_test.go b/src/mod/auth/sso/forward/outpost_test.go
new file mode 100644
index 0000000..a0974fe
--- /dev/null
+++ b/src/mod/auth/sso/forward/outpost_test.go
@@ -0,0 +1,68 @@
+package forward
+
+import "testing"
+
+// TestRequestPathWithinPrefix verifies the hardened path matching used to decide the
+// outpost passthrough. The boundary and path-traversal cases are security relevant: a false
+// positive would skip authentication on a path the operator did not intend to expose.
+func TestRequestPathWithinPrefix(t *testing.T) {
+	const outpost = "/outpost.goauthentik.io"
+
+	cases := []struct {
+		name   string
+		uri    string
+		prefix string
+		want   bool
+	}{
+		{"callback with query", "/outpost.goauthentik.io/callback?code=abc&state=xyz", outpost, true},
+		{"exact", "/outpost.goauthentik.io", outpost, true},
+		{"trailing slash", "/outpost.goauthentik.io/", outpost, true},
+		{"start endpoint", "/outpost.goauthentik.io/start", outpost, true},
+		{"sign_out endpoint", "/outpost.goauthentik.io/sign_out", outpost, true},
+		{"boundary sibling", "/outpost.goauthentik.io.evil/x", outpost, false},
+		{"prefix as substring", "/outpost.goauthentik.ioxyz", outpost, false},
+		{"dot dot traversal", "/outpost.goauthentik.io/../admin", outpost, false},
+		{"encoded traversal", "/outpost.goauthentik.io/%2e%2e/admin", outpost, false},
+		{"nested traversal", "/outpost.goauthentik.io/foo/../../secret", outpost, false},
+		{"unrelated path", "/api/v1/data", outpost, false},
+	}
+
+	for _, tc := range cases {
+		t.Run(tc.name, func(t *testing.T) {
+			if got := requestPathWithinPrefix(tc.uri, tc.prefix); got != tc.want {
+				t.Errorf("requestPathWithinPrefix(%q, %q) = %v, want %v", tc.uri, tc.prefix, got, tc.want)
+			}
+		})
+	}
+}
+
+// TestDeriveOutpostBaseURL verifies that the outpost upstream is correctly derived from the
+// forward auth verify address for the common Authentik endpoints, and that it fails closed
+// when the prefix is absent or inputs are empty.
+func TestDeriveOutpostBaseURL(t *testing.T) {
+	const outpost = "/outpost.goauthentik.io"
+
+	cases := []struct {
+		name    string
+		address string
+		prefix  string
+		wantURL string
+		wantOK  bool
+	}{
+		{"traefik endpoint", "http://10.10.20.213:9000/outpost.goauthentik.io/auth/traefik", outpost, "http://10.10.20.213:9000/outpost.goauthentik.io", true},
+		{"nginx endpoint", "http://authentik:9000/outpost.goauthentik.io/auth/nginx", outpost, "http://authentik:9000/outpost.goauthentik.io", true},
+		{"https endpoint", "https://auth.example.com/outpost.goauthentik.io/auth/traefik", outpost, "https://auth.example.com/outpost.goauthentik.io", true},
+		{"prefix absent", "http://authentik:9000/api/verify", outpost, "", false},
+		{"empty prefix", "http://authentik:9000/outpost.goauthentik.io/auth/traefik", "", "", false},
+		{"empty address", "", outpost, "", false},
+	}
+
+	for _, tc := range cases {
+		t.Run(tc.name, func(t *testing.T) {
+			gotURL, gotOK := deriveOutpostBaseURL(tc.address, tc.prefix)
+			if gotURL != tc.wantURL || gotOK != tc.wantOK {
+				t.Errorf("deriveOutpostBaseURL(%q, %q) = (%q, %v), want (%q, %v)", tc.address, tc.prefix, gotURL, gotOK, tc.wantURL, tc.wantOK)
+			}
+		})
+	}
+}
diff --git a/src/mod/dynamicproxy/Server.go b/src/mod/dynamicproxy/Server.go
index 9ecdd7d..9a7e983 100644
--- a/src/mod/dynamicproxy/Server.go
+++ b/src/mod/dynamicproxy/Server.go
@@ -126,6 +126,27 @@ func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 			}
 		}
 
+		//Forward-auth outpost passthrough (global preset). For hosts using forward auth, the
+		//auth provider's outpost/callback subpath (e.g. Authentik's /outpost.goauthentik.io) is
+		//transparently reverse-proxied to the outpost and excluded from authentication, so
+		//single-application setups work without a per-host virtual directory (see issue #895).
+		//The outpost upstream is derived from the global forward auth address. Matching is
+		//hardened against path traversal / boundary tricks (see forward.MatchOutpostPassthrough).
+		if sep.AuthenticationProvider.AuthMethod == AuthMethodForward && h.Parent.Option.ForwardAuthRouter != nil {
+			if outpostBase, outpostPrefix, matched := h.Parent.Option.ForwardAuthRouter.MatchOutpostPassthrough(r.RequestURI); matched {
+				if outpostProxy := h.Parent.getForwardOutpostProxy(outpostBase, outpostPrefix); outpostProxy != nil {
+					h.vdirRequest(w, r, &VirtualDirectoryEndpoint{
+						MatchingPath: outpostPrefix,
+						Domain:       strings.TrimPrefix(strings.TrimPrefix(outpostBase, "https://"), "http://"),
+						RequireTLS:   strings.HasPrefix(outpostBase, "https://"),
+						proxy:        outpostProxy,
+						parent:       sep,
+					})
+					return
+				}
+			}
+		}
+
 		//Validate auth (basic auth or SSO auth)
 		respWritten := handleAuthProviderRouting(sep, w, r, h)
 		if respWritten {
diff --git a/src/mod/dynamicproxy/router.go b/src/mod/dynamicproxy/router.go
index 49500af..067c314 100644
--- a/src/mod/dynamicproxy/router.go
+++ b/src/mod/dynamicproxy/router.go
@@ -73,6 +73,38 @@ func (router *Router) PrepareProxyRoute(endpoint *ProxyEndpoint) (*ProxyEndpoint
 	return endpoint, nil
 }
 
+// getForwardOutpostProxy returns a cached reverse proxy to the forward-auth outpost base
+// URL, building it on first use and rebuilding it if the base URL changes. The matchingPath
+// is the public prefix that is stripped before proxying (mirrors virtual directory routing).
+// Returns nil if the base URL cannot be parsed.
+func (router *Router) getForwardOutpostProxy(baseURLWithScheme string, matchingPath string) *dpcore.ReverseProxy {
+	router.forwardOutpostProxyMutex.RLock()
+	if router.forwardOutpostProxy != nil && router.forwardOutpostProxyBase == baseURLWithScheme {
+		proxy := router.forwardOutpostProxy
+		router.forwardOutpostProxyMutex.RUnlock()
+		return proxy
+	}
+	router.forwardOutpostProxyMutex.RUnlock()
+
+	router.forwardOutpostProxyMutex.Lock()
+	defer router.forwardOutpostProxyMutex.Unlock()
+	//Re-check after acquiring the write lock in case another goroutine built it
+	if router.forwardOutpostProxy != nil && router.forwardOutpostProxyBase == baseURLWithScheme {
+		return router.forwardOutpostProxy
+	}
+
+	target, err := url.Parse(baseURLWithScheme)
+	if err != nil {
+		return nil
+	}
+	proxy := dpcore.NewDynamicProxyCore(target, matchingPath, &dpcore.DpcoreOptions{
+		FlushInterval: 500 * time.Millisecond,
+	})
+	router.forwardOutpostProxy = proxy
+	router.forwardOutpostProxyBase = baseURLWithScheme
+	return proxy
+}
+
 // Add Proxy Route to current runtime. Call to PrepareProxyRoute before adding to runtime
 func (router *Router) AddProxyRouteToRuntime(endpoint *ProxyEndpoint) error {
 	lookupHostname := strings.ToLower(endpoint.RootOrMatchingDomain)
diff --git a/src/mod/dynamicproxy/typedef.go b/src/mod/dynamicproxy/typedef.go
index 5bb8d79..419f7dc 100644
--- a/src/mod/dynamicproxy/typedef.go
+++ b/src/mod/dynamicproxy/typedef.go
@@ -107,6 +107,12 @@ type Router struct {
 
 	captchaSessionStore *captcha.SessionStore //CAPTCHA session store for tracking verified sessions
 
+	// Forward-auth outpost passthrough: cached reverse proxy to the auth outpost, built
+	// lazily from the (global) forward auth address and rebuilt if it changes. See ServeHTTP.
+	forwardOutpostProxy      *dpcore.ReverseProxy
+	forwardOutpostProxyBase  string
+	forwardOutpostProxyMutex sync.RWMutex
+
 	// Secondary listening ports and their servers
 	secondaryServers     map[string]*http.Server //Map of secondary listening servers, key is the listening address (ip:port or :port)
 	secondaryStopChans   map[string]chan bool    //Stop channels for secondary listening servers
diff --git a/src/web/components/sso.html b/src/web/components/sso.html
index 96360ff..cf0dce7 100644
--- a/src/web/components/sso.html
+++ b/src/web/components/sso.html
@@ -30,6 +30,17 @@
                 <input type="text" id="forwardAuthAddress" name="forwardAuthAddress" placeholder="Enter Forward Auth Address">
                 <small>The full remote address or URL of the authorization servers forward auth endpoint. <strong>Example:</strong> http://127.0.0.1:9091/authz/forward-auth</small>
             </div>
+            <div class="field">
+                <div class="ui toggle checkbox">
+                    <input type="checkbox" id="forwardAuthOutpostPassthrough" name="forwardAuthOutpostPassthrough">
+                    <label>Authentik per-application passthrough<br><small>Automatically route and skip authentication for the auth provider's outpost callback path (below) on <b>every</b> host using Forward Auth. This makes single-application setups (e.g. Authentik per-app proxy providers) work <b>without adding a virtual directory per host</b>. The outpost upstream is derived from the Address above.</small></label>
+                </div>
+            </div>
+            <div class="field">
+                <label for="forwardAuthOutpostPathPrefix">Outpost Callback Path</label>
+                <input type="text" id="forwardAuthOutpostPathPrefix" name="forwardAuthOutpostPathPrefix" placeholder="/outpost.goauthentik.io">
+                <small>Public path prefix proxied to the outpost and excluded from authentication when passthrough is enabled. Must also appear within the Address above. Default: <code>/outpost.goauthentik.io</code> (Authentik).</small>
+            </div>
             <div class="ui basic segment advanceoptions" style="margin-top:0.6em;">
                 <div class="ui advancedSSOForwardAuthOptions accordion">
                     <div class="title">
@@ -370,6 +381,16 @@
                 } else {
                     $("#forwardAuthRequestUseXOriginalHeaders").parent().checkbox("set unchecked");
                 }
+                if (data.outpostPassthrough != null && data.outpostPassthrough === true) {
+                    $("#forwardAuthOutpostPassthrough").parent().checkbox("set checked");
+                } else {
+                    $("#forwardAuthOutpostPassthrough").parent().checkbox("set unchecked");
+                }
+                if (data.outpostPathPrefix != null && data.outpostPathPrefix !== "") {
+                    $('#forwardAuthOutpostPathPrefix').val(data.outpostPathPrefix);
+                } else {
+                    $('#forwardAuthOutpostPathPrefix').val("/outpost.goauthentik.io");
+                }
             },
             error: function(jqXHR, textStatus, errorThrown) {
                 console.error('Error fetching SSO settings:', textStatus, errorThrown);
@@ -391,6 +412,8 @@
         const requestExcludedCookies = $('#forwardAuthRequestExcludedCookies').val();
         const requestIncludeBody = $('#forwardAuthRequestIncludeBody').is(':checked');
         const useXOriginalHeaders = $('#forwardAuthRequestUseXOriginalHeaders').is(':checked');
+        const outpostPassthrough = $('#forwardAuthOutpostPassthrough').is(':checked');
+        const outpostPathPrefix = $('#forwardAuthOutpostPathPrefix').val();
 
         console.log(`Updating Forward Auth settings. Address: ${address}. Response Headers: ${responseHeaders}. Response Client Headers: ${responseClientHeaders}. Request Headers: ${requestHeaders}. Request Included Cookies: ${requestIncludedCookies}. Request Excluded Cookies: ${requestExcludedCookies}. Request Include Body: ${requestIncludeBody}. Use X-Original-* Headers: ${useXOriginalHeaders}.`);
 
@@ -406,6 +429,8 @@
                 requestExcludedCookies: requestExcludedCookies,
                 requestIncludeBody: requestIncludeBody,
                 useXOriginalHeaders: useXOriginalHeaders,
+                outpostPassthrough: outpostPassthrough,
+                outpostPathPrefix: outpostPathPrefix,
             },
             success: function(data) {
                 if (data.error !== undefined) {
```
