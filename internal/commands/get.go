package commands

import "github.com/rabbicse/distcache/internal/storage"

func Get(store storage.Store, args []string) any {

	key := args[0]

	val, ok := store.Get(key)

	if !ok {
		return nil
	}

	return val
}
