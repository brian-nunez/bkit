package yaml_test

import (
	"reflect"
	"testing"

	"github.com/brian-nunez/bkit/settings/drivers/yaml"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "nested",
			doc:  "name:\n  first: Ada\nport: 8080\nratio: 0.5\ndebug: true\ntags: [a, b]\nnothing: null\n",
			want: map[string]any{
				"name":    map[string]any{"first": "Ada"},
				"port":    8080,
				"ratio":   0.5,
				"debug":   true,
				"tags":    []any{"a", "b"},
				"nothing": nil,
			},
		},
		{name: "empty", doc: "", want: nil},
		{name: "first document only", doc: "a: 1\n---\na: 2\n", want: map[string]any{"a": 1}},
		{name: "duplicate key", doc: "a: 1\na: 2\n", wantErr: true},
		{name: "top-level list", doc: "- a\n", wantErr: true},
		{name: "invalid", doc: "a: [\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := yaml.Format.Parse([]byte(tt.doc))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse = %#v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// format matches settings.Format without importing settings.
type format interface {
	Parse(data []byte) (map[string]any, error)
}

var _ format = yaml.Parser{}
