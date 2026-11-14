package toml

import (
	"github.com/pelletier/go-toml/v2"
)

// Format is the TOML format, ready to pass to a settings source.
var Format = Parser{}

// Parser parses TOML documents. It implements settings.Format.
type Parser struct{}

// Parse returns the values of the TOML document in data.
func (Parser) Parse(data []byte) (map[string]any, error) {
	var values map[string]any
	if err := toml.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}
