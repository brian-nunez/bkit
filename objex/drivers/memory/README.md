# objex/drivers/memory

In-memory [objex](../..) driver.

It stores buckets and objects in process memory with the same semantics as
every other objex driver: content types, user metadata, ETags (MD5),
conditional writes, delimiter listings, and atomic moves. Nothing is
persisted. Use it in tests, or anywhere object storage is needed without an
external service.

## Install

```shell
go get github.com/brian-nunez/bkit/objex/drivers/memory
```

```go
import "github.com/brian-nunez/bkit/objex/drivers/memory"
```

## Usage

```go
client := memory.New()
if err := client.CreateBucket(ctx, "assets"); err != nil {
	return err
}
var b objex.Bucket = client.Bucket("assets")

_, err := b.Put(ctx, "hello.txt", strings.NewReader("hi"), objex.WithContentType("text/plain"))
```

Buckets must be created before use; operations on a missing bucket return
`objex.ErrBucketNotFound`.

## Options

| Option | Effect |
| --- | --- |
| `memory.WithClock(func() time.Time)` | Timestamps buckets and objects with a custom clock (UTC) |

## Capabilities

| Interface | Supported |
| --- | --- |
| `objex.Bucket` | Yes |
| `objex.Manager` | Yes (on `*memory.Client`) |
| `objex.Mover` | Yes, atomic |
| `objex.Pinger` | Yes |
| `objex.Presigner` | No; presigned URLs need an HTTP server |

`Client` and `Bucket` are safe for concurrent use. Memory use grows with the
total size of stored objects.

## Development

```shell
make -C objex/drivers/memory check
```

`check` runs the objex contract suite and the driver's own tests, and
`coverage-check` requires 100% statement coverage.

## Releases

Released independently with tags of the form
`objex/drivers/memory/vX.Y.Z`.
