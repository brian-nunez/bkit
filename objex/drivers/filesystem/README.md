# objex/drivers/filesystem

[objex](../..) driver that stores objects as files under a directory.

Each bucket is a top-level directory and each object a file at its key's
path. Every path is resolved through `os.Root`, so neither keys nor symlinks
can reach outside the root. Writes go to a temporary file that is renamed
into place, so readers never see a partial object.

## Install

```shell
go get github.com/brian-nunez/bkit/objex/drivers/filesystem
```

```go
import "github.com/brian-nunez/bkit/objex/drivers/filesystem"
```

## Usage

```go
client, err := filesystem.New("/var/lib/app/storage") // must already exist
if err != nil {
	return err
}
defer client.Close()

if err := client.CreateBucket(ctx, "assets"); err != nil && !errors.Is(err, objex.ErrBucketExists) {
	return err
}
var b objex.Bucket = client.Bucket("assets")
```

`assets/images/logo.png` is stored at
`/var/lib/app/storage/assets/images/logo.png`.

## What is stored

A file holds only its bytes, so:

| Field | Source |
| --- | --- |
| `ContentType` | The key's extension (`mime.TypeByExtension`), else `application/octet-stream`; `WithContentType` does not change it |
| `ETag` | The file's modification time and size |
| `LastModified` | The file's modification time |
| `Metadata` | Not supported: `WithMetadata` returns `objex.ErrUnsupported` |

A key cannot name both an object and a prefix of other objects (`a` and
`a/b`), because one path cannot be a file and a directory; such writes return
`objex.ErrUnsupported`. `List` walks the directory under the prefix and
sorts the results in memory.

## Presigned URLs

Configure presigning with the URL where your application serves the
driver's handler and a secret signing key of at least 32 bytes:

```go
client, err := filesystem.New(root,
	filesystem.WithPresign("https://example.com/files", signingKey),
)

mux.Handle("/files/", client.Handler())

req, err := client.Bucket("assets").PresignGet(ctx, "report.pdf", 15*time.Minute)
// req.URL: https://example.com/files/assets/report.pdf?X-Objex-Expires=...&X-Objex-Signature=...
```

The package never starts a server. `Handler` serves presigned requests in
your server:

- `GET` and `HEAD` stream the object, with `Range`, `If-Match`, and other
  conditional headers.
- `PUT` stores the request body atomically. A content type bound with
  `WithContentType` must be sent exactly; `IfNotExists` and `IfMatch` return
  `412` when they fail.
- Invalid, tampered, or expired signatures return `403`.

Mount the handler without stripping the URL prefix, and wrap it with
`http.MaxBytesHandler` to cap uploads. Every process serving the same root
must use the same signing key.

## Options

| Option | Effect |
| --- | --- |
| `filesystem.WithPresign(baseURL, key)` | Enables presigned URLs served by `Client.Handler` |
| `filesystem.WithClock(func() time.Time)` | Clock for presigned URL expiry |

## Capabilities

| Interface | Supported |
| --- | --- |
| `objex.Bucket` | Yes |
| `objex.Manager` | Yes (on `*filesystem.Client`) |
| `objex.Mover` | Yes, atomic rename |
| `objex.Pinger` | Yes |
| `objex.Presigner` | Yes, with `WithPresign` |

## Concurrency

`Client` and `Bucket` are safe for concurrent use. Conditional writes are
atomic within one process. When several processes share a root,
`IfNotExists` stays atomic where the filesystem supports hard links, and
`IfMatch` is best-effort.

## Development

```shell
make -C objex/drivers/filesystem check
```

`check` runs the objex contract suite and the driver's own tests, and
`coverage-check` requires 100% statement coverage.

## Releases

Released independently with tags of the form
`objex/drivers/filesystem/vX.Y.Z`.
