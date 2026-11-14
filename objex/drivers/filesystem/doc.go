// Package filesystem provides an objex driver that stores objects as files
// in a directory.
//
// The package owns the layout under one root directory: each bucket is a
// top-level directory and each object a file at its key's path. Every path
// is resolved through os.Root, so keys and symlinks can never reach outside
// the root. Writes go to a temporary file that is renamed into place, so
// readers never observe a partial object. Callers supply the root directory,
// which must already exist, and create buckets through the [Client].
//
// Objects carry only what the filesystem stores. The content type is derived
// from the key's extension, the ETag from the file's size and modification
// time, and user metadata is not supported: WithMetadata returns
// objex.ErrUnsupported. A key cannot name both an object and a prefix of
// other objects (for example "a" and "a/b"), because one path cannot be both
// a file and a directory; such writes return objex.ErrUnsupported.
//
// Presigned URLs are signed with HMAC-SHA256 when the Client is configured
// with [WithPresign]. The package never starts a server: mount
// [Client.Handler] in the application's own HTTP server at the configured
// base URL to serve presigned GET and PUT requests.
//
// Create a Client with [New] and release it with [Client.Close]; its zero
// value is not initialized. A Client and its [Bucket] handles are safe for
// concurrent use. Conditional writes are atomic within one process; when
// several processes share a root, IfNotExists stays atomic where the
// filesystem supports hard links, and IfMatch is best-effort.
package filesystem
