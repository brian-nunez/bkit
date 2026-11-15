// Package memory provides a stash store that keeps values in process memory.
//
// Pass [Driver] to stash.Open to open memory:// URLs, or call [New]:
//
//	s, err := stash.Open(ctx, "memory://", memory.Driver())
//	s := memory.New()
//
// Every store is separate and empty when created, and its values are lost
// when the process exits. It suits local development, tests, and caches a
// single process owns.
//
// Expired keys are never returned. Each write also checks a few stored keys
// and removes the expired ones, so memory held by expired keys is freed
// without a background goroutine.
//
// A store is safe for concurrent use. Its atomic operations hold one lock,
// so they are atomic within the process.
package memory
