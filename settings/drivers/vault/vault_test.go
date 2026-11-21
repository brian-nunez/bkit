package vault_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/settings"
	"github.com/brian-nunez/bkit/settings/drivers/vault"
)

// fakeVault serves one canned response and records the last request.
type fakeVault struct {
	status int
	body   string
	last   *http.Request
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.last = r
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
}

func newFake(t *testing.T, status int, body string) (*fakeVault, string) {
	t.Helper()
	f := &fakeVault{status: status, body: body}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func mustNew(t *testing.T, address string, opts ...vault.Option) *vault.Client {
	t.Helper()
	c, err := vault.New(address, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewInvalidAddress(t *testing.T) {
	for _, addr := range []string{"", "vault:8200", "ftp://vault", "http://", "http://bad host"} {
		if _, err := vault.New(addr); !errors.Is(err, vault.ErrInvalidAddress) {
			t.Errorf("New(%q) err = %v, want ErrInvalidAddress", addr, err)
		}
	}
}

func TestKV2(t *testing.T) {
	f, addr := newFake(t, http.StatusOK, `{"data": {"data": {"DB": {"Host": "db.local"}, "port": 5432, "big": 9007199254740993}, "metadata": {"version": 3}}}`)
	c := mustNew(t, addr, vault.WithToken("s.token"), vault.WithNamespace("team"), vault.WithHTTPClient(&http.Client{}))
	src := c.KV2("secret", "apps/web")
	if got, want := src.Name(), "vault:secret/apps/web"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}

	cfg, err := settings.Load(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"db":   map[string]any{"host": "db.local"},
		"port": int64(5432),
		"big":  int64(9007199254740993),
	}
	if got := cfg.Data(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Data() = %#v, want %#v", got, want)
	}
	if f.last.URL.Path != "/v1/secret/data/apps/web" || f.last.URL.RawQuery != "" {
		t.Fatalf("request = %s", f.last.URL)
	}
	if f.last.Method != http.MethodGet {
		t.Fatalf("method = %s", f.last.Method)
	}
	if got := f.last.Header.Get("X-Vault-Token"); got != "s.token" {
		t.Fatalf("token header = %q", got)
	}
	if got := f.last.Header.Get("X-Vault-Namespace"); got != "team" {
		t.Fatalf("namespace header = %q", got)
	}
}

func TestKV2Version(t *testing.T) {
	f, addr := newFake(t, http.StatusOK, `{"data": {"data": {}}}`)
	if _, err := mustNew(t, addr).KV2("kv", "app", vault.WithVersion(2)).Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.last.URL.Path != "/v1/kv/data/app" || f.last.URL.RawQuery != "version=2" {
		t.Fatalf("request = %s", f.last.URL)
	}
	if _, ok := f.last.Header["X-Vault-Token"]; ok {
		t.Fatal("token header sent without WithToken")
	}
	if _, ok := f.last.Header["X-Vault-Namespace"]; ok {
		t.Fatal("namespace header sent without WithNamespace")
	}
}

func TestKV1(t *testing.T) {
	f, addr := newFake(t, http.StatusOK, `{"data": {"name": "web"}}`)
	src := mustNew(t, addr+"/prefix").KV1("kv", "app")
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]any{"name": "web"}) {
		t.Fatalf("Load = %#v", got)
	}
	if f.last.URL.Path != "/prefix/v1/kv/app" {
		t.Fatalf("request = %s", f.last.URL)
	}
	if src.Name() != "vault:kv/app" {
		t.Fatalf("Name() = %q", src.Name())
	}
}

