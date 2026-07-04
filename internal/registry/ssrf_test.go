package registry

import "testing"

func TestRegistryDialerControlBlocksDangerousDestinations(t *testing.T) {
	cases := []struct {
		name    string
		address string
		blocked bool
	}{
		{"loopback v4", "127.0.0.1:443", true},
		{"loopback v6", "[::1]:443", true},
		{"link-local / cloud metadata", "169.254.169.254:80", true},
		{"link-local multicast", "224.0.0.1:443", true},
		{"unspecified", "0.0.0.0:443", true},
		{"public IP (docker hub-ish)", "104.18.121.25:443", false},
		{"RFC1918 private — must stay allowed for self-hosted registries", "192.168.1.50:5000", false},
		{"RFC1918 10/8 — must stay allowed", "10.0.0.5:443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := registryDialer.Control("tcp", tc.address, nil)
			if tc.blocked && err == nil {
				t.Fatalf("expected %s to be blocked, but Control allowed it", tc.address)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("expected %s to be allowed, but Control blocked it: %v", tc.address, err)
			}
		})
	}
}
