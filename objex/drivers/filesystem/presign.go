package filesystem

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

// Query parameters carried by presigned URLs.
const (
	paramExpires   = "X-Objex-Expires"
	paramCondition = "X-Objex-Condition"
	paramSignature = "X-Objex-Signature"
)

// PresignGet returns a URL that downloads the object at key through
// Client.Handler until ttl elapses.
func (b *Bucket) PresignGet(ctx context.Context, key string, ttl time.Duration) (objex.PresignedRequest, error) {
	return b.presign(ctx, "presign get", http.MethodGet, key, ttl, objex.PutOptions{})
}

// PresignPut returns a URL that uploads to key through Client.Handler until
// ttl elapses. WithContentType binds the Content-Type header the client must
// send; IfNotExists and IfMatch are enforced when the upload arrives.
// WithMetadata returns objex.ErrUnsupported.
func (b *Bucket) PresignPut(ctx context.Context, key string, ttl time.Duration, opts ...objex.PutOption) (objex.PresignedRequest, error) {
	o, err := objex.ResolvePutOptions(opts...)
	if err != nil {
		return objex.PresignedRequest{}, objex.NewError("presign put", b.name, key, objex.ErrInvalidOption, err)
	}
	if len(o.Metadata) > 0 {
		return objex.PresignedRequest{}, objex.NewError("presign put", b.name, key, objex.ErrUnsupported, errors.New("the filesystem driver does not store user metadata"))
	}
	return b.presign(ctx, "presign put", http.MethodPut, key, ttl, o)
}

func (b *Bucket) presign(ctx context.Context, op, method, key string, ttl time.Duration, o objex.PutOptions) (objex.PresignedRequest, error) {
	if err := b.check(ctx, op, key); err != nil {
		return objex.PresignedRequest{}, err
	}
	if ttl <= 0 {
		return objex.PresignedRequest{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption, errors.New("ttl must be positive"))
	}
	c := b.client
	if c.baseURL == nil {
		return objex.PresignedRequest{}, objex.NewError(op, b.name, key, objex.ErrUnsupported, errors.New("presigned URLs require WithPresign"))
	}

	expires := c.now().Add(ttl).Unix()
	condition := conditionOf(o)
	signature := c.sign(method, b.name, key, expires, o.ContentType, condition)

	u := *c.baseURL
	u.Path = c.baseURL.Path + "/" + b.name + "/" + key
	u.RawPath = c.baseURL.Path + "/" + b.name + "/" + escapeKey(key)
	query := url.Values{}
	query.Set(paramExpires, strconv.FormatInt(expires, 10))
	if condition != "" {
		query.Set(paramCondition, condition)
	}
	query.Set(paramSignature, signature)
	u.RawQuery = query.Encode()

	header := http.Header{}
	if o.ContentType != "" {
		header.Set("Content-Type", o.ContentType)
	}
	return objex.PresignedRequest{
		Method:  method,
		URL:     u.String(),
		Header:  header,
		Expires: time.Unix(expires, 0).UTC(),
	}, nil
}

func conditionOf(o objex.PutOptions) string {
	switch {
	case o.IfNotExists:
		return "if-not-exists"
	case o.IfMatch != "":
		return "if-match:" + o.IfMatch
	default:
		return ""
	}
}

// sign returns the hex HMAC-SHA256 of the request's signed fields.
func (c *Client) sign(method, bucket, key string, expires int64, contentType, condition string) string {
	mac := hmac.New(sha256.New, c.signingKey)
	fmt.Fprintf(mac, "objex-presign-1\n%s\n%s\n%s\n%d\n%s\n%s", method, bucket, key, expires, contentType, condition)
	return hex.EncodeToString(mac.Sum(nil))
}

func escapeKey(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// Handler returns an http.Handler that serves presigned requests issued by
// the Client's buckets. Mount it in the application's HTTP server so that it
// receives the request paths under the URL given to WithPresign, without
// stripping the prefix:
//
//	mux.Handle("/files/", client.Handler()) // WithPresign("https://example.com/files", key)
//
// GET and HEAD stream the object with support for Range and conditional
// headers; PUT stores the request body atomically. Wrap the handler with
// http.MaxBytesHandler to cap upload size. Requests without a valid,
// unexpired signature receive 403. Without WithPresign every request
// receives 404.
func (c *Client) Handler() http.Handler {
	return http.HandlerFunc(c.serveHTTP)
}

func (c *Client) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if c.baseURL == nil {
		http.NotFound(w, r)
		return
	}

	method := r.Method
	switch method {
	case http.MethodGet, http.MethodHead:
		method = http.MethodGet
	case http.MethodPut:
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rest, ok := strings.CutPrefix(r.URL.Path, c.baseURL.Path+"/")
	bucketName, key, found := strings.Cut(rest, "/")
	if !ok || !found || objex.ValidateBucketName(bucketName) != nil || objex.ValidateKey(key) != nil {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	expires, err := strconv.ParseInt(query.Get(paramExpires), 10, 64)
	if err != nil {
		http.Error(w, "invalid or missing signature", http.StatusForbidden)
		return
	}
	condition := query.Get(paramCondition)
	signature := []byte(query.Get(paramSignature))
	valid := func(contentType string) bool {
		return hmac.Equal([]byte(c.sign(method, bucketName, key, expires, contentType, condition)), signature)
	}

	// A PUT signed with a content type must send exactly that type; a PUT
	// signed without one may send any type.
	contentType := ""
	if method == http.MethodPut {
		contentType = r.Header.Get("Content-Type")
	}
	if !valid(contentType) {
		if contentType == "" || !valid("") {
			http.Error(w, "invalid or missing signature", http.StatusForbidden)
			return
		}
	}
	if c.now().Unix() > expires {
		http.Error(w, "signature expired", http.StatusForbidden)
		return
	}

	b := c.Bucket(bucketName)
	if method == http.MethodGet {
		b.serveGet(w, r, key)
		return
	}
	b.servePut(w, r, key, contentType, condition)
}

func (b *Bucket) serveGet(w http.ResponseWriter, r *http.Request, key string) {
	f, stat, err := b.open("presigned get", key)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType(key))
	w.Header().Set("ETag", `"`+etag(stat)+`"`)
	http.ServeContent(w, r, "", stat.ModTime(), f)
}

func (b *Bucket) servePut(w http.ResponseWriter, r *http.Request, key, contentType, condition string) {
	var opts []objex.PutOption
	if contentType != "" {
		opts = append(opts, objex.WithContentType(contentType))
	}
	switch {
	case condition == "if-not-exists":
		opts = append(opts, objex.IfNotExists())
	case strings.HasPrefix(condition, "if-match:"):
		opts = append(opts, objex.IfMatch(strings.TrimPrefix(condition, "if-match:")))
	case condition != "":
		http.Error(w, "invalid or missing signature", http.StatusForbidden)
		return
	}
	if r.ContentLength >= 0 {
		opts = append(opts, objex.WithSize(r.ContentLength))
	}

	info, err := b.Put(r.Context(), key, r.Body, opts...)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+info.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

func writeError(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, objex.ErrNotFound), errors.Is(err, objex.ErrBucketNotFound), errors.Is(err, fs.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, objex.ErrPreconditionFailed):
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
	case errors.As(err, &maxBytes):
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, objex.ErrInvalidOption), errors.Is(err, objex.ErrInvalidKey):
		http.Error(w, "bad request", http.StatusBadRequest)
	case errors.Is(err, objex.ErrUnsupported):
		http.Error(w, "conflicts with an existing key", http.StatusConflict)
	case errors.Is(err, objex.ErrPermission):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
