package forward

import "testing"

// TestRequestPathWithinPrefix verifies the hardened path matching used to decide the
// outpost passthrough. The boundary and path-traversal cases are security relevant: a false
// positive would skip authentication on a path the operator did not intend to expose.
func TestRequestPathWithinPrefix(t *testing.T) {
	const outpost = "/outpost.goauthentik.io"

	cases := []struct {
		name   string
		uri    string
		prefix string
		want   bool
	}{
		{"callback with query", "/outpost.goauthentik.io/callback?code=abc&state=xyz", outpost, true},
		{"exact", "/outpost.goauthentik.io", outpost, true},
		{"trailing slash", "/outpost.goauthentik.io/", outpost, true},
		{"start endpoint", "/outpost.goauthentik.io/start", outpost, true},
		{"sign_out endpoint", "/outpost.goauthentik.io/sign_out", outpost, true},
		{"boundary sibling", "/outpost.goauthentik.io.evil/x", outpost, false},
		{"prefix as substring", "/outpost.goauthentik.ioxyz", outpost, false},
		{"dot dot traversal", "/outpost.goauthentik.io/../admin", outpost, false},
		{"encoded traversal", "/outpost.goauthentik.io/%2e%2e/admin", outpost, false},
		{"nested traversal", "/outpost.goauthentik.io/foo/../../secret", outpost, false},
		{"unrelated path", "/api/v1/data", outpost, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestPathWithinPrefix(tc.uri, tc.prefix); got != tc.want {
				t.Errorf("requestPathWithinPrefix(%q, %q) = %v, want %v", tc.uri, tc.prefix, got, tc.want)
			}
		})
	}
}

// TestDeriveOutpostBaseURL verifies that the outpost upstream is correctly derived from the
// forward auth verify address for the common Authentik endpoints, and that it fails closed
// when the prefix is absent or inputs are empty.
func TestDeriveOutpostBaseURL(t *testing.T) {
	const outpost = "/outpost.goauthentik.io"

	cases := []struct {
		name    string
		address string
		prefix  string
		wantURL string
		wantOK  bool
	}{
		{"traefik endpoint", "http://10.10.20.213:9000/outpost.goauthentik.io/auth/traefik", outpost, "http://10.10.20.213:9000/outpost.goauthentik.io", true},
		{"nginx endpoint", "http://authentik:9000/outpost.goauthentik.io/auth/nginx", outpost, "http://authentik:9000/outpost.goauthentik.io", true},
		{"https endpoint", "https://auth.example.com/outpost.goauthentik.io/auth/traefik", outpost, "https://auth.example.com/outpost.goauthentik.io", true},
		{"prefix absent", "http://authentik:9000/api/verify", outpost, "", false},
		{"empty prefix", "http://authentik:9000/outpost.goauthentik.io/auth/traefik", "", "", false},
		{"empty address", "", outpost, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, gotOK := deriveOutpostBaseURL(tc.address, tc.prefix)
			if gotURL != tc.wantURL || gotOK != tc.wantOK {
				t.Errorf("deriveOutpostBaseURL(%q, %q) = (%q, %v), want (%q, %v)", tc.address, tc.prefix, gotURL, gotOK, tc.wantURL, tc.wantOK)
			}
		})
	}
}
