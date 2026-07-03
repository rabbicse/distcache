package commands

import "github.com/rabbicse/distcache/internal/storage"

func Set(store storage.Store, args []string) string {

	key := args[0]
	value := args[1]

	store.Set(key, value)

	return "OK"
}
