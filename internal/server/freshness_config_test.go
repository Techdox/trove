package server

import "testing"

func TestLoadFreshnessConfigPrivateRegistryAllowlist(t *testing.T) {
	t.Setenv("TROVE_REGISTRY_AUTHS", `{"registry.lan:5000":{"username":"trove","password":"secret","auth_realm_hosts":["sso.lan"]}}`)
	t.Setenv("TROVE_REGISTRY_PRIVATE_HOSTS", "anonymous.lan:5000,  mirror.lan  ,")

	cfg := LoadFreshnessConfigFromEnv()
	cred, ok := cfg.Creds["registry.lan:5000"]
	if !ok || cred.Username != "trove" || len(cred.AuthRealmHosts) != 1 || cred.AuthRealmHosts[0] != "sso.lan" {
		t.Fatalf("registry credentials = %#v", cfg.Creds)
	}
	if len(cfg.AllowedPrivateRegistries) != 2 ||
		cfg.AllowedPrivateRegistries[0] != "anonymous.lan:5000" ||
		cfg.AllowedPrivateRegistries[1] != "mirror.lan" {
		t.Fatalf("private registry allowlist = %#v", cfg.AllowedPrivateRegistries)
	}
}
