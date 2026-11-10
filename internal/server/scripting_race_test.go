// Regression test: write scripts must hold write shard locks so plain readers
// cannot observe a keyspace map while a script mutates it.
package server_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestScripting_WriteScriptExcludesConcurrentReaders runs write scripts and
// GETs on the same keys in parallel; under -race an unlocked map write fails.
func TestScripting_WriteScriptExcludesConcurrentReaders(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	const rounds = 300
	var wg sync.WaitGroup
	run := func(f func(c *testutil.Client, i int)) {
		c, err := testutil.Dial(addr)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			for i := range rounds {
				f(c, i)
			}
		}()
	}
	script := "for i=1,20 do redis.call('SET', KEYS[1]..i, ARGV[1]) end return 1"
	run(func(c *testutil.Client, i int) { c.Do("EVAL", script, "1", "k", strconv.Itoa(i)) })
	run(func(c *testutil.Client, i int) {
		c.Do("FUNCTION", "LOAD", "REPLACE", "#!lua name=racelib\nredis.register_function('w', function(k, a) for i=1,20 do redis.call('SET', k[1]..i, a[1]) end return 1 end)")
		c.Do("FCALL", "w", "1", "k", strconv.Itoa(i))
	})
	for range 4 {
		run(func(c *testutil.Client, i int) { c.Do("GET", "k"+strconv.Itoa(i%20+1)) })
	}
	wg.Wait()
}
