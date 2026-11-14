package yaml

import (
	"go.yaml.in/yaml/v3"
)

// Format is the YAML format, ready to pass to a settings source.
var Format = Parser{}

// Parser parses YAML documents. It implements settings.Format.
type Parser struct{}

// Parse returns the values of the first YAML document in data.
func (Parser) Parse(data []byte) (map[string]any, error) {
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}
