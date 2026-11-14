package consul_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/settings"
	"github.com/brian-nunez/bkit/settings/drivers/consul"
)

// The integration tests run against a real Consul agent when
// SETTINGS_CONSUL_ADDR is set; docker-compose.yml in this directory starts
// one with ACLs enabled. SETTINGS_CONSUL_TOKEN defaults to the compose
// file's management token.
func integration(t *testing.T) (addr, token string) {
	t.Helper()
	addr = os.Getenv("SETTINGS_CONSUL_ADDR")
	if addr == "" {
		t.Skip("set SETTINGS_CONSUL_ADDR to run integration tests (see docker-compose.yml)")
	}
	token = os.Getenv("SETTINGS_CONSUL_TOKEN")
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

// consulRequest sends a raw API request used only to arrange test state.
func consulRequest(t *testing.T, addr, token, method, path, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, addr+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Consul-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
	}
}

func TestIntegration(t *testing.T) {
	addr, token := integration(t)
	root := uniqueName(t)
	put := func(key, value string) {
		consulRequest(t, addr, token, http.MethodPut, "/v1/kv/"+root+"/"+key, value)
	}
	put("config.json", `{"Server": {"Port": 8080}}`)
	put("app/name", "web")
	put("app/db/host", "db.local")
	put("app/db/port", "5432")
	put("app/folder/", "")
	t.Cleanup(func() { consulRequest(t, addr, token, http.MethodDelete, "/v1/kv/"+root+"?recurse", "") })

	c, err := consul.New(addr, consul.WithToken(token))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := settings.Load(context.Background(),
		c.Key(root+"/config.json", settings.JSON),
		c.Prefix(root+"/app"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("server.port", 0); got != 8080 {
		t.Fatalf("server.port = %d", got)
	}
	if got := cfg.GetString("name", ""); got != "web" {
		t.Fatalf("name = %q", got)
	}
	if got := cfg.GetString("db.host", ""); got != "db.local" {
		t.Fatalf("db.host = %q", got)
	}
	if got := cfg.GetInt("db.port", 0); got != 5432 {
		t.Fatalf("db.port = %d", got)
	}
	if _, ok := cfg.Get("folder"); ok {
		t.Fatal("folder key was loaded")
	}

	if _, err := c.Key(root+"/missing", settings.JSON).Load(context.Background()); !errors.Is(err, consul.ErrNotFound) {
		t.Fatalf("missing key: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Prefix(root + "/nothing").Load(context.Background()); !errors.Is(err, consul.ErrNotFound) {
		t.Fatalf("empty prefix: err = %v, want ErrNotFound", err)
	}

	anon, err := consul.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.Key(root+"/config.json", settings.JSON).Load(context.Background()); !errors.Is(err, consul.ErrPermission) {
		t.Fatalf("no token: err = %v, want ErrPermission", err)
	}
}