func TestNullData(t *testing.T) {
	_, addr := newFake(t, http.StatusOK, `{"data": null}`)
	got, err := mustNew(t, addr).KV1("kv", "app").Load(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("Load = %#v, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		v1     bool
		want   error
		msg    string
	}{
		{"not found", http.StatusNotFound, `{"errors": []}`, false, vault.ErrNotFound, "vault: secret not found: status 404"},
		{"forbidden", http.StatusForbidden, `{"errors": ["permission denied"]}`, false, vault.ErrPermission, "vault: permission denied: status 403: permission denied"},
		{"server error", http.StatusInternalServerError, `{"errors": ["a", "b"]}`, false, vault.ErrResponse, "vault: unexpected response: status 500: a; b"},
		{"non-JSON error", http.StatusBadGateway, `<html>`, false, vault.ErrResponse, "vault: unexpected response: status 502"},
		{"invalid JSON", http.StatusOK, `{`, false, vault.ErrResponse, ""},
		{"data not an object", http.StatusOK, `{"data": "x"}`, false, vault.ErrResponse, ""},
		{"missing v2 data", http.StatusOK, `{"data": {}}`, false, vault.ErrResponse, ""},
		{"missing v1 data", http.StatusOK, `{}`, true, vault.ErrResponse, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, addr := newFake(t, tt.status, tt.body)
			c := mustNew(t, addr)
			src := c.KV2("secret", "app")
			if tt.v1 {
				src = c.KV1("kv", "app")
			}
			_, err := src.Load(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.msg != "" && err.Error() != tt.msg {
				t.Fatalf("err = %q, want %q", err, tt.msg)
			}
		})
	}
}

func TestCanceled(t *testing.T) {
	_, addr := newFake(t, http.StatusOK, `{"data": {"data": {}}}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mustNew(t, addr).KV2("secret", "app").Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNilContext(t *testing.T) {
	var ctx context.Context
	if _, err := mustNew(t, "http://127.0.0.1:1").KV2("secret", "app").Load(ctx); err == nil {
		t.Fatal("Load with a nil context succeeded")
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	if _, err := mustNew(t, addr).KV1("kv", "app").Load(context.Background()); err == nil {
		t.Fatal("Load against a closed server succeeded")
	}
}

// redirectTo serves a 307 redirect to target for every request.
func redirectTo(t *testing.T, target string, tls bool) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestRedirectToOtherHostKeepsToken(t *testing.T) {
	other, otherAddr := newFake(t, http.StatusOK, `{"data": {"data": {}}}`)
	srv := redirectTo(t, otherAddr+"/v1/secret/data/app", false)
	for name, opts := range map[string][]vault.Option{
		"default client": {vault.WithToken("s.token")},
		"caller client":  {vault.WithToken("s.token"), vault.WithHTTPClient(&http.Client{})},
	} {
		t.Run(name, func(t *testing.T) {
			other.last = nil
			_, err := mustNew(t, srv.URL, opts...).KV2("secret", "app").Load(context.Background())
			if !errors.Is(err, vault.ErrResponse) || err.Error() != "vault: unexpected response: status 307" {
				t.Fatalf("err = %v, want ErrResponse with status 307", err)
			}
			if other.last != nil {
				t.Fatalf("redirect to another host followed with token %q", other.last.Header.Get("X-Vault-Token"))
			}
		})
	}
	if http.DefaultClient.CheckRedirect != nil {
		t.Fatal("New changed http.DefaultClient")
	}
}

func TestRedirectToPlainHTTPKeepsToken(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+srv.Listener.Addr().String()+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	_, err := mustNew(t, srv.URL, vault.WithToken("s.token"), vault.WithHTTPClient(client)).KV2("secret", "app").Load(context.Background())
	if !errors.Is(err, vault.ErrResponse) || err.Error() != "vault: unexpected response: status 307" {
		t.Fatalf("err = %v, want ErrResponse with status 307", err)
	}
	if client.CheckRedirect != nil {
		t.Fatal("New changed the caller's client")
	}
}

func TestRedirectSameHost(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/v1/secret/data/app", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/v1/secret/data/app", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Vault-Token")
		_, _ = w.Write([]byte(`{"data": {"data": {"name": "web"}}}`))
	})
	mux.HandleFunc("/v1/secret/data/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := mustNew(t, srv.URL, vault.WithToken("s.token"))

	data, err := c.KV2("secret", "old").Load(context.Background())
	if err != nil || data["name"] != "web" || got != "s.token" {
		t.Fatalf("Load = %#v, %v, token %q", data, err, got)
	}
	if _, err := c.KV2("secret", "loop").Load(context.Background()); err == nil {
		t.Fatal("redirect loop succeeded")
	}
}

func TestRedirectWithoutToken(t *testing.T) {
	other, otherAddr := newFake(t, http.StatusOK, `{"data": {"data": {}}}`)
	srv := redirectTo(t, otherAddr+"/v1/secret/data/app", false)
	if _, err := mustNew(t, srv.URL).KV2("secret", "app").Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if other.last == nil {
		t.Fatal("redirect without a token not followed")
	}
}

func TestRedirectCallerPolicy(t *testing.T) {
	other, otherAddr := newFake(t, http.StatusOK, `{"data": {"data": {}}}`)
	srv := redirectTo(t, otherAddr+"/v1/secret/data/app", false)
	called := false
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return nil
	}}
	if _, err := mustNew(t, srv.URL, vault.WithToken("s.token"), vault.WithHTTPClient(client)).KV2("secret", "app").Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called || other.last == nil {
		t.Fatal("caller's CheckRedirect not used")
	}
}

func TestDefaultClientTimesOut(t *testing.T) {
	t.Cleanup(vault.SetDefaultTimeout(50 * time.Millisecond))
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	for name, opts := range map[string][]vault.Option{
		"no token":   nil,
		"with token": {vault.WithToken("vault-token")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mustNew(t, srv.URL, opts...).KV2("kv", "app").Load(context.Background())
			var timeout interface{ Timeout() bool }
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("err = %v, want a request timeout", err)
			}
		})
	}
}
