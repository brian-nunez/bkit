// Package stash provides one API over key-value stores, so an application
// can switch between an in-process store, a SQL database, and a key-value
// server by changing a URL or one constructor.
//
// A [Store] holds byte values under string keys. Keys can expire, Set can
// write only when a key is missing, and Incr updates counters. Each of these
// is one atomic step in the backend, so racing callers cannot both win a
// lock or lose an increment. [Typed] stores Go values through a [Codec] such
// as [JSON], which runs before values reach the driver, so typed values work
// the same on every driver.
//
// Drivers live in their own modules under
// github.com/brian-nunez/bkit/stash/drivers and implement [Backend]. Pass
// them to [Open], or use a driver's own constructor. stash keeps no global
// registry.
//
// # Keys and namespaces
//
// A key is non-empty, valid UTF-8, and at most [MaxKeyLen] bytes including
// its namespace. [Store.Namespace] returns a store whose keys live under a
// prefix, and namespaces nest. [Store.Clear] empties one namespace and
// refuses to run on the root store.
//
// # Errors
//
// Failures are *[Error] values that match a sentinel such as [ErrNotFound]
// or [ErrExists] with [errors.Is]. Messages never contain the store URL, a
// key, or a value.
//
// # Concurrency
//
// A Store is safe for concurrent use, and its namespace stores share its
// backend. The zero value is not usable. Every call honors its context.
package stash
