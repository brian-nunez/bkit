package vault_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/brian-nunez/bkit/settings"
	"github.com/brian-nunez/bkit/settings/drivers/vault"
)

// The integration tests run against a real Vault dev server when
// SETTINGS_VAULT_ADDR is set; docker-compose.yml in this directory starts
// one. SETTINGS_VAULT_TOKEN defaults to the compose file's root token.
func integration(t *testing.T) (addr, token string) {
	t.Helper()
	addr = os.Getenv("SETTINGS_VAULT_ADDR")
	if addr == "" {
		t.Skip("set SETTINGS_VAULT_ADDR to run integration tests (see docker-compose.yml)")
	}
	token = os.Getenv("SETTINGS_VAULT_TOKEN")
	if token == "" {
		token = "settingstest-root"
	}
	return addr, token
}

func uniqueName(t *testing.T) string {
	t.Helper()
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "settingstest-" + hex.EncodeToString(b[:])
}

// vaultRequest sends a raw API request used only to arrange test state.
func vaultRequest(t *testing.T, addr, token, method, path, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, addr+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
	}
}

func TestIntegrationKV2(t *testing.T) {
	addr, token := integration(t)
	path := uniqueName(t)
	vaultRequest(t, addr, token, http.MethodPost, "/v1/secret/data/"+path, `{"data": {"DB": {"Host": "one"}, "port": 5432}}`)
	vaultRequest(t, addr, token, http.MethodPost, "/v1/secret/data/"+path, `{"data": {"DB": {"Host": "two"}, "port": 5433}}`)
	t.Cleanup(func() { vaultRequest(t, addr, token, http.MethodDelete, "/v1/secret/metadata/"+path, "") })

	c, err := vault.New(addr, vault.WithToken(token))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := settings.Load(context.Background(), c.KV2("secret", path))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetString("db.host", ""); got != "two" {
		t.Fatalf("db.host = %q, want two", got)
	}
	if got := cfg.GetInt("port", 0); got != 5433 {
		t.Fatalf("port = %d, want 5433", got)
	}

	cfg, err = settings.Load(context.Background(), c.KV2("secret", path, vault.WithVersion(1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetString("db.host", ""); got != "one" {
		t.Fatalf("version 1 db.host = %q, want one", got)
	}

	if _, err := c.KV2("secret", uniqueName(t)).Load(context.Background()); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("missing secret: err = %v, want ErrNotFound", err)
	}

	bad, err := vault.New(addr, vault.WithToken("not-a-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.KV2("secret", path).Load(context.Background()); !errors.Is(err, vault.ErrPermission) {
		t.Fatalf("bad token: err = %v, want ErrPermission", err)
	}
}

func TestIntegrationKV1(t *testing.T) {
	addr, token := integration(t)
	mount := uniqueName(t)
	vaultRequest(t, addr, token, http.MethodPost, "/v1/sys/mounts/"+mount, `{"type": "kv", "options": {"version": "1"}}`)
	t.Cleanup(func() { vaultRequest(t, addr, token, http.MethodDelete, "/v1/sys/mounts/"+mount, "") })
	vaultRequest(t, addr, token, http.MethodPost, "/v1/"+mount+"/app", `{"Name": "web", "workers": 4}`)

	c, err := vault.New(addr, vault.WithToken(token))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := settings.Load(context.Background(), c.KV1(mount, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetString("name", ""); got != "web" {
		t.Fatalf("name = %q", got)
	}
	if got := cfg.GetInt("workers", 0); got != 4 {
		t.Fatalf("workers = %d", got)
	}
}
