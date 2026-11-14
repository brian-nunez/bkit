package settings_test

import (
	"context"
	"fmt"
	"os"
	"testing/fstest"
	"time"

	"github.com/brian-nunez/bkit/settings"
)

func ExampleLoad() {
	files := fstest.MapFS{
		"config.json": {Data: []byte(`{"server": {"addr": ":8080", "timeout": "5s"}}`)},
	}
	os.Setenv("EXAMPLE_SERVER__ADDR", ":9090")
	defer os.Unsetenv("EXAMPLE_SERVER__ADDR")

	cfg, err := settings.Load(context.Background(),
		settings.Map(map[string]any{"server": map[string]any{"addr": ":80", "workers": 4}}),
		settings.FileFS(files, "config.json", settings.JSON),
		settings.Env(settings.WithPrefix("EXAMPLE_")),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(cfg.GetString("server.addr", ""))
	fmt.Println(cfg.GetDuration("server.timeout", time.Second))
	fmt.Println(cfg.GetInt("server.workers", 1))
	fmt.Println(cfg.GetString("server.missing", "default"))
	// Output:
	// :9090
	// 5s
	// 4
	// default
}

func ExampleGet() {
	cfg, _ := settings.Load(context.Background(),
		settings.Map(map[string]any{"Server": map[string]any{"Port": "8080"}}),
	)

	port, err := settings.Get[int](cfg, "server.port")
	fmt.Println(port, err)

	_, err = settings.Get[int](cfg, "server.host")
	fmt.Println(err)
	// Output:
	// 8080 <nil>
	// settings: key server.host: not found
}

func ExampleConfig_Decode() {
	type Server struct {
		Addr    string        `settings:"addr"`
		Timeout time.Duration `settings:"timeout"`
	}
	type App struct {
		Name   string `settings:"name"`
		Server Server `settings:"server"`
	}

	cfg, _ := settings.Load(context.Background(),
		settings.Map(map[string]any{"name": "api", "server": map[string]any{"addr": ":8080"}}),
	)

	app := App{Server: Server{Timeout: 10 * time.Second}}
	if err := cfg.Decode(&app); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%s %s %s\n", app.Name, app.Server.Addr, app.Server.Timeout)
	// Output: api :8080 10s
}

func ExampleConfig_Get() {
	cfg, _ := settings.Load(context.Background(),
		settings.Map(map[string]any{"name": "set"}),
		settings.Map(map[string]any{"name": nil}),
	)

	v, ok := cfg.Get("name")
	fmt.Println(v, ok)
	fmt.Println(cfg.GetString("name", "fallback"))
	// Output:
	// <nil> true
	// fallback
}
