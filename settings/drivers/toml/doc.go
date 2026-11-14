// Package toml provides the TOML format for settings.
//
// [Format] parses a TOML document into the tree of maps that settings
// sources return. Pass it to any source that reads bytes:
//
//	settings.File("config.toml", toml.Format)
//
// The package owns parsing only. settings owns key lowercasing, value
// normalization, and merging; callers own the documents. Parsing follows
// github.com/pelletier/go-toml/v2: tables become nested maps, and a
// duplicate key is an error, as the TOML specification requires. Dates and
// times keep their go-toml types.
//
// [Parser] has no state; its zero value is ready to use and safe for
// concurrent use.
package toml
