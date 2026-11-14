# objex

A backend-independent API for object storage.

objex defines the contract: a small `Bucket` interface, optional capability
interfaces, call options, key rules, and sentinel errors. Drivers implement
it for specific backends and live in their own modules, so an application
installs only the driver it uses. Application code that depends on
`objex.Bucket` switches backends by changing the one place the driver is
constructed.

## Install

```shell
go get github.com/brian-nunez/bkit/objex
```

```go
import "github.com/brian-nunez/bkit/objex"
```

Then install one driver:

| Driver | Module | Backend |
| --- | --- | --- |
| s3 | `github.com/brian-nunez/bkit/objex/drivers/s3` | Amazon S3 and S3-compatible services |
| filesystem | `github.com/brian-nunez/bkit/objex/drivers/filesystem` | A directory on local disk |
| memory | `github.com/brian-nunez/bkit/objex/drivers/memory` | Process memory, for tests |

## Usage

Construct the driver in one place and pass `objex.Bucket` everywhere else:

```go
func newStorage(ctx context.Context) (objex.Bucket, error) {
	client, err := s3.New(ctx, s3.WithRegion("us-east-1"))
	if err != nil {
		return nil, err
	}
	return client.Bucket("assets"), nil
}
```

```go
func saveAvatar(ctx context.Context, b objex.Bucket, userID string, r io.Reader) error {
	_, err := b.Put(ctx, "avatars/"+userID+".png", r, objex.WithContentType("image/png"))
	return err
}
```

### Reading

```go
obj, err := b.Get(ctx, "avatars/42.png")
if errors.Is(err, objex.ErrNotFound) {
	// No avatar yet.
}
defer obj.Close()
// obj is an io.Reader; obj.Info describes the object.

head, err := b.Get(ctx, "videos/intro.mp4", objex.WithRange(0, 1<<20)) // first MiB
```

### Listing

`List` returns an iterator and fetches further pages as you range over it:

```go
for info, err := range b.List(ctx, objex.WithPrefix("avatars/")) {
	if err != nil {
		return err
	}
	fmt.Println(info.Key, info.Size, info.LastModified)
}
```

`WithDelimiter("/")` groups keys into directory-like entries with
`IsPrefix` set. `WithStartAfter` resumes after a key.

### Conditional writes

```go
// Create only.
_, err := b.Put(ctx, "locks/job-7", r, objex.IfNotExists())

// Replace only the version you read.
info, _ := b.Stat(ctx, "config.json")
_, err = b.Put(ctx, "config.json", r, objex.IfMatch(info.ETag))
if errors.Is(err, objex.ErrPreconditionFailed) {
	// Someone else changed it first.
}
```

### Copy, move, and existence

```go
info, err := b.Copy(ctx, "archive/report.pdf", "inbox/report.pdf")
info, err = objex.Move(ctx, b, "done/report.pdf", "inbox/report.pdf")
ok, err := objex.Exists(ctx, b, "done/report.pdf")
```

`objex.Move` uses the driver's native move when it has one and falls back to
copy-then-delete otherwise.

## Capabilities

Drivers implement these interfaces when their backend supports them. Check
with a type assertion when holding an `objex.Bucket`:

| Interface | Provides |
| --- | --- |
| `objex.Presigner` | Time-limited `GET` and `PUT` URLs |
| `objex.Mover` | Native, atomic moves (used by `objex.Move`) |
| `objex.Pinger` | `Ping` to verify the bucket is reachable |
| `objex.Manager` | Bucket creation, deletion, listing (on driver clients) |

```go
if p, ok := b.(objex.Presigner); ok {
	req, err := p.PresignGet(ctx, "avatars/42.png", 15*time.Minute)
	// Send req.Method to req.URL with req.Header.
}
```

## Keys

Every driver accepts the same keys, checked by `objex.ValidateKey`: valid
UTF-8, 1 to 1024 bytes, `/`-separated segments that are non-empty and not
`.` or `..`, with no backslashes or control characters. Bucket names follow
`objex.ValidateBucketName`: 3 to 63 lowercase letters, digits, hyphens, and
dots.

## Metadata

`ObjectInfo` reports `Key`, `Size`, `ContentType`, `ETag`, `LastModified`,
and user `Metadata`. ETags are opaque: compare them only for equality, with
ETags from the same driver. Entries from `List` always carry `Key`, `Size`,
`ETag`, and `LastModified`; use `Stat` for the content type and metadata.

`WithMetadata` keys are stored in lowercase and may contain only ASCII
letters, digits, and hyphens; values must be printable ASCII. Drivers that
cannot store metadata reject it with `objex.ErrUnsupported` rather than
dropping it.

## Errors

Failures are `*objex.Error` values carrying the operation, bucket, key, a
sentinel, and the backend's original error:

```go
var objexErr *objex.Error
if errors.As(err, &objexErr) {
	log.Printf("%s %s/%s failed: %v", objexErr.Op, objexErr.Bucket, objexErr.Key, objexErr.Err)
}
```

| Sentinel | Meaning |
| --- | --- |
| `ErrNotFound` | The object does not exist |
| `ErrBucketNotFound` | The bucket does not exist |
| `ErrBucketExists` | A bucket being created already exists |
| `ErrBucketNotEmpty` | A bucket being deleted still holds objects |
| `ErrPermission` | The credentials do not allow the operation |
| `ErrPreconditionFailed` | `IfMatch` or `IfNotExists` did not hold |
| `ErrInvalidKey` | The key fails `ValidateKey` |
| `ErrInvalidBucketName` | The name fails `ValidateBucketName` |
| `ErrInvalidRange` | The range is malformed or starts past the end |
| `ErrInvalidOption` | An option is malformed or conflicts with another |
| `ErrUnsupported` | The driver cannot provide the operation or option |

Context cancellation and deadlines surface unchanged: `errors.Is(err,
context.Canceled)` works on every operation.

## Writing a driver

Implement `objex.Bucket` (and any capability interfaces), resolve options
with `objex.ResolvePutOptions` and friends, validate keys with
`objex.ValidateKey`, and return failures with `objex.NewError`. Then run the
conformance suite from the driver's tests:

```go
func TestConformance(t *testing.T) {
	objextest.Run(t, objextest.Config{
		NewBucket:   newEmptyBucket,
		Metadata:    true,
		ContentType: true,
	})
}
```

`objextest.RunManager` and `objextest.RunPresigner` cover bucket management
and presigned URLs.

## Development

From the repository root:

```shell
make -C objex check
```

## Releases

Released independently with tags of the form `objex/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/objex@vX.Y.Z
```
