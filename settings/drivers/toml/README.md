# settings/drivers/toml

TOML format for [settings](../..).

## Install

```shell
go get github.com/brian-nunez/bkit/settings/drivers/toml
```

```go
import "github.com/brian-nunez/bkit/settings/drivers/toml"
```

## Usage

Pass `toml.Format` to any settings source that reads bytes:

```go
cfg, err := settings.Load(ctx,
	settings.File("config.toml", toml.Format),
)
```

## Behavior

Parsing uses `github.com/pelletier/go-toml/v2`.

- Tables become nested maps.
- A duplicate key is an error, as the TOML spec requires.
- Dates and times keep their go-toml types, such as `toml.LocalDate`.
- An empty document gives an empty tree.

## Errors

`Parse` returns the error from the TOML library. A settings source wraps it
so it matches `settings.ErrParse`.

## Development

From the repository root:

```shell
make -C settings/drivers/toml check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `settings/drivers/toml/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/settings/drivers/toml@vX.Y.Z
```
