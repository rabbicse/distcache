package storage

import "sync"

type KeyValueStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewKV() *KeyValueStore {
	return &KeyValueStore{
		data: map[string][]byte{},
	}
}

func (kv *KeyValueStore) Set(key, val []byte) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.data[string(key)] = []byte(val)
	return nil
}

func (kv *KeyValueStore) Get(key []byte) ([]byte, bool) {
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	val, ok := kv.data[string(key)]
	return val, ok
}
