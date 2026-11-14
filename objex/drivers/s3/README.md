# objex/drivers/s3

[objex](../..) driver for Amazon S3 and S3-compatible services such as MinIO
and Cloudflare R2, built on the AWS SDK for Go v2.

## Install

```shell
go get github.com/brian-nunez/bkit/objex/drivers/s3
```

```go
import "github.com/brian-nunez/bkit/objex/drivers/s3"
```

## Usage

### Amazon S3

Credentials and region come from the AWS SDK's default chain (environment
variables, shared config files, IAM roles) unless set explicitly:

```go
client, err := s3.New(ctx, s3.WithRegion("us-east-1"))
if err != nil {
	return err
}
var b objex.Bucket = client.Bucket("assets")
```

### S3-compatible services

```go
// MinIO or another self-hosted service.
client, err := s3.New(ctx,
	s3.WithEndpoint("http://localhost:9000"),
	s3.WithPathStyle(),
	s3.WithCredentials(accessKeyID, secretAccessKey, ""),
)

// Cloudflare R2.
client, err := s3.New(ctx,
	s3.WithEndpoint("https://"+accountID+".r2.cloudflarestorage.com"),
	s3.WithRegion("auto"),
	s3.WithCredentials(accessKeyID, secretAccessKey, ""),
)
```

With a custom endpoint, request checksums are sent only when an operation
requires them, which every S3-compatible service accepts.

## Options

| Option | Effect |
| --- | --- |
| `s3.WithRegion(region)` | Region; defaults to the SDK's configuration, then `us-east-1` |
| `s3.WithEndpoint(url)` | Send requests to an S3-compatible service |
| `s3.WithPathStyle()` | Address buckets as `host/bucket` instead of `bucket.host` |
| `s3.WithCredentials(id, secret, token)` | Static credentials instead of the default chain |
| `s3.WithAWSConfig(cfg)` | Use an existing `aws.Config` instead of loading one |
| `s3.WithPartSize(bytes)` | Multipart part size, 5 MiB to 5 GiB (default 8 MiB) |
| `s3.WithUploadConcurrency(n)` | Parts sent at once per upload (default 4) |

## Behavior

- **Uploads** stream: a body up to one part goes up in a single `PutObject`;
  larger or unknown-size bodies become a multipart upload, buffering one
  part per concurrent slot. Failed multipart uploads are aborted.
- **Conditional writes** (`IfNotExists`, `IfMatch`) use S3's native
  `If-None-Match` and `If-Match`, checked atomically by the server, including
  on multipart uploads.
- **Copy** is server-side and pinned to the source version it first sees;
  objects over 5 GiB are copied with multipart `UploadPartCopy`.
- **List** pages through `ListObjectsV2` lazily as you iterate.
- **Presigned URLs** are SigV4, valid up to seven days. `PresignPut` binds
  `WithContentType`, `WithMetadata`, `IfNotExists`, and `IfMatch` as signed
  headers listed in `PresignedRequest.Header`.
- **Errors** map S3 codes to objex sentinels (`NoSuchKey` →
  `ErrNotFound`, `PreconditionFailed` → `ErrPreconditionFailed`, and so on).

Caveats:

- `Put` returns `LastModified` as the time the write completed on the
  client, because S3 does not report it; `Stat`, `Get`, and `List` report the
  server's value.
- `Stat` on a key in a missing bucket returns `objex.ErrNotFound`: S3 answers
  `HEAD` requests without a code that tells the two apart. `Put`, `Get`,
  `Delete`, and `List` return `objex.ErrBucketNotFound`.
- Multipart ETags (`"<hash>-<parts>"`) are not content hashes.

## Capabilities

| Interface | Supported |
| --- | --- |
| `objex.Bucket` | Yes |
| `objex.Manager` | Yes (on `*s3.Client`) |
| `objex.Presigner` | Yes |
| `objex.Pinger` | Yes (`HeadBucket`) |
| `objex.Mover` | No; `objex.Move` copies, then deletes |

## Development

Unit tests need no service. They run the full objex contract suite against
an in-memory fake S3 client, plus driver-specific tests, and `coverage-check`
requires 100% statement coverage from them alone:

```shell
make -C objex/drivers/s3 check
```

The same contract suite, plus multipart tests, also runs against a real
S3-compatible service when `OBJEX_S3_ENDPOINT` is set; this is in addition to
the unit tests and is not needed for coverage:

```shell
docker compose -f objex/drivers/s3/docker-compose.yml up -d
OBJEX_S3_ENDPOINT=http://localhost:9000 make -C objex/drivers/s3 tests
docker compose -f objex/drivers/s3/docker-compose.yml down
```

`OBJEX_S3_ACCESS_KEY`, `OBJEX_S3_SECRET_KEY`, and `OBJEX_S3_REGION` override
the compose file's defaults.

## Releases

Released independently with tags of the form `objex/drivers/s3/vX.Y.Z`.
