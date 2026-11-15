---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Implement backends as driver modules passed in explicitly

## Context and Problem Statement

Several bkit modules put one API in front of many backends, such as object
stores, configuration sources, SQL databases, and key-value stores. Each
backend needs its own client library, and some of those libraries are large.
An application should be able to switch backends by changing one place, and
it should only build the client libraries for the backends it uses. Where do
backend implementations live, and how does an application choose one?

## Decision Drivers

* The parent module stays light: standard library first, with no backend
  client libraries in its `go.mod`.
* Switching backends, or the client library behind one, changes one place in
  the application.
* No hidden global state or import side effects decide which backends exist.
* Backend configuration is checked by the compiler.
* Every backend behaves the same for the same calls.

## Considered Options

* Parent defines the interface, each backend is a nested driver module, and
  the application passes drivers in explicitly
* A global registry that drivers join from `init()`, selected by name
* Every backend inside the parent module
* Backends selected with build tags

## Decision Outcome

Chosen option: "Parent defines the interface, each backend is a nested driver
module, and the application passes drivers in explicitly", because it is the
only option that keeps the parent dependency-light, keeps backend choice
visible in application code, and lets each backend release on its own.

The parent module defines the interface and the sentinel errors, and can
ship a conformance suite for its drivers. Each backend is a module at
`<parent>/drivers/<driver>` that implements the interface and exposes typed
constructors, plus a `Driver()` option when the parent opens backends from a
URL. The application constructs the driver, or passes the drivers it
supports, as in `rdb.Open(ctx, url, sqlite.Driver(), postgres.Driver())`,
and the parent never imports its drivers.

### Consequences

* Good, because the parent module's `go.mod` carries no backend client
  libraries, and an application installs only the drivers it uses.
* Good, because replacing the client library behind a backend is a driver
  release, and application code does not change.
* Good, because driver configuration uses typed options and `Config` structs
  instead of untyped maps.
* Good, because a parent can ship a conformance suite that every driver runs,
  as objex and stash do with `objextest` and `stashtest`, so their drivers
  cannot drift apart.
* Bad, because there are more modules to version and release.
* Bad, because a driver change that needs an unreleased parent change ships
  in two steps: release the parent, then update the driver.
* Bad, because applications must pass drivers explicitly, which is one more
  line than an import with a side effect.
* Bad, because drivers for similar backends can repeat code, since modules
  cannot share unexported helpers.

### Confirmation

`scripts/check-components.sh` fails when a driver is listed before its parent
in `COMPONENTS`. Review checks that a parent `go.mod` adds no backend client
library and that no package registers drivers from `init()`. Drivers of a
parent with a conformance suite run it in their own tests, and
`coverage-check` requires 100% statement coverage without the backend
running.

## Pros and Cons of the Options

### Parent defines the interface, each backend is a nested driver module, and the application passes drivers in explicitly

* Good, because dependencies are scoped to the drivers an application uses.
* Good, because the backend choice is visible where the application
  constructs it.
* Good, because each driver is versioned and released independently.
* Neutral, because one application can still support several backends by
  passing several drivers, and the URL scheme or constructor picks one.
* Bad, because the number of modules grows with the number of backends.

### A global registry that drivers join from `init()`, selected by name

Drivers register themselves under a string name when imported, and the
parent looks them up at runtime.

* Good, because adding a backend can be a blank import.
* Bad, because which backends exist depends on import side effects anywhere
  in the program.
* Bad, because configuration crosses the registry as `any`, so type errors
  appear at runtime.
* Bad, because tests share global state.

### Every backend inside the parent module

* Good, because there is one module to release.
* Bad, because every application inherits every backend's client library.
* Bad, because a client library update forces a release of the whole module.

### Backends selected with build tags

* Good, because unused backends are left out of the binary.
* Bad, because the module's `go.mod` still lists every client library.
* Bad, because build tags are easy to forget and hard to test in every
  combination.

## More Information

The driver rules are in `.agents/skills/add-bkit-package/drivers.md`. This
decision builds on
[ADR-0001](0001-release-each-module-independently.md), which makes each
driver its own released module.
