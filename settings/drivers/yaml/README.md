# settings/drivers/yaml

YAML format for [settings](../..).

## Install

```shell
go get github.com/brian-nunez/bkit/settings/drivers/yaml
```

```go
import "github.com/brian-nunez/bkit/settings/drivers/yaml"
```

## Usage

Pass `yaml.Format` to any settings source that reads bytes:

```go
cfg, err := settings.Load(ctx,
	settings.File("config.yaml", yaml.Format),
)
```

## Behavior

Parsing uses `go.yaml.in/yaml/v3`.

- Only the first document in a stream is read.
- The top level must be a mapping.
- A duplicate key in one mapping is an error.
- An empty document gives an empty tree.

## Errors

`Parse` returns the error from the YAML library. A settings source wraps it
so it matches `settings.ErrParse`.

## Development

From the repository root:

```shell
make -C settings/drivers/yaml check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `settings/drivers/yaml/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/settings/drivers/yaml@vX.Y.Z
```
