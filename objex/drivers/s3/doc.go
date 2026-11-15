// Package s3 provides an objex driver for Amazon S3 and S3-compatible
// services such as MinIO and Cloudflare R2.
//
// The package owns translating objex calls into S3 API requests: streaming
// uploads (a single PutObject for small bodies, multipart above the part
// size), ranged and conditional reads, paginated listing, server-side copy
// (multipart above 5 GiB), presigned URLs, bucket management, and mapping S3
// error codes to objex sentinels. Callers supply credentials and endpoint
// settings, or rely on the AWS SDK's default credential chain and
// configuration (environment variables, shared config files, IAM roles).
//
// Conditional writes use S3's native If-Match and If-None-Match support, so
// they are atomic on the server, including for multipart uploads.
//
// Create a Client with [New]; its zero value is not initialized. A Client and
// its [Bucket] handles are safe for concurrent use.
//
// S3 does not report the modification time of a write in its response, so
// ObjectInfo.LastModified returned by Put is the time the write completed on
// the client; Stat, Get, and List report the server's value.
//
// Every operation on a bucket that does not exist returns
// objex.ErrBucketNotFound. S3 answers HEAD requests without an error code,
// so when Stat or Copy finds no object, the driver sends one HeadBucket
// request to tell a missing bucket from a missing key.
package s3
