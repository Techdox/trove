package registry

import "testing"

func TestParseImage(t *testing.T) {
	cases := []struct {
		ref                        string
		wantReg, wantRepo, wantTag string
	}{
		{"nginx", "registry-1.docker.io", "library/nginx", "latest"},
		{"nginx:alpine", "registry-1.docker.io", "library/nginx", "alpine"},
		{"gitea/gitea:1.22", "registry-1.docker.io", "gitea/gitea", "1.22"},
		{"ghcr.io/nick/app:v2", "ghcr.io", "nick/app", "v2"},
		{"gitea.techdox.nz/nick/test-base:latest", "gitea.techdox.nz", "nick/test-base", "latest"},
		{"localhost:5000/foo:dev", "localhost:5000", "foo", "dev"},
		{"registry.example.com:443/team/svc", "registry.example.com:443", "team/svc", "latest"},
		// A pinned digest is dropped in favour of resolving the tag.
		{"gitea/gitea:1.22@sha256:abc", "registry-1.docker.io", "gitea/gitea", "1.22"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			reg, repo, tag := ParseImage(tc.ref)
			if reg != tc.wantReg || repo != tc.wantRepo || tag != tc.wantTag {
				t.Fatalf("ParseImage(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.ref, reg, repo, tag, tc.wantReg, tc.wantRepo, tc.wantTag)
			}
		})
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, params := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if scheme != "Bearer" {
		t.Fatalf("scheme = %q", scheme)
	}
	if params["realm"] != "https://auth.docker.io/token" {
		t.Fatalf("realm = %q", params["realm"])
	}
	if params["service"] != "registry.docker.io" {
		t.Fatalf("service = %q", params["service"])
	}
	if params["scope"] != "repository:library/nginx:pull" {
		t.Fatalf("scope = %q", params["scope"])
	}
}
