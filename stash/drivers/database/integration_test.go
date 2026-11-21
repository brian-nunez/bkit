package database

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/rdb"
	"github.com/brian-nunez/bkit/rdb/drivers/mysql"
	"github.com/brian-nunez/bkit/rdb/drivers/postgres"
	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

// tableSeq names a fresh table for every conformance subtest.
var tableSeq atomic.Int64

// TestIntegration runs the conformance suite against the servers named by
// STASH_POSTGRES_URL and STASH_MYSQL_URL. See docker-compose.yml.
func TestIntegration(t *testing.T) {
	urls := map[string]string{
		"postgres": os.Getenv("STASH_POSTGRES_URL"),
		"mysql":    os.Getenv("STASH_MYSQL_URL"),
	}
	if urls["postgres"] == "" && urls["mysql"] == "" {
		t.Skip("set STASH_POSTGRES_URL or STASH_MYSQL_URL to run integration tests (see docker-compose.yml)")
	}
	for name, raw := range urls {
		if raw == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := rdb.Open(ctx, raw, postgres.Driver(), mysql.Driver(),
				rdb.WithMaxOpenConns(16), rdb.WithOpenRetry(30, time.Second))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			stashtest.Run(t, stashtest.Config{
				NewBackend: func(t *testing.T) stash.Backend {
					t.Helper()
					table := fmt.Sprintf("stash_test_%d_%d", os.Getpid(), tableSeq.Add(1))
					b, err := open(ctx, db, WithTable(table))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _, _ = db.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })
					return b
				},
			})
		})
	}
}
