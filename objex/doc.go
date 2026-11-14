// Package objex defines a backend-independent API for object storage.
//
// The package owns the contract every driver implements: the [Bucket]
// interface, optional capability interfaces ([Presigner], [Mover], [Pinger],
// [Manager]), call options, key rules, and the sentinel errors drivers map
// backend failures to. Drivers live in their own modules under
// github.com/brian-nunez/bkit/objex/drivers and are constructed directly by
// the application; objex keeps no global registry. Callers supply the driver,
// bucket names, keys, and object data.
//
// A Bucket is an immutable handle to one bucket and is safe for concurrent
// use. Code that depends only on Bucket can switch drivers by changing the
// one place the driver is constructed.
//
// Every operation honors its context. Failures are returned as *[Error]
// values that match a sentinel with [errors.Is] and unwrap to the backend's
// original error.
package objex
