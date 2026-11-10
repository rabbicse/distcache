// This file holds the console's static sample-command catalogue and the
// seed script behind the "Load sample data" button. Every key is prefixed
// "sample:" so the seed can detect an earlier load and refuse to overwrite.
package web

// sampleKeyPrefix namespaces every key the samples and the seed touch.
const sampleKeyPrefix = "sample:"

// sample is one runnable example shown in the sample panel.
type sample struct {
	Cmd  string
	Desc string
}

// sampleGroup is a titled list of examples for one data type.
type sampleGroup struct {
	Name     string
	Examples []sample
}

// sampleGroups is read-only after init. Examples assume the seed script ran.
var sampleGroups = []sampleGroup{
	{"Strings", []sample{
		{"GET sample:string", "Read the seeded string."},
		{"SET sample:greeting \"hello world\" EX 300", "Write a string that expires in five minutes."},
		{"INCR sample:counter", "Atomically increment a counter."},
		{"APPEND sample:string \"!\"", "Append to a string."},
		{"STRLEN sample:string", "Length of a string in bytes."},
	}},
	{"Hashes", []sample{
		{"HGETALL sample:hash", "All fields and values."},
		{"HSET sample:hash city Dhaka", "Set one field."},
		{"HGET sample:hash name", "Read one field."},
		{"HINCRBY sample:hash visits 1", "Increment a numeric field."},
	}},
	{"Lists", []sample{
		{"LRANGE sample:list 0 -1", "Every element, head to tail."},
		{"RPUSH sample:list d", "Append to the tail."},
		{"LPOP sample:list", "Remove and return the head."},
		{"LLEN sample:list", "Number of elements."},
	}},
	{"Sets", []sample{
		{"SMEMBERS sample:set", "All members."},
		{"SADD sample:set date", "Add a member."},
		{"SISMEMBER sample:set apple", "Membership test."},
		{"SCARD sample:set", "Number of members."},
	}},
	{"Sorted sets", []sample{
		{"ZRANGE sample:zset 0 -1 WITHSCORES", "Members by ascending score."},
		{"ZADD sample:zset 4 dave", "Add a scored member."},
		{"ZRANK sample:zset bob", "Rank of a member."},
		{"ZINCRBY sample:zset 10 alice", "Raise a member's score."},
	}},
	{"Streams", []sample{
		{"XRANGE sample:stream - +", "Every entry."},
		{"XADD sample:stream * event click", "Append an entry with an automatic ID."},
		{"XLEN sample:stream", "Number of entries."},
	}},
	{"Geo", []sample{
		{"GEOPOS sample:geo Dhaka", "Coordinates of a member."},
		{"GEODIST sample:geo Dhaka Chittagong km", "Distance between two members."},
		{"GEOSEARCH sample:geo FROMLONLAT 90.4 23.8 BYRADIUS 300 km ASC", "Members within a radius."},
	}},
	{"HyperLogLog", []sample{
		{"PFCOUNT sample:hll", "Approximate distinct count."},
		{"PFADD sample:hll user4 user5", "Add elements."},
		{"PFADD sample:hll2 a b c", "Create a second estimator."},
		{"PFMERGE sample:hll3 sample:hll sample:hll2", "Merge estimators into a new key."},
	}},
	{"Pub/Sub", []sample{
		{"PUBLISH sample:channel hello", "Publish a message; the reply is the receiver count."},
		{"PUBSUB CHANNELS", "Channels with at least one subscriber."},
		{"PUBSUB NUMSUB sample:channel", "Subscriber count for a channel."},
	}},
	{"Server", []sample{
		{"PING", "Check the connection."},
		{"DBSIZE", "Number of keys in the current database."},
		{"TIME", "Server time as seconds and microseconds."},
		{"TYPE sample:hash", "Data type stored at a key."},
		{"TTL sample:greeting", "Seconds until a key expires."},
	}},
}

// seedScript creates one key of each type. Commands run in order.
var seedScript = [][]string{
	{"SET", "sample:string", "hello"},
	{"HSET", "sample:hash", "name", "Ada", "visits", "1"},
	{"RPUSH", "sample:list", "a", "b", "c"},
	{"SADD", "sample:set", "apple", "banana", "cherry"},
	{"ZADD", "sample:zset", "1", "alice", "2", "bob", "3", "carol"},
	{"XADD", "sample:stream", "*", "event", "start"},
	{"GEOADD", "sample:geo", "90.4125", "23.8103", "Dhaka", "91.7832", "22.3569", "Chittagong"},
	{"PFADD", "sample:hll", "user1", "user2", "user3"},
}
