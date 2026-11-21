// Package settings loads configuration from any number of sources and merges
// it into one tree of values.
//
// A [Source] supplies part of the configuration: a file in any format, the
// environment, a map in code, or a remote store. A [Format] turns bytes into
// a tree, so any source that reads bytes accepts any format. The package
// ships [File], [FileFS], [Env], and [Map] sources and the [JSON] format;
// other formats and sources live in their own modules. Callers choose the
// sources, their order, and the keys their application reads.
//
// # Keys
//
// Every key is lowercased, and every path given to a getter is lowercased
// before lookup, so "Server.Port" and "server.port" name the same value.
// A path joins keys with dots. Nesting comes from the source: a JSON, YAML,
// or TOML object becomes a nested map, and [Env] nests on a double
// underscore, so NAME__FIRST becomes name.first.
//
// # Merging
//
// [Load] applies sources left to right. When two sources set the same key,
// the later value replaces the earlier one, except that two maps merge key
// by key. A null value is a real nil: it replaces the earlier value, and the
// key still exists. Keys that differ only in case within one source merge
// the same way, in sorted order of their original spelling. Within one
// source, duplicate keys follow that source's format.
//
// # Reading
//
// [Get] converts a value to any Go type and reports [ErrNotFound] or
// [ErrType]. The GetString, GetInt, GetBool, GetFloat, and GetDuration
// methods return a caller-supplied default instead of an error.
// [Config.Decode] fills a struct. Strings convert to numbers, booleans, and
// durations, so values from the environment read as typed values. A string
// converts to a float only when it is a finite decimal number such as
// -1.5e3: hex floats, NaN, and Inf are an [ErrType]. An
// integer converts to a float only when the float holds it exactly; any
// other integer is an [ErrType].
//
// # Concurrency and cancellation
//
// [Load] honors its context: it stops before the next source once the
// context is done and passes the context to every source. A [Config] is
// immutable after Load returns and safe for concurrent use. Its zero value
// is an empty configuration.
package settings
