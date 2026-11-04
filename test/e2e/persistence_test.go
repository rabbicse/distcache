// Persistence tests: snapshot and AOF restarts through the public client API.
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/redis/go-redis/v9"
)

// writeMixed writes n keys across string, hash, list, set and zset types;
// every tenth string key carries a 1 h TTL.
func writeMixed(t *testing.T, rc *redis.Client, n int) {
	t.Helper()
	ctx := context.Background()
	pipe := rc.Pipeline()
	for i := range n {
		k := fmt.Sprintf("k:%d", i)
		switch i % 5 {
		case 0:
			if i%10 == 0 {
				pipe.Set(ctx, k, fmt.Sprint("v", i), time.Hour)
			} else {
				pipe.Set(ctx, k, fmt.Sprint("v", i), 0)
			}
		case 1:
			pipe.HSet(ctx, k, "f", i, "g", "x")
		case 2:
			pipe.RPush(ctx, k, "a", i, "c")
		case 3:
			pipe.SAdd(ctx, k, "a", i)
		case 4:
			pipe.ZAdd(ctx, k, redis.Z{Score: float64(i), Member: "m"})
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("write mixed: %v", err)
	}
}

// checkMixed asserts the n keys written by writeMixed are intact and the
// database holds size keys in total.
func checkMixed(t *testing.T, rc *redis.Client, n, size int) {
	t.Helper()
	ctx := context.Background()
	if got, err := rc.DBSize(ctx).Result(); err != nil || got != int64(size) {
		t.Fatalf("DBSIZE = %d, %v; want %d", got, err, size)
	}
	for i := range n {
		k := fmt.Sprintf("k:%d", i)
		var got, want string
		switch i % 5 {
		case 0:
			got, want = rc.Get(ctx, k).Val(), fmt.Sprint("v", i)
			ttl := rc.TTL(ctx, k).Val()
			if i%10 == 0 && (ttl < time.Hour-time.Second*2 || ttl > time.Hour) {
				t.Errorf("TTL %s = %v, want ~1h (±1s)", k, ttl)
			}
			if i%10 != 0 && ttl != -1 {
				t.Errorf("TTL %s = %v, want none", k, ttl)
			}
		case 1:
			got, want = fmt.Sprint(rc.HGetAll(ctx, k).Val()), fmt.Sprintf("map[f:%d g:x]", i)
		case 2:
			got, want = fmt.Sprint(rc.LRange(ctx, k, 0, -1).Val()), fmt.Sprintf("[a %d c]", i)
		case 3:
			got, want = fmt.Sprint(rc.SCard(ctx, k).Val()), "2"
		case 4:
			got, want = fmt.Sprint(rc.ZScore(ctx, k, "m").Val()), fmt.Sprint(i)
		}
		if got != want {
			t.Fatalf("%s: got %q, want %q", k, got, want)
		}
	}
}

func TestSnapshotSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, rc := startNode(t, dir, nil)
	writeMixed(t, rc, 1000)
	if err := rc.Save(context.Background()).Err(); err != nil {
		t.Fatalf("SAVE: %v", err)
	}
	if err := s.Shutdown(false); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_, rc2 := startNode(t, dir, nil)
	checkMixed(t, rc2, 1000, 1000)
}

func TestAOFSurvivesRestart(t *testing.T) {
	tests := []struct {
		name     string
		fsync    string
		rewrite  bool
		truncate bool
	}{
		{name: "always", fsync: "always"},
		{name: "everysec", fsync: "everysec"},
		{name: "no", fsync: "no"},
		{name: "after_bgrewriteaof", fsync: "everysec", rewrite: true},
		{name: "truncated_tail", fsync: "always", truncate: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			aof := func(c *config.Config) {
				c.AppendOnly.Store(true)
				c.AppendFsync = tc.fsync
			}
			s, rc := startNode(t, dir, aof)
			writeMixed(t, rc, 500)
			if tc.rewrite {
				ctx := context.Background()
				if err := rc.BgRewriteAOF(ctx).Err(); err != nil {
					t.Fatalf("BGREWRITEAOF: %v", err)
				}
				waitFor(t, 10*time.Second, "AOF rewrite to finish", func() bool {
					return infoField(t, rc, "persistence", "aof_rewrite_in_progress") == "0" &&
						infoField(t, rc, "persistence", "aof_rewrite_scheduled") != "1"
				})
				// Writes after the rewrite must land in the new file too.
				if err := rc.Set(ctx, "post-rewrite", "1", 0).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Shutdown(false); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if tc.truncate {
				path := filepath.Join(dir, "appendonly.aof")
				fileSize(t, path)
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString("*3\r\n$3\r\nSE"); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, rc2 := startNode(t, dir, aof)
			want := 500
			if tc.rewrite {
				want++
				if got := rc2.Get(context.Background(), "post-rewrite").Val(); got != "1" {
					t.Fatalf("post-rewrite: got %q, want 1", got)
				}
			}
			checkMixed(t, rc2, 500, want)
		})
	}
}
