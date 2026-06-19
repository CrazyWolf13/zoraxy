package dynamicproxy

import "testing"

// TestMatchesAuthBypassPrefix verifies the hardened path matching used for the
// authentication-bypass decision on virtual directories (issue #895). A false positive
// here would skip authentication on a path the operator did not intend to expose, so the
// boundary and path-traversal cases are security relevant.
func TestMatchesAuthBypassPrefix(t *testing.T) {
	const outpost = "/outpost.goauthentik.io"

	cases := []struct {
		name         string
		requestURI   string
		matchingPath string
		want         bool
	}{
		// Legitimate matches
		{"oauth callback with query", "/outpost.goauthentik.io/callback?code=abc&state=xyz", outpost, true},
		{"start endpoint", "/outpost.goauthentik.io/start", outpost, true},
		{"sign_out endpoint", "/outpost.goauthentik.io/sign_out", outpost, true},
		{"exact match", "/outpost.goauthentik.io", outpost, true},
		{"exact match with trailing slash on request", "/outpost.goauthentik.io/", outpost, true},
		{"config prefix has trailing slash", "/outpost.goauthentik.io/callback", "/outpost.goauthentik.io/", true},

		// Boundary tricks: a sibling path that merely shares the prefix string must NOT match
		{"sibling path with dot suffix", "/outpost.goauthentik.io.evil/x", outpost, false},
		{"prefix as substring", "/outpost.goauthentik.ioxyz", outpost, false},

		// Path traversal: must resolve before matching so it cannot escape the directory
		{"dot dot traversal", "/outpost.goauthentik.io/../admin", outpost, false},
		{"encoded dot dot traversal", "/outpost.goauthentik.io/%2e%2e/admin", outpost, false},
		{"nested dot dot traversal", "/outpost.goauthentik.io/foo/../../secret", outpost, false},

		// Unrelated paths
		{"unrelated path", "/api/v1/data", outpost, false},
		{"root path", "/", outpost, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesAuthBypassPrefix(tc.requestURI, tc.matchingPath); got != tc.want {
				t.Errorf("matchesAuthBypassPrefix(%q, %q) = %v, want %v", tc.requestURI, tc.matchingPath, got, tc.want)
			}
		})
	}
}
