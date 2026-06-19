# Virtual Directory "Skip Authentication" — Forward Auth for single-application mode

This document describes a fix/feature for **Forward Auth in single-application mode**, most notably
**Authentik per-application proxy providers**. It resolves the `ERR_TOO_MANY_REDIRECTS` loop reported
in [issue #895](https://github.com/tobychui/zoraxy/issues/895).

---

## TL;DR

Authentik (and other providers) using **per-application** forward auth need their callback path
(`/outpost.goauthentik.io/*`) to reach the auth outpost on the **protected app's own domain**.
Zoraxy evaluated forward auth **before** virtual-directory routing, so the OAuth callback was
intercepted by the forward-auth verify call and never completed → infinite redirect loop.

The fix adds an opt-in **"Skip Authentication"** flag to virtual directories. A request that matches
a skip-auth vdir is routed to its target **before** any authentication provider runs, so the callback
reaches the outpost and the login completes.

```
adguard.example.com           →  AdGuard            [Auth method = Forward Auth]
   └─ /outpost.goauthentik.io  →  Authentik outpost  [Skip Authentication = ON]
```

---

## The problem

When using Authentik in **single-application** ("per-app") mode behind Zoraxy:

1. You visit `https://app.example.com/`.
2. Zoraxy runs the forward-auth check → Authentik returns `302` → you are redirected to log in.
3. You authenticate successfully at Authentik.
4. Authentik redirects your browser back to `https://app.example.com/outpost.goauthentik.io/callback?code=…`.
5. **The login never completes** — the browser bounces back to the login page repeatedly and the
   browser eventually shows `ERR_TOO_MANY_REDIRECTS`.

Domain-level mode worked; single-application mode did not.

## Root cause

In `src/mod/dynamicproxy/Server.go`, the host-routing pipeline evaluated authentication **before**
virtual-directory routing:

```
access control → exploit detection → rate limit → CAPTCHA
   → Forward Auth        (ran here)
   → plugin router
   → virtual directory   (ran here — too late)
   → upstream
```

Authentik's proxy provider needs the browser to reach the **outpost** at `/outpost.goauthentik.io/*`
to complete the OAuth code exchange. The forward-auth *verify* endpoint (`/outpost.goauthentik.io/auth/traefik`)
only answers "is this request authenticated?" — it does **not** complete the exchange.

Because forward auth ran first, the callback request `…/outpost.goauthentik.io/callback?code=…` was
sent to the verify endpoint instead of being routed to the outpost. The verify call had no session
yet, so Authentik returned `302` again → back to step 2 → **infinite loop**.

**Why domain-level mode works but single-app does not:** in domain-level mode the cookie is set for
the whole parent domain (e.g. `.example.com`) and the callback lands on Authentik's *own* subdomain
(which has no forward auth in front of it). Every app subdomain then shares the cookie. In
single-application mode the callback lands on the *protected app's* domain, where Zoraxy's forward
auth intercepts it.

Forward auth in Zoraxy is configured **globally** (one auth server for all proxy hosts); the only
per-host forward-auth setting is which authentication method the host uses. So there was previously
**no** way to exclude a single path on a single host from the auth check. (Basic Auth already had
`BasicAuthExceptionRules`, but Forward Auth had no equivalent.)

## The fix

A new **`BypassAuth`** boolean on virtual directories. When set, requests whose path falls within the
vdir's matching path are routed to the vdir target **before** any authentication provider runs. This:

- routes `/outpost.goauthentik.io/*` to the Authentik outpost (the vdir target), and
- skips authentication for exactly that sub-path,

while the rest of the host stays protected by forward auth.

Because the check sits before the auth dispatcher, it transparently skips **all** auth methods
(Basic / Forward / OAuth2 / ZorxAuth), which is the desired behaviour for an auth callback path.

### Why a per-virtual-directory flag (and not the alternatives)

- **Why not move virtual directories before auth globally?** That would let *every* virtual directory
  bypass authentication — a silent security downgrade. The flag is strictly opt-in per vdir.
- **Why not a separate exception-path list (like Basic Auth)?** That would require two configuration
  objects kept in sync (a vdir to route the path + a list to exclude it). The flag puts both on the
  one object you already have to create.
- The routing half is unavoidable and standard: nginx needs a dedicated `location` block and Traefik
  needs a second router for `/outpost.goauthentik.io/` too. Zoraxy uses a virtual directory for this.

### Security: hardened path matching

Normal virtual-directory routing uses a loose string-prefix match. That is **not** safe for an
auth-bypass decision, so `BypassAuth` matching is hardened (`matchesAuthBypassPrefix` in
`src/mod/dynamicproxy/endpoints.go`):

- the request path is decoded and normalized with `path.Clean` (query string dropped);
- a match requires the cleaned path to **equal** the matching path **or** start with `matchingPath + "/"`.

This neutralizes:

| Attack | Result |
| --- | --- |
| `/outpost.goauthentik.io.evil/x` (boundary trick) | does **not** match → auth still runs |
| `/outpost.goauthentik.io/../admin` (traversal) | resolves to `/admin` → does **not** match → auth still runs |
| `/outpost.goauthentik.io/%2e%2e/admin` (encoded traversal) | decoded + resolved → does **not** match → auth still runs |

As an additional safety property, a bypassed request is only ever proxied to the vdir's **own
target** (the auth outpost), never the protected app backend.

---

## Changes

### Backend (Go)

| File | Change |
| --- | --- |
| `src/mod/dynamicproxy/typedef.go` | Add `BypassAuth bool` to `VirtualDirectoryEndpoint`. |
| `src/mod/dynamicproxy/endpoints.go` | Add `matchesAuthBypassPrefix()` (hardened, normalized matcher) and `GetAuthBypassVirtualDirectoryFromRequestURI()`. |
| `src/mod/dynamicproxy/Server.go` | Before the auth dispatcher, route any matching `BypassAuth` vdir and return. |
| `src/vdir.go` | Read the `bypassAuth` POST param in the add and edit handlers; persist it. |
| `src/mod/dynamicproxy/endpoints_test.go` | New unit test covering the matcher (legitimate paths, boundary tricks, path traversal, encoded traversal). |

### Frontend

| File | Change |
| --- | --- |
| `src/web/components/vdir.html` | Add a **Skip Authentication** checkbox to the New Virtual Directory form (Advanced Settings) and to the inline editor; send `bypassAuth`; show a shield icon for bypass vdirs in the list. |

### Backward compatibility

The new field defaults to `false`. Existing configurations deserialize with `BypassAuth = false`,
i.e. **no behaviour change** for anyone not using the feature. No config migration is required.

---

## How to configure (Authentik per-application example)

1. **Authentik** — create a *Proxy Provider* in **Forward auth (single application)** mode, set its
   *External host* to your app's public URL (e.g. `https://app.example.com`), and assign it to your
   (embedded) outpost.

2. **Zoraxy → the proxy host for `app.example.com`** — set the **authentication method** to
   **Forward Auth**.

3. **Zoraxy → global Forward Auth settings** — set the address to your outpost's verify endpoint, e.g.
   `http://<authentik-host>:9000/outpost.goauthentik.io/auth/traefik`, with **Use X-Original-\* Headers
   OFF**. (Use a direct host:port that is **not** itself proxied through Zoraxy.)

4. **Zoraxy → Virtual Directory** (on the `app.example.com` host) — add:
   - **Matching Path Prefix:** `/outpost.goauthentik.io`
   - **Target:** `<authentik-host>:9000/outpost.goauthentik.io/`  (tick *Require TLS* only if the outpost uses HTTPS)
   - **Advanced Settings → Skip Authentication: ON**

That's it. Visiting `https://app.example.com/` now redirects to Authentik, logs you in, and returns
to the app without looping.

> Tip: in the Virtual Directory list a bypass route is marked with a shield icon.

## Known limitations / caveats

- **CAPTCHA / exploit detection are not bypassed.** They run before authentication in the pipeline and
  are *not* skipped by this flag. If you enable CAPTCHA gating on the same host, add a CAPTCHA path
  exception for `/outpost.goauthentik.io` so the callback is not challenged. (Authentik forward auth is
  rarely combined with CAPTCHA on the same host.)
- This flag disables **all** authentication for the matched path. Only enable it for auth callback
  paths (or other paths you intentionally want public).

## Testing

- **Unit:** `go test ./mod/dynamicproxy/` — exercises `matchesAuthBypassPrefix` including the boundary
  and path-traversal cases above.
- **Build:** `go build ./...` from `src/`.
- **Manual (recommended for the test branch):** put a `traefik/whoami` container behind a single-app
  Authentik provider, configure as above, and confirm the full login round-trip completes with no
  loop; then confirm an unauthenticated request to a normal path still redirects to Authentik.

## Related

- Issue: [#895 — \[HELP\] Forward Auth with Authentik](https://github.com/tobychui/zoraxy/issues/895)
- Prior fix already merged: forward-auth response header direction + settings saving/defaults.

---

## Full diff

The complete set of changes is shown below (this documentation file itself is omitted to avoid
recursion). New file: `src/mod/dynamicproxy/endpoints_test.go`.

```diff
diff --git a/src/mod/dynamicproxy/Server.go b/src/mod/dynamicproxy/Server.go
index 9ecdd7d..82ac424 100644
--- a/src/mod/dynamicproxy/Server.go
+++ b/src/mod/dynamicproxy/Server.go
@@ -126,6 +126,16 @@ func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 			}
 		}
 
+		//Auth-bypass virtual directories (e.g. Authentik's /outpost.goauthentik.io callback path).
+		//These must be routed to their target BEFORE any authentication provider runs. Otherwise the
+		//forward-auth verify call intercepts the OAuth callback and the flow never completes, causing
+		//an infinite redirect loop in single-application mode (see issue #895). Matching is hardened
+		//against path traversal / boundary tricks (see matchesAuthBypassPrefix).
+		if bypassVdir := sep.GetAuthBypassVirtualDirectoryFromRequestURI(r.RequestURI); bypassVdir != nil {
+			h.vdirRequest(w, r, bypassVdir)
+			return
+		}
+
 		//Validate auth (basic auth or SSO auth)
 		respWritten := handleAuthProviderRouting(sep, w, r, h)
 		if respWritten {
diff --git a/src/mod/dynamicproxy/endpoints.go b/src/mod/dynamicproxy/endpoints.go
index 6ec09cc..2183be8 100644
--- a/src/mod/dynamicproxy/endpoints.go
+++ b/src/mod/dynamicproxy/endpoints.go
@@ -3,6 +3,8 @@ package dynamicproxy
 import (
 	"encoding/json"
 	"errors"
+	"net/url"
+	"path"
 	"strings"
 
 	"golang.org/x/text/cases"
@@ -86,6 +88,42 @@ func (ep *ProxyEndpoint) GetVirtualDirectoryHandlerFromRequestURI(requestURI str
 	return nil
 }
 
+// matchesAuthBypassPrefix reports whether the given request URI falls *within* the
+// matchingPath using normalized path boundaries. Unlike the loose prefix matching used
+// for normal vdir routing, this is hardened against:
+//   - boundary tricks    e.g. "/outpost.goauthentik.io.evil" must NOT match "/outpost.goauthentik.io"
+//   - path traversal     e.g. "/outpost.goauthentik.io/../admin" must NOT match (resolves to "/admin")
+//   - encoded traversal  e.g. "/outpost.goauthentik.io/%2e%2e/admin"
+//
+// It is intentionally used ONLY for the authentication-bypass decision, where a false
+// positive would skip auth on a path the operator did not intend to expose.
+func matchesAuthBypassPrefix(requestURI string, matchingPath string) bool {
+	requestPath := requestURI
+	if u, err := url.ParseRequestURI(requestURI); err == nil {
+		//Use the decoded path only, dropping any query string / fragment
+		requestPath = u.Path
+	}
+	requestPath = path.Clean("/" + requestPath)
+	cleanedMatch := path.Clean("/" + matchingPath)
+	return requestPath == cleanedMatch || strings.HasPrefix(requestPath, cleanedMatch+"/")
+}
+
+// GetAuthBypassVirtualDirectoryFromRequestURI returns the first enabled virtual directory
+// that has BypassAuth set and whose (normalized) matching path contains the request URI.
+// This is checked before any authentication provider runs so that auth callback paths
+// (e.g. Authentik's /outpost.goauthentik.io) can reach their upstream without being
+// intercepted by forward auth, which otherwise causes a redirect loop in single-application
+// mode (see issue #895).
+func (ep *ProxyEndpoint) GetAuthBypassVirtualDirectoryFromRequestURI(requestURI string) *VirtualDirectoryEndpoint {
+	for _, vdir := range ep.VirtualDirectories {
+		if vdir.BypassAuth && !vdir.Disabled && matchesAuthBypassPrefix(requestURI, vdir.MatchingPath) {
+			thisVdir := vdir
+			return thisVdir
+		}
+	}
+	return nil
+}
+
 // Get virtual directory handler by matching path (exact match required)
 func (ep *ProxyEndpoint) GetVirtualDirectoryRuleByMatchingPath(matchingPath string) *VirtualDirectoryEndpoint {
 	for _, vdir := range ep.VirtualDirectories {
diff --git a/src/mod/dynamicproxy/endpoints_test.go b/src/mod/dynamicproxy/endpoints_test.go
new file mode 100644
index 0000000..87eaa7e
--- /dev/null
+++ b/src/mod/dynamicproxy/endpoints_test.go
@@ -0,0 +1,47 @@
+package dynamicproxy
+
+import "testing"
+
+// TestMatchesAuthBypassPrefix verifies the hardened path matching used for the
+// authentication-bypass decision on virtual directories (issue #895). A false positive
+// here would skip authentication on a path the operator did not intend to expose, so the
+// boundary and path-traversal cases are security relevant.
+func TestMatchesAuthBypassPrefix(t *testing.T) {
+	const outpost = "/outpost.goauthentik.io"
+
+	cases := []struct {
+		name         string
+		requestURI   string
+		matchingPath string
+		want         bool
+	}{
+		// Legitimate matches
+		{"oauth callback with query", "/outpost.goauthentik.io/callback?code=abc&state=xyz", outpost, true},
+		{"start endpoint", "/outpost.goauthentik.io/start", outpost, true},
+		{"sign_out endpoint", "/outpost.goauthentik.io/sign_out", outpost, true},
+		{"exact match", "/outpost.goauthentik.io", outpost, true},
+		{"exact match with trailing slash on request", "/outpost.goauthentik.io/", outpost, true},
+		{"config prefix has trailing slash", "/outpost.goauthentik.io/callback", "/outpost.goauthentik.io/", true},
+
+		// Boundary tricks: a sibling path that merely shares the prefix string must NOT match
+		{"sibling path with dot suffix", "/outpost.goauthentik.io.evil/x", outpost, false},
+		{"prefix as substring", "/outpost.goauthentik.ioxyz", outpost, false},
+
+		// Path traversal: must resolve before matching so it cannot escape the directory
+		{"dot dot traversal", "/outpost.goauthentik.io/../admin", outpost, false},
+		{"encoded dot dot traversal", "/outpost.goauthentik.io/%2e%2e/admin", outpost, false},
+		{"nested dot dot traversal", "/outpost.goauthentik.io/foo/../../secret", outpost, false},
+
+		// Unrelated paths
+		{"unrelated path", "/api/v1/data", outpost, false},
+		{"root path", "/", outpost, false},
+	}
+
+	for _, tc := range cases {
+		t.Run(tc.name, func(t *testing.T) {
+			if got := matchesAuthBypassPrefix(tc.requestURI, tc.matchingPath); got != tc.want {
+				t.Errorf("matchesAuthBypassPrefix(%q, %q) = %v, want %v", tc.requestURI, tc.matchingPath, got, tc.want)
+			}
+		})
+	}
+}
diff --git a/src/mod/dynamicproxy/typedef.go b/src/mod/dynamicproxy/typedef.go
index 5bb8d79..ea9dca7 100644
--- a/src/mod/dynamicproxy/typedef.go
+++ b/src/mod/dynamicproxy/typedef.go
@@ -152,6 +152,7 @@ type VirtualDirectoryEndpoint struct {
 	RequireTLS          bool                 //Target domain require TLS
 	SkipCertValidations bool                 //Set to true to accept self signed certs
 	Disabled            bool                 //If the rule is enabled
+	BypassAuth          bool                 //Skip ALL authentication providers (Basic / Forward / OAuth2 / ZorxAuth) for requests matching this virtual directory. Opt-in, used for auth callback paths such as Authentik's /outpost.goauthentik.io
 	proxy               *dpcore.ReverseProxy `json:"-"`
 	parent              *ProxyEndpoint       `json:"-"`
 }
diff --git a/src/vdir.go b/src/vdir.go
index 9bcced4..45960d2 100644
--- a/src/vdir.go
+++ b/src/vdir.go
@@ -96,6 +96,13 @@ func ReverseProxyAddVdir(w http.ResponseWriter, r *http.Request) {
 
 	skipValid := (skipValidStr == "true")
 
+	bypassAuthStr, err := utils.PostPara(r, "bypassAuth")
+	if err != nil {
+		//Assume false
+		bypassAuthStr = "false"
+	}
+	bypassAuth := (bypassAuthStr == "true")
+
 	//Load the target proxy endpoint from runtime
 	var targetProxyEndpoint *dynamicproxy.ProxyEndpoint
 	if eptype == "root" {
@@ -130,6 +137,7 @@ func ReverseProxyAddVdir(w http.ResponseWriter, r *http.Request) {
 		Domain:              domain,
 		RequireTLS:          reqTLS,
 		SkipCertValidations: skipValid,
+		BypassAuth:          bypassAuth,
 	}
 
 	//Add Virtual Directory Rule to this Proxy Endpoint
@@ -237,6 +245,13 @@ func ReverseProxyEditVdir(w http.ResponseWriter, r *http.Request) {
 
 	skipValid := (skipValidStr == "true")
 
+	bypassAuthStr, err := utils.PostPara(r, "bypassAuth")
+	if err != nil {
+		//Assume false
+		bypassAuthStr = "false"
+	}
+	bypassAuth := (bypassAuthStr == "true")
+
 	var targetEndpoint *dynamicproxy.ProxyEndpoint
 	if eptype == "root" {
 		targetEndpoint = dynamicProxyRouter.Root
@@ -273,6 +288,7 @@ func ReverseProxyEditVdir(w http.ResponseWriter, r *http.Request) {
 		Domain:              domain,
 		RequireTLS:          reqTLS,
 		SkipCertValidations: skipValid,
+		BypassAuth:          bypassAuth,
 		Disabled:            false,
 	}
 
diff --git a/src/web/components/vdir.html b/src/web/components/vdir.html
index 817306c..258f58c 100644
--- a/src/web/components/vdir.html
+++ b/src/web/components/vdir.html
@@ -83,6 +83,12 @@
                                         <label>Ignore TLS/SSL Verification Error<br><small>For targets that is using self-signed, expired certificate (Not Recommended)</small></label>
                                     </div>
                                 </div>
+                                <div class="field">
+                                    <div class="ui checkbox">
+                                        <input type="checkbox" id="vdBypassAuth">
+                                        <label>Skip Authentication<br><small>Requests under this path bypass <b>all</b> authentication (Basic / Forward / OAuth2). Only enable for auth callback paths, e.g. Authentik per-app forward auth uses <code>/outpost.goauthentik.io</code>.</small></label>
+                                    </div>
+                                </div>
                             </div>
                         </div>
                     </div>
@@ -170,10 +176,15 @@
                             }
                         }
 
+                        var bypassAuthIcon = "";
+                        if (vdir.BypassAuth){
+                            bypassAuthIcon = ` <i class="grey shield alternate icon" title="Authentication bypassed for this path"></i>`;
+                        }
+
                         let payload = JSON.stringify(vdir).hexEncode();
 
                         $("#vdirList").append(`<tr vdirid="${vdir.MatchingPath.hexEncode()}" class="vdirEntry" payload="${payload}">
-                            <td data-label="" editable="false" >${vdir.MatchingPath}</td>
+                            <td data-label="" editable="false" >${vdir.MatchingPath}${bypassAuthIcon}</td>
                             <td data-label="" editable="true" datatype="domain">${vdir.Domain} ${tlsIcon}</td>
                             <td class="center aligned" editable="true" datatype="action" data-label="">
                                 <button class="ui circular mini basic icon button editBtn" onclick='editVdir("${vdir.MatchingPath}", "${endpoint}")'><i class="edit icon"></i></button>
@@ -229,6 +240,7 @@
         var targetDomain = $("#virtualDirectoryDomain").val().trim();
         var reqTLS = $("#vdReqTls")[0].checked;
         var skipTLSValidation = $("#vdSkipTLSValidation")[0].checked;
+        var bypassAuth = $("#vdBypassAuth")[0].checked;
 
         //Validate the input data
         if (matchingPath == ""){
@@ -267,6 +279,7 @@
                 "domain":targetDomain,
                 "reqTLS":reqTLS,
                 "skipValid":skipTLSValidation,
+                "bypassAuth":bypassAuth,
             },
             success: function(data){
                 if (data.error != undefined){
@@ -290,6 +303,7 @@
         $("#virtualDirectoryDomain").val("");
         $("#vdReqTls").parent().checkbox("set unchecked");
         $("#vdSkipTLSValidation").parent().checkbox("set unchecked");
+        $("#vdBypassAuth").parent().checkbox("set unchecked");
     }
 
     //Remove Vdir 
@@ -346,6 +360,12 @@
                     checkstate = "checked";
                 }
 
+                //Bypass authentication for this path
+                let bypassAuthState = "";
+                if (payload.BypassAuth){
+                    bypassAuthState = "checked";
+                }
+
                 input = `
                     <div class="ui mini fluid input">
                         <input type="text" class="Domain" value="${domain}">
@@ -359,6 +379,11 @@
                         <input type="checkbox" class="SkipCertValidations" ${checkstate}>
                         <label>Skip Verification<br>
                         <small>Check this if proxy target is using self signed certificates</small></label>
+                    </div><br>
+                    <div class="ui checkbox" style="margin-top: 0.4em;">
+                        <input type="checkbox" class="BypassAuth" ${bypassAuthState}>
+                        <label>Skip Authentication<br>
+                        <small>Bypass all authentication for this path (e.g. Authentik <code>/outpost.goauthentik.io</code>)</small></label>
                     </div>
                 `;
                 column.empty().append(input);
@@ -386,6 +411,7 @@
         let newDomain = $("#vdirList").find(".Domain").val();
         let requireTLS = $("#vdirList").find(".RequireTLS")[0].checked;
         let skipValidation = $("#vdirList").find(".SkipCertValidations")[0].checked;
+        let bypassAuth = $("#vdirList").find(".BypassAuth")[0].checked;
 
         //console.log(mathingPath, newDomain, requireTLS, skipValidation);
 
@@ -398,7 +424,8 @@
                 "domain":newDomain,
                 "path":path,
                 "reqTLS":requireTLS,
-                "skipValid": skipValidation
+                "skipValid": skipValidation,
+                "bypassAuth": bypassAuth
             },
             success: function(data){
                 if (data.error != undefined){
```
