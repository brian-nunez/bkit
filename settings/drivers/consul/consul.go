package consul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/bkit/settings"
)

// Sentinel errors. Match them with errors.Is.
var (
	// ErrNotFound reports that the key does not exist, or that no key
	// starts with the prefix.
	ErrNotFound = errors.New("consul: key not found")

	// ErrPermission reports that the token may not read the key.
	ErrPermission = errors.New("consul: permission denied")

	// ErrResponse reports any other unsuccessful or unreadable response.
	ErrResponse = errors.New("consul: unexpected response")

	// ErrInvalidAddress reports an address that is not an absolute http or
	// https URL.
	ErrInvalidAddress = errors.New("consul: invalid address")
)

// maxErrorText limits how much of an error response is kept in the error.
const maxErrorText = 512

// Option configures New.
type Option func(*Client)

// WithToken sets the ACL token sent in the X-Consul-Token header.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithDatacenter reads from the given datacenter instead of the agent's
// own.
func WithDatacenter(dc string) Option {
	return func(c *Client) { c.query.Set("dc", dc) }
}

// WithNamespace reads from the given namespace.
func WithNamespace(namespace string) Option {
	return func(c *Client) { c.query.Set("ns", namespace) }
}

// WithHTTPClient sets the HTTP client used for requests, for example to
// trust a private certificate authority. The default is a client that
// gives up on a request after 30 seconds, even when its context has no
// deadline.
// When a token is set and the client has no CheckRedirect of its own, the
// Client uses a copy of it that does not follow redirects to another host
// or from https to http. A client with its own CheckRedirect is used as
// is.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) { c.http = client }
}

// defaultTimeout bounds each request made with the default client. Tests
// shorten it.
var defaultTimeout = 30 * time.Second

// Client reads keys from one Consul agent.
type Client struct {
	base  *url.URL
	token string
	query url.Values
	http  *http.Client
}

// New returns a Client for the Consul agent at address, such as
// "http://localhost:8500". It makes no requests.
func New(address string, opts ...Option) (*Client, error) {
	base, err := url.Parse(address)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	c := &Client{base: base, query: url.Values{}, http: &http.Client{Timeout: defaultTimeout}}
	for _, opt := range opts {
		opt(c)
	}
	if c.token != "" && c.http.CheckRedirect == nil {
		hc := *c.http
		hc.CheckRedirect = checkRedirect
		c.http = &hc
	}
	return c, nil
}

// maxRedirects matches the limit of net/http's default redirect policy.
const maxRedirects = 10

// checkRedirect keeps the token on the original host. It stops at a
// redirect to another host or from https to http and returns the redirect
// response instead. Other redirects follow net/http's default policy.
func checkRedirect(req *http.Request, via []*http.Request) error {
	first := via[0].URL
	if req.URL.Host != first.Host || (first.Scheme == "https" && req.URL.Scheme != "https") {
		return http.ErrUseLastResponse
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return nil
}

// Key returns a settings.Source that reads one key whose value is a whole
// document and parses it with format. A parse failure matches
// settings.ErrParse. The source's name is "consul:" followed by key.
func (c *Client) Key(key string, format settings.Format) settings.Source {
	return &keySource{client: c, key: key, format: format}
}

// Prefix returns a settings.Source that reads every key under prefix and
// builds a tree from their paths: under the prefix "apps/web",
// apps/web/db/host becomes db.host. Values are strings, and settings
// getters convert them. Keys ending in "/" are folders and are skipped. The
// source's name is "consul:" followed by prefix.
func (c *Client) Prefix(prefix string) settings.Source {
	return &prefixSource{client: c, prefix: prefix}
}

type keySource struct {
	client *Client
	key    string
	format settings.Format
}

func (s *keySource) Name() string { return "consul:" + s.key }

func (s *keySource) Load(ctx context.Context) (map[string]any, error) {
	body, err := s.client.get(ctx, s.key, "raw")
	if err != nil {
		return nil, err
	}
	values, err := s.format.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", settings.ErrParse, err)
	}
	return values, nil
}

// kvEntry is one key in a recursive KV response.
type kvEntry struct {
	Key   string
	Value []byte
}

type prefixSource struct {
	client *Client
	prefix string
}

func (s *prefixSource) Name() string { return "consul:" + s.prefix }

func (s *prefixSource) Load(ctx context.Context) (map[string]any, error) {
	prefix := strings.TrimPrefix(s.prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	body, err := s.client.get(ctx, prefix, "recurse")
	if err != nil {
		return nil, err
	}
	var entries []kvEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponse, err)
	}
	slices.SortFunc(entries, func(a, b kvEntry) int { return strings.Compare(a.Key, b.Key) })

	tree := make(map[string]any)
	for _, e := range entries {
		rest := strings.TrimPrefix(e.Key, prefix)
		if rest == "" || strings.HasSuffix(rest, "/") {
			continue
		}
		parts := strings.Split(rest, "/")
		node := tree
		for _, part := range parts[:len(parts)-1] {
			next, ok := node[part].(map[string]any)
			if !ok {
				next = make(map[string]any)
				node[part] = next
			}
			node = next
		}
		node[parts[len(parts)-1]] = string(e.Value)
	}
	return tree, nil
}

// get reads key from the KV API with the given flag query parameter and
// returns the body of a successful response.
func (c *Client) get(ctx context.Context, key, flag string) ([]byte, error) {
	u := c.base.JoinPath("v1", "kv/"+key)
	q := url.Values{flag: {""}}
	maps.Copy(q, c.query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("X-Consul-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponse, err)
	}
	if resp.StatusCode == http.StatusOK {
		return body, nil
	}

	kind := ErrResponse
	switch resp.StatusCode {
	case http.StatusNotFound:
		kind = ErrNotFound
	case http.StatusForbidden:
		kind = ErrPermission
	}
	msg := "status " + strconv.Itoa(resp.StatusCode)
	if text := strings.TrimSpace(string(body[:min(len(body), maxErrorText)])); text != "" {
		msg += ": " + text
	}
	return nil, fmt.Errorf("%w: %s", kind, msg)
}
