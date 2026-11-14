// Package memory provides an in-memory objex driver.
//
// The package owns bucket and object storage in process memory: contents,
// content types, user metadata, ETags, and modification times, with the same
// semantics as every other objex driver. Callers create buckets through the
// [Client] before writing to them. Nothing is persisted; data lives as long
// as the Client.
//
// Create a Client with [New]; its zero value is not initialized. A Client and
// the [Bucket] handles it returns are safe for concurrent use. Conditional
// writes and [Bucket.Move] are atomic.
//
// The driver suits tests and short-lived processes that need object storage
// without external services.
package memory
