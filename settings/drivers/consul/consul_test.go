package consul_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/settings"
	"github.com/brian-nunez/bkit/settings/drivers/consul"
)

// fakeConsul serves one canned response and records the last request.
type fakeConsul struct {
	status int
	body   string
	last   *http.Request
}

func (f *fakeConsul) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.last = r
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
}

func newFake(t *testing.T, status int, body string) (*fakeConsul, string) {
	t.Helper()
	f := &fakeConsul{status: status, body: body}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func mustNew(t *testing.T, address string, opts ...consul.Option) *consul.Client {
	t.Helper()
	c, err := consul.New(address, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewInvalidAddress(t *testing.T) {
	for _, addr := range []string{"", "consul:8500", "ftp://consul", "http://", "http://bad host"} {
		if _, err := consul.New(addr); !errors.Is(err, consul.ErrInvalidAddress) {
			t.Errorf("New(%q) err = %v, want ErrInvalidAddress", addr, err)
		}
	}
}

func TestKey(t *testing.T) {
	f, addr := newFake(t, http.StatusOK, `{"Server": {"Port": 8080}}`)
	c := mustNew(t, addr, consul.WithToken("acl-token"), consul.WithDatacenter("dc2"), consul.WithNamespace("team"), consul.WithHTTPClient(&http.Client{}))
	src := c.Key("apps/web/config.json", settings.JSON)
	if got, want := src.Name(), "consul:apps/web/config.json"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	cfg, err := settings.Load(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetInt("server.port", 0); got != 8080 {
		t.Fatalf("server.port = %d", got)
	}
	if f.last.URL.Path != "/v1/kv/apps/web/config.json" {
		t.Fatalf("path = %s", f.last.URL.Path)
	}
	q := f.last.URL.Query()
	if _, ok := q["raw"]; !ok || q.Get("dc") != "dc2" || q.Get("ns") != "team" {
		t.Fatalf("query = %s", f.last.URL.RawQuery)
	}
	if got := f.last.Header.Get("X-Consul-Token"); got != "acl-token" {
		t.Fatalf("token header = %q", got)
	}
}

func TestKeyParseError(t *testing.T) {
	_, addr := newFake(t, http.StatusOK, `not json`)
	_, err := settings.Load(context.Background(), mustNew(t, addr).Key("cfg", settings.JSON))
	if !errors.Is(err, settings.ErrParse) {
		t.Fatalf("err = %v, want settings.ErrParse", err)
	}
}

func TestPrefix(t *testing.T) {
	body := `[
		{"Key": "apps/web/", "Value": null},
		{"Key": "apps/web/name", "Value": "d2Vi"},
		{"Key": "apps/web/db/", "Value": null},
		{"Key": "apps/web/DB/Host", "Value": "ZGIubG9jYWw="},
		{"Key": "apps/web/db/port", "Value": "NTQzMg=="},
		{"Key": "apps/web/empty", "Value": null},
		{"Key": "apps/web/cache", "Value": "b2Zm"},
		{"Key": "apps/web/cache/ttl", "Value": "MzBz"}
	]`
	f, addr := newFake(t, http.StatusOK, body)
	src := mustNew(t, addr).Prefix("apps/web")
	if src.Name() != "consul:apps/web" {
		t.Fatalf("Name() = %q", src.Name())
	}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":  "web",
		"DB":    map[string]any{"Host": "db.local"},
		"db":    map[string]any{"port": "5432"},
		"empty": "",
		"cache": map[string]any{"ttl": "30s"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %#v, want %#v", got, want)
	}
	if f.last.URL.Path != "/v1/kv/apps/web/" {
		t.Fatalf("path = %s", f.last.URL.Path)
	}
	if _, ok := f.last.URL.Query()["recurse"]; !ok {
		t.Fatalf("query = %s", f.last.URL.RawQuery)
	}
	if _, ok := f.last.Header["X-Consul-Token"]; ok {
		t.Fatal("token header sent without WithToken")
	}

	cfg, err := settings.Load(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetString("db.host", ""); got != "db.local" {
		t.Fatalf("db.host = %q", got)
	}
	if got := cfg.GetInt("db.port", 0); got != 5432 {
		t.Fatalf("db.port = %d", got)
	}
}

func TestPrefixPaths(t *testing.T) {
	tests := []struct{ prefix, path string }{
		{"", "/v1/kv/"},
		{"/", "/v1/kv/"},
		{"/apps/web/", "/v1/kv/apps/web/"},
	}
	for _, tt := range tests {
		f, addr := newFake(t, http.StatusOK, `[]`)
		if _, err := mustNew(t, addr+"/agent").Prefix(tt.prefix).Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimPrefix(f.last.URL.Path, "/agent"); got != tt.path {
			t.Errorf("Prefix(%q) path = %s, want %s", tt.prefix, got, tt.path)
		}
	}
}

func TestErrors(t *testing.T) {
	long := strings.Repeat("x", 600)
	tests := []struct {
		name   string
		status int
		body   string
		prefix bool
		want   error
		msg    string
	}{
		{"not found", http.StatusNotFound, "", false, consul.ErrNotFound, "consul: key not found: status 404"},
		{"forbidden", http.StatusForbidden, "Permission denied: token lacks key:read\n", false, consul.ErrPermission, "consul: permission denied: status 403: Permission denied: token lacks key:read"},
		{"server error", http.StatusInternalServerError, long, false, consul.ErrResponse, "consul: unexpected response: status 500: " + long[:512]},
		{"prefix not found", http.StatusNotFound, "", true, consul.ErrNotFound, ""},
		{"prefix invalid JSON", http.StatusOK, `{`, true, consul.ErrResponse, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, addr := newFake(t, tt.status, tt.body)
			c := mustNew(t, addr)
			src := c.Key("cfg", settings.JSON)
			if tt.prefix {
				src = c.Prefix("apps")
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

func TestTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("{"))
	}))
	t.Cleanup(srv.Close)
	if _, err := mustNew(t, srv.URL).Key("cfg", settings.JSON).Load(context.Background()); !errors.Is(err, consul.ErrResponse) {
		t.Fatalf("err = %v, want ErrResponse", err)
	}
}

func TestCanceled(t *testing.T) {
	_, addr := newFake(t, http.StatusOK, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mustNew(t, addr).Key("cfg", settings.JSON).Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNilContext(t *testing.T) {
	var ctx context.Context
	if _, err := mustNew(t, "http://127.0.0.1:1").Prefix("apps").Load(ctx); err == nil {
		t.Fatal("Load with a nil context succeeded")
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	if _, err := mustNew(t, addr).Prefix("apps").Load(context.Background()); err == nil {
		t.Fatal("Load against a closed server succeeded")
	}
}
