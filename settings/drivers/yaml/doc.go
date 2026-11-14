// Package yaml provides the YAML format for settings.
//
// [Format] parses a YAML document into the tree of maps that settings
// sources return. Pass it to any source that reads bytes:
//
//	settings.File("config.yaml", yaml.Format)
//
// The package owns parsing only. settings owns key lowercasing, value
// normalization, and merging; callers own the documents. Parsing follows
// go.yaml.in/yaml/v3: only the first document of a stream is read, the top
// level must be a mapping, and a duplicate key in one mapping is an error.
// An empty document parses to an empty tree.
//
// [Parser] has no state; its zero value is ready to use and safe for
// concurrent use.
package yaml
