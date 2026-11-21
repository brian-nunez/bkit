# settings

Load configuration from files, environment variables, and code, and merge it
into one tree.

Each source returns a tree of values. A format turns bytes into a tree, so
any source that reads bytes works with any format.

## Install

```shell
go get github.com/brian-nunez/bkit/settings
```

```go
import "github.com/brian-nunez/bkit/settings"
```

JSON is built in. Other formats are separate modules:

| Format | Module |
| --- | --- |
| YAML | `github.com/brian-nunez/bkit/settings/drivers/yaml` |
| TOML | `github.com/brian-nunez/bkit/settings/drivers/toml` |

## Usage

```go
cfg, err := settings.Load(ctx,
	settings.Map(map[string]any{"server": map[string]any{"addr": ":80"}}),
	settings.File("config.yaml", yaml.Format),
	settings.Env(settings.WithPrefix("APP_")),
)
if err != nil {
	return err
}

addr := cfg.GetString("server.addr", ":8080")
port, err := settings.Get[int](cfg, "server.port")

var app AppConfig
err = cfg.Decode(&app)
```

### Sources

| Source | Reads |
| --- | --- |
| `settings.File(path, format)` | A file on disk |
| `settings.FileFS(fsys, path, format)` | A file in an `fs.FS`, such as an `embed.FS` |
| `settings.Env(opts...)` | Environment variables |
| `settings.Map(values)` | Values from code, such as defaults |

A format is any type with `Parse([]byte) (map[string]any, error)`. A source
is any type with `Name() string` and
`Load(context.Context) (map[string]any, error)`.

### Keys

All keys are lowercased, and so are the paths passed to getters. A path
joins keys with dots. The JSON `{"name": {"first": "Ada"}}` is read as
`name.first`, and the same structure in YAML or TOML reads the same way.

`settings.Env()` reads every environment variable. A double underscore marks
a nested key:

| Variable | `Env()` | `Env(WithPrefix("APP_"))` | `Env(WithPrefixPreserve("APP_"))` |
| --- | --- | --- | --- |
| `NAME=Ada` | `name` | skipped | skipped |
| `APP_NAME__FIRST=Ada` | `app_name.first` | `name.first` | `app_name.first` |
| `APP_SERVER_PORT=80` | `app_server_port` | `server_port` | `app_server_port` |

Prefixes match in any case. Environment values are stored as strings, and
the getters convert them.

### Merging

`Load` applies sources from left to right, and the later source wins.

- Two maps merge key by key.
- Any other value replaces the earlier one. A map can replace a scalar, and
  a list replaces the whole earlier list.
- A null value sets the key to nil. The key still exists.

When one source has the same key twice, its format decides which value is
kept.

### Reading

| Call | When the value is missing or has the wrong type |
| --- | --- |
| `cfg.GetString`, `GetInt`, `GetBool`, `GetFloat`, `GetDuration` | Returns the default you pass |
| `settings.Get[T](cfg, path)` | Returns `ErrNotFound` or `ErrType` |
| `cfg.Get(path)` | Returns `nil, false`. A null value returns `nil, true` |
| `cfg.Decode(&target)` | Returns `ErrType` with the path that failed |

Strings convert to numbers, booleans, durations, and types that implement
`encoding.TextUnmarshaler`, so a value from the environment can be read as
an `int`. `Decode` matches struct fields by their `settings:"name"` tag or
by field name. Fields with no matching key keep their current value, which
lets a pre-filled struct hold defaults. A pre-filled map with string keys,
including `map[string]any`, keeps the entries the configuration does not
set, and nested maps merge key by key. An empty path reads the whole tree.

JSON integers keep full precision. An integer past the `int64` range reads
as a `uint64`, and an integer past the `uint64` range is kept as its decimal
string, which reads as a `string` or converts to a `float64`. An integer
value converts to a float only when the float holds it exactly, so
2^53 + 1 read as a `float64` returns `ErrType`.

## Cancellation

`Load` passes its context to every source and stops before the next source
once the context is done. The error matches `context.Canceled` or
`context.DeadlineExceeded` with `errors.Is`.

## Errors

Errors are `*settings.Error` values with the source name or key path. Match
them with `errors.Is`:

| Sentinel | Meaning |
| --- | --- |
| `settings.ErrNotFound` | No value exists at the path |
| `settings.ErrType` | The value cannot be converted to the requested type |
| `settings.ErrParse` | A format could not parse a document |

The source's original error stays in the chain. For example, a missing file
matches `fs.ErrNotExist`.

A conversion error names the key path and the value's type. It never
includes the value, so a secret read as the wrong type stays out of logs.
An `encoding.TextUnmarshaler` error stays in the chain, and its text is left
out of the message.

## Development

From the repository root:

```shell
make -C settings check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `settings/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/settings@vX.Y.Z
```
