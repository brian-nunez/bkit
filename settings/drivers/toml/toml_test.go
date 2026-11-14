package toml_test

import (
	"reflect"
	"testing"

	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/brian-nunez/bkit/settings/drivers/toml"
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
			doc:  "port = 8080\nratio = 0.5\ndebug = true\ntags = [\"a\", \"b\"]\nday = 2026-01-02\n\n[name]\nfirst = \"Ada\"\n",
			want: map[string]any{
				"name":  map[string]any{"first": "Ada"},
				"port":  int64(8080),
				"ratio": 0.5,
				"debug": true,
				"tags":  []any{"a", "b"},
				"day":   gotoml.LocalDate{Year: 2026, Month: 1, Day: 2},
			},
		},
		{name: "empty", doc: "", want: map[string]any{}},
		{name: "duplicate key", doc: "a = 1\na = 2\n", wantErr: true},
		{name: "invalid", doc: "a = \n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toml.Format.Parse([]byte(tt.doc))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse = %#v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) == 0 && len(tt.want) == 0 {
				return
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

var _ format = toml.Parser{}
