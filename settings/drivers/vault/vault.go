package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/brian-nunez/bkit/settings"
)

// Sentinel errors. Match them with errors.Is.
var (
	// ErrNotFound reports that the secret does not exist, or that its
	// current version is deleted or destroyed.
	ErrNotFound = errors.New("vault: secret not found")

	// ErrPermission reports that the token may not read the secret.
	ErrPermission = errors.New("vault: permission denied")

	// ErrResponse reports any other unsuccessful or unreadable response.
	ErrResponse = errors.New("vault: unexpected response")

	// ErrInvalidAddress reports an address that is not an absolute http or
	// https URL.
	ErrInvalidAddress = errors.New("vault: invalid address")
)

// Option configures New.
type Option func(*Client)

// WithToken sets the token sent in the X-Vault-Token header. Without it, no
// token is sent, which suits a Vault Agent or proxy that adds its own.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithNamespace sets the namespace sent in the X-Vault-Namespace header.
func WithNamespace(namespace string) Option {
	return func(c *Client) { c.namespace = namespace }
}

// WithHTTPClient sets the HTTP client used for requests, for example to
// trust a private certificate authority. The default is http.DefaultClient.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) { c.http = client }
}

// Client reads secrets from one Vault server.
type Client struct {
	base      *url.URL
	token     string
	namespace string
	http      *http.Client
}

// New returns a Client for the Vault server at address, such as
// "https://vault.example.com:8200". It makes no requests.
func New(address string, opts ...Option) (*Client, error) {
	base, err := url.Parse(address)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidAddress, address)
	}
	c := &Client{base: base, http: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// KVOption configures KV2.
type KVOption func(*kvSource)

// WithVersion reads the given version of a KV version 2 secret instead of
// the current one.
func WithVersion(version int) KVOption {
	return func(s *kvSource) { s.version = version }
}

// KV2 returns a settings.Source that reads the secret at path in the KV
// version 2 engine mounted at mount. The secret's data becomes the source's
// tree. The source's name is "vault:" followed by mount and path.
func (c *Client) KV2(mount, path string, opts ...KVOption) settings.Source {
	s := &kvSource{client: c, mount: mount, path: path, v2: true}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// KV1 returns a settings.Source that reads the secret at path in the KV
// version 1 engine mounted at mount. The secret's data becomes the source's
// tree. The source's name is "vault:" followed by mount and path.
func (c *Client) KV1(mount, path string) settings.Source {
	return &kvSource{client: c, mount: mount, path: path}
}

type kvSource struct {
	client  *Client
	mount   string
	path    string
	v2      bool
	version int
}

func (s *kvSource) Name() string {
	return "vault:" + s.mount + "/" + s.path
}

func (s *kvSource) Load(ctx context.Context) (map[string]any, error) {
	u := s.client.base.JoinPath("v1", s.mount, s.path)
	if s.v2 {
		u = s.client.base.JoinPath("v1", s.mount, "data", s.path)
		if s.version > 0 {
			u.RawQuery = url.Values{"version": {strconv.Itoa(s.version)}}.Encode()
		}
	}

	var raw json.RawMessage
	if s.v2 {
		var body struct {
			Data struct {
				Data json.RawMessage `json:"data"`
			} `json:"data"`
		}
		if err := s.client.get(ctx, u, &body); err != nil {
			return nil, err
		}
		raw = body.Data.Data
	} else {
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		if err := s.client.get(ctx, u, &body); err != nil {
			return nil, err
		}
		raw = body.Data
	}
	return decodeData(raw)
}

// decodeData decodes a secret's data, keeping integers exact.
func decodeData(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponse, err)
	}
	return data, nil
}

// get sends a GET request to u and decodes a successful JSON response into
// out.
func (c *Client) get(ctx context.Context, u *url.URL, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	if c.namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.namespace)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body struct {
			Errors []string `json:"errors"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		kind := ErrResponse
		switch resp.StatusCode {
		case http.StatusNotFound:
			kind = ErrNotFound
		case http.StatusForbidden:
			kind = ErrPermission
		}
		msg := "status " + strconv.Itoa(resp.StatusCode)
		if len(body.Errors) > 0 {
			msg += ": " + strings.Join(body.Errors, "; ")
		}
		return fmt.Errorf("%w: %s", kind, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %w", ErrResponse, err)
	}
	return nil
}
