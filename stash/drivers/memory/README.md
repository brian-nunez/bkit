# stash/drivers/memory

Keep [stash](../..) values in process memory.

## Install

```shell
go get github.com/brian-nunez/bkit/stash/drivers/memory
```

```go
import "github.com/brian-nunez/bkit/stash/drivers/memory"
```

## Usage

Open a URL with `stash.Open`:

```go
s, err := stash.Open(ctx, os.Getenv("STASH_URL"), memory.Driver())
```

```text
memory://
```

Or create a store directly:

```go
s := memory.New()
```

Every store starts empty and is separate from every other store, including
others opened with the same URL. Values are lost when the process exits.

## Behavior

- Values are copied in and out, so changing a slice after `Set` or `Get` does
  not change the stored value.
- Expired keys are never returned. Each write checks a few stored keys and
  removes the expired ones, so the memory they hold is freed with no
  background goroutine.
- `IfNotExists` and `Incr` hold one lock, so they are atomic within the
  process. Use a shared backend when several processes need the same keys.
- `Close` drops every value.

## Development

From the repository root:

```shell
make -C stash/drivers/memory check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The tests run the
`stashtest` conformance suite.

## Releases

Each release is tagged `stash/drivers/memory/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/stash/drivers/memory@vX.Y.Z
```
