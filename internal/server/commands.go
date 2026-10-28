package server

import (
	"sort"
	"strings"

	"github.com/rabbicse/distcache/internal/config"
)

type regSpec struct {
	name        string
	arity       int
	flags       string
	first, last int
	step        int
	group       string
	h           Handler
	keys        KeysFunc
}

func (s *Server) registerCommands() {
	specs := []regSpec{
		// strings
		{"get", 2, "readonly fast", 1, 1, 1, "string", cmdGet, nil},
		{"set", -3, "write denyoom", 1, 1, 1, "string", cmdSet, nil},
		{"setnx", 3, "write denyoom fast", 1, 1, 1, "string", cmdSetNX, nil},
		{"setex", 4, "write denyoom", 1, 1, 1, "string", cmdSetEX, nil},
		{"psetex", 4, "write denyoom", 1, 1, 1, "string", cmdPSetEX, nil},
		{"getset", 3, "write denyoom fast", 1, 1, 1, "string", cmdGetSet, nil},
		{"getdel", 2, "write fast", 1, 1, 1, "string", cmdGetDel, nil},
		{"getex", -2, "write fast", 1, 1, 1, "string", cmdGetEx, nil},
		{"mget", -2, "readonly fast", 1, -1, 1, "string", cmdMGet, nil},
		{"mset", -3, "write denyoom", 1, -1, 2, "string", cmdMSet, nil},
		{"msetnx", -3, "write denyoom", 1, -1, 2, "string", cmdMSetNX, nil},
		{"strlen", 2, "readonly fast", 1, 1, 1, "string", cmdStrlen, nil},
		{"append", 3, "write denyoom fast", 1, 1, 1, "string", cmdAppend, nil},
		{"getrange", 4, "readonly", 1, 1, 1, "string", cmdGetRange, nil},
		{"substr", 4, "readonly", 1, 1, 1, "string", cmdGetRange, nil},
		{"setrange", 4, "write denyoom", 1, 1, 1, "string", cmdSetRange, nil},
		{"incr", 2, "write denyoom fast", 1, 1, 1, "string", cmdIncr, nil},
		{"decr", 2, "write denyoom fast", 1, 1, 1, "string", cmdDecr, nil},
		{"incrby", 3, "write denyoom fast", 1, 1, 1, "string", cmdIncrBy, nil},
		{"decrby", 3, "write denyoom fast", 1, 1, 1, "string", cmdDecrBy, nil},
		{"incrbyfloat", 3, "write denyoom fast", 1, 1, 1, "string", cmdIncrByFloat, nil},
		{"lcs", -3, "readonly", 1, 2, 1, "string", cmdLCS, nil},
		// bitmaps
		{"setbit", 4, "write denyoom", 1, 1, 1, "bitmap", cmdSetBit, nil},
		{"getbit", 3, "readonly fast", 1, 1, 1, "bitmap", cmdGetBit, nil},
		{"bitcount", -2, "readonly", 1, 1, 1, "bitmap", cmdBitCount, nil},
		{"bitpos", -3, "readonly", 1, 1, 1, "bitmap", cmdBitPos, nil},
		{"bitop", -4, "write denyoom", 2, -1, 1, "bitmap", cmdBitOp, nil},
		{"bitfield", -2, "write denyoom", 1, 1, 1, "bitmap", cmdBitField, nil},
		{"bitfield_ro", -2, "readonly fast", 1, 1, 1, "bitmap", cmdBitFieldRO, nil},
		// generic keyspace
		{"del", -2, "write", 1, -1, 1, "keyspace", cmdDel, nil},
		{"unlink", -2, "write fast", 1, -1, 1, "keyspace", cmdDel, nil},
		{"exists", -2, "readonly fast", 1, -1, 1, "keyspace", cmdExists, nil},
		{"touch", -2, "readonly fast", 1, -1, 1, "keyspace", cmdTouch, nil},
		{"type", 2, "readonly fast", 1, 1, 1, "keyspace", cmdType, nil},
		{"expire", -3, "write fast", 1, 1, 1, "keyspace", cmdExpire, nil},
		{"pexpire", -3, "write fast", 1, 1, 1, "keyspace", cmdPExpire, nil},
		{"expireat", -3, "write fast", 1, 1, 1, "keyspace", cmdExpireAt, nil},
		{"pexpireat", -3, "write fast", 1, 1, 1, "keyspace", cmdPExpireAt, nil},
		{"ttl", 2, "readonly fast", 1, 1, 1, "keyspace", cmdTTL, nil},
		{"pttl", 2, "readonly fast", 1, 1, 1, "keyspace", cmdPTTL, nil},
		{"expiretime", 2, "readonly fast", 1, 1, 1, "keyspace", cmdExpireTime, nil},
		{"pexpiretime", 2, "readonly fast", 1, 1, 1, "keyspace", cmdPExpireTime, nil},
		{"persist", 2, "write fast", 1, 1, 1, "keyspace", cmdPersist, nil},
		{"keys", 2, "readonly global", 0, 0, 0, "keyspace", cmdKeys, nil},
		{"scan", -2, "readonly global", 0, 0, 0, "keyspace", cmdScan, nil},
		{"randomkey", 1, "readonly global", 0, 0, 0, "keyspace", cmdRandomKey, nil},
		{"rename", 3, "write", 1, 2, 1, "keyspace", cmdRename, nil},
		{"renamenx", 3, "write fast", 1, 2, 1, "keyspace", cmdRenameNX, nil},
		{"move", 3, "write fast", 1, 1, 1, "keyspace", cmdMove, nil},
		{"copy", -3, "write denyoom", 1, 2, 1, "keyspace", cmdCopy, nil},
		{"object", -2, "readonly", 2, 2, 1, "keyspace", cmdObject, nil},
		{"dump", 2, "readonly", 1, 1, 1, "keyspace", cmdDump, nil},
		{"restore", -4, "write denyoom", 1, 1, 1, "keyspace", cmdRestore, nil},
		{"sort", -2, "write denyoom", 1, 1, 1, "keyspace", cmdSort, sortKeys},
		{"sort_ro", -2, "readonly", 1, 1, 1, "keyspace", cmdSortRO, sortKeys},
		{"dbsize", 1, "readonly fast", 0, 0, 0, "keyspace", cmdDBSize, nil},
		{"flushdb", -1, "write lockall", 0, 0, 0, "keyspace", cmdFlushDB, nil},
		{"flushall", -1, "write lockall", 0, 0, 0, "keyspace", cmdFlushAll, nil},
		{"swapdb", 3, "write fast lockall", 0, 0, 0, "keyspace", cmdSwapDB, nil},
		{"select", 2, "loading stale fast", 0, 0, 0, "connection", cmdSelect, nil},
		// lists
		{"lpush", -3, "write denyoom fast", 1, 1, 1, "list", cmdLPush, nil},
		{"rpush", -3, "write denyoom fast", 1, 1, 1, "list", cmdRPush, nil},
		{"lpushx", -3, "write denyoom fast", 1, 1, 1, "list", cmdLPushX, nil},
		{"rpushx", -3, "write denyoom fast", 1, 1, 1, "list", cmdRPushX, nil},
		{"lpop", -2, "write fast", 1, 1, 1, "list", cmdLPop, nil},
		{"rpop", -2, "write fast", 1, 1, 1, "list", cmdRPop, nil},
		{"llen", 2, "readonly fast", 1, 1, 1, "list", cmdLLen, nil},
		{"lindex", 3, "readonly", 1, 1, 1, "list", cmdLIndex, nil},
		{"lset", 4, "write denyoom", 1, 1, 1, "list", cmdLSet, nil},
		{"lrange", 4, "readonly", 1, 1, 1, "list", cmdLRange, nil},
		{"ltrim", 4, "write", 1, 1, 1, "list", cmdLTrim, nil},
		{"lrem", 4, "write", 1, 1, 1, "list", cmdLRem, nil},
		{"linsert", 5, "write denyoom", 1, 1, 1, "list", cmdLInsert, nil},
		{"lpos", -3, "readonly", 1, 1, 1, "list", cmdLPos, nil},
		{"lmove", 5, "write denyoom", 1, 2, 1, "list", cmdLMove, nil},
		{"rpoplpush", 3, "write denyoom", 1, 2, 1, "list", cmdRPopLPush, nil},
		{"blmove", 6, "write denyoom blocking", 1, 2, 1, "list", cmdBLMove, nil},
		{"brpoplpush", 4, "write denyoom blocking", 1, 2, 1, "list", cmdBRPopLPush, nil},
		{"blpop", -3, "write blocking", 1, -2, 1, "list", cmdBLPop, nil},
		{"brpop", -3, "write blocking", 1, -2, 1, "list", cmdBRPop, nil},
		{"lmpop", -4, "write", 0, 0, 0, "list", cmdLMPop, numKeysAt(1)},
		{"blmpop", -5, "write blocking", 0, 0, 0, "list", cmdBLMPop, numKeysAt(2)},
		// hashes
		{"hset", -4, "write denyoom fast", 1, 1, 1, "hash", cmdHSet, nil},
		{"hmset", -4, "write denyoom fast", 1, 1, 1, "hash", cmdHSet, nil},
		{"hsetnx", 4, "write denyoom fast", 1, 1, 1, "hash", cmdHSetNX, nil},
		{"hget", 3, "readonly fast", 1, 1, 1, "hash", cmdHGet, nil},
		{"hmget", -3, "readonly fast", 1, 1, 1, "hash", cmdHMGet, nil},
		{"hdel", -3, "write fast", 1, 1, 1, "hash", cmdHDel, nil},
		{"hlen", 2, "readonly fast", 1, 1, 1, "hash", cmdHLen, nil},
		{"hstrlen", 3, "readonly fast", 1, 1, 1, "hash", cmdHStrlen, nil},
		{"hexists", 3, "readonly fast", 1, 1, 1, "hash", cmdHExists, nil},
		{"hgetall", 2, "readonly", 1, 1, 1, "hash", cmdHGetAll, nil},
		{"hkeys", 2, "readonly", 1, 1, 1, "hash", cmdHKeys, nil},
		{"hvals", 2, "readonly", 1, 1, 1, "hash", cmdHVals, nil},
		{"hincrby", 4, "write denyoom fast", 1, 1, 1, "hash", cmdHIncrBy, nil},
		{"hincrbyfloat", 4, "write denyoom fast", 1, 1, 1, "hash", cmdHIncrByFloat, nil},
		{"hrandfield", -2, "readonly", 1, 1, 1, "hash", cmdHRandField, nil},
		{"hscan", -3, "readonly", 1, 1, 1, "hash", cmdHScan, nil},
		{"hexpire", -6, "write denyoom fast", 1, 1, 1, "hash", cmdHExpire, nil},
		{"hpexpire", -6, "write denyoom fast", 1, 1, 1, "hash", cmdHPExpire, nil},
		{"hexpireat", -6, "write denyoom fast", 1, 1, 1, "hash", cmdHExpireAt, nil},
		{"hpexpireat", -6, "write denyoom fast", 1, 1, 1, "hash", cmdHPExpireAt, nil},
		{"httl", -5, "readonly fast", 1, 1, 1, "hash", cmdHTTL, nil},
		{"hpttl", -5, "readonly fast", 1, 1, 1, "hash", cmdHPTTL, nil},
		{"hexpiretime", -5, "readonly fast", 1, 1, 1, "hash", cmdHExpireTime, nil},
		{"hpexpiretime", -5, "readonly fast", 1, 1, 1, "hash", cmdHPExpireTime, nil},
		{"hpersist", -5, "write fast", 1, 1, 1, "hash", cmdHPersist, nil},
		{"hgetdel", -5, "write fast", 1, 1, 1, "hash", cmdHGetDel, nil},
		{"hgetex", -5, "write denyoom fast", 1, 1, 1, "hash", cmdHGetEx, nil},
		{"hsetex", -6, "write denyoom fast", 1, 1, 1, "hash", cmdHSetEx, nil},
		// sets
		{"sadd", -3, "write denyoom fast", 1, 1, 1, "set", cmdSAdd, nil},
		{"srem", -3, "write fast", 1, 1, 1, "set", cmdSRem, nil},
		{"scard", 2, "readonly fast", 1, 1, 1, "set", cmdSCard, nil},
		{"sismember", 3, "readonly fast", 1, 1, 1, "set", cmdSIsMember, nil},
		{"smismember", -3, "readonly fast", 1, 1, 1, "set", cmdSMIsMember, nil},
		{"smembers", 2, "readonly", 1, 1, 1, "set", cmdSMembers, nil},
		{"srandmember", -2, "readonly", 1, 1, 1, "set", cmdSRandMember, nil},
		{"spop", -2, "write fast", 1, 1, 1, "set", cmdSPop, nil},
		{"smove", 4, "write fast", 1, 2, 1, "set", cmdSMove, nil},
		{"sinter", -2, "readonly", 1, -1, 1, "set", cmdSInter, nil},
		{"sunion", -2, "readonly", 1, -1, 1, "set", cmdSUnion, nil},
		{"sdiff", -2, "readonly", 1, -1, 1, "set", cmdSDiff, nil},
		{"sinterstore", -3, "write denyoom", 1, -1, 1, "set", cmdSInterStore, nil},
		{"sunionstore", -3, "write denyoom", 1, -1, 1, "set", cmdSUnionStore, nil},
		{"sdiffstore", -3, "write denyoom", 1, -1, 1, "set", cmdSDiffStore, nil},
		{"sintercard", -3, "readonly", 0, 0, 0, "set", cmdSInterCard, numKeysAt(1)},
		{"sscan", -3, "readonly", 1, 1, 1, "set", cmdSScan, nil},
		// sorted sets
		{"zadd", -4, "write denyoom fast", 1, 1, 1, "sortedset", cmdZAdd, nil},
		{"zincrby", 4, "write denyoom fast", 1, 1, 1, "sortedset", cmdZIncrBy, nil},
		{"zrem", -3, "write fast", 1, 1, 1, "sortedset", cmdZRem, nil},
		{"zcard", 2, "readonly fast", 1, 1, 1, "sortedset", cmdZCard, nil},
		{"zscore", 3, "readonly fast", 1, 1, 1, "sortedset", cmdZScore, nil},
		{"zmscore", -3, "readonly fast", 1, 1, 1, "sortedset", cmdZMScore, nil},
		{"zrank", -3, "readonly fast", 1, 1, 1, "sortedset", cmdZRank, nil},
		{"zrevrank", -3, "readonly fast", 1, 1, 1, "sortedset", cmdZRevRank, nil},
		{"zcount", 4, "readonly fast", 1, 1, 1, "sortedset", cmdZCount, nil},
		{"zlexcount", 4, "readonly fast", 1, 1, 1, "sortedset", cmdZLexCount, nil},
		{"zrange", -4, "readonly", 1, 1, 1, "sortedset", cmdZRange, nil},
		{"zrevrange", -4, "readonly", 1, 1, 1, "sortedset", cmdZRevRange, nil},
		{"zrangebyscore", -4, "readonly", 1, 1, 1, "sortedset", cmdZRangeByScore, nil},
		{"zrevrangebyscore", -4, "readonly", 1, 1, 1, "sortedset", cmdZRevRangeByScore, nil},
		{"zrangebylex", -4, "readonly", 1, 1, 1, "sortedset", cmdZRangeByLex, nil},
		{"zrevrangebylex", -4, "readonly", 1, 1, 1, "sortedset", cmdZRevRangeByLex, nil},
		{"zrangestore", -5, "write denyoom", 1, 2, 1, "sortedset", cmdZRangeStore, nil},
		{"zremrangebyrank", 4, "write", 1, 1, 1, "sortedset", cmdZRemRangeByRank, nil},
		{"zremrangebyscore", 4, "write", 1, 1, 1, "sortedset", cmdZRemRangeByScore, nil},
		{"zremrangebylex", 4, "write", 1, 1, 1, "sortedset", cmdZRemRangeByLex, nil},
		{"zpopmin", -2, "write fast", 1, 1, 1, "sortedset", cmdZPopMin, nil},
		{"zpopmax", -2, "write fast", 1, 1, 1, "sortedset", cmdZPopMax, nil},
		{"bzpopmin", -3, "write fast blocking", 1, -2, 1, "sortedset", cmdBZPopMin, nil},
		{"bzpopmax", -3, "write fast blocking", 1, -2, 1, "sortedset", cmdBZPopMax, nil},
		{"zmpop", -4, "write", 0, 0, 0, "sortedset", cmdZMPop, numKeysAt(1)},
		{"bzmpop", -5, "write blocking", 0, 0, 0, "sortedset", cmdBZMPop, numKeysAt(2)},
		{"zrandmember", -2, "readonly", 1, 1, 1, "sortedset", cmdZRandMember, nil},
		{"zunion", -3, "readonly", 0, 0, 0, "sortedset", cmdZUnion, numKeysAt(1)},
		{"zinter", -3, "readonly", 0, 0, 0, "sortedset", cmdZInter, numKeysAt(1)},
		{"zdiff", -3, "readonly", 0, 0, 0, "sortedset", cmdZDiff, numKeysAt(1)},
		{"zunionstore", -4, "write denyoom", 0, 0, 0, "sortedset", cmdZUnionStore, zstoreKeys},
		{"zinterstore", -4, "write denyoom", 0, 0, 0, "sortedset", cmdZInterStore, zstoreKeys},
		{"zdiffstore", -4, "write denyoom", 0, 0, 0, "sortedset", cmdZDiffStore, zstoreKeys},
		{"zintercard", -3, "readonly", 0, 0, 0, "sortedset", cmdZInterCard, numKeysAt(1)},
		{"zscan", -3, "readonly", 1, 1, 1, "sortedset", cmdZScan, nil},
		// streams
		{"xadd", -5, "write denyoom fast", 1, 1, 1, "stream", cmdXAdd, nil},
		{"xlen", 2, "readonly fast", 1, 1, 1, "stream", cmdXLen, nil},
		{"xrange", -4, "readonly", 1, 1, 1, "stream", cmdXRange, nil},
		{"xrevrange", -4, "readonly", 1, 1, 1, "stream", cmdXRevRange, nil},
		{"xdel", -3, "write fast", 1, 1, 1, "stream", cmdXDel, nil},
		{"xtrim", -4, "write", 1, 1, 1, "stream", cmdXTrim, nil},
		{"xsetid", -3, "write denyoom fast", 1, 1, 1, "stream", cmdXSetID, nil},
		{"xread", -4, "readonly blocking", 0, 0, 0, "stream", cmdXRead, xreadKeys},
		{"xreadgroup", -7, "write blocking", 0, 0, 0, "stream", cmdXReadGroup, xreadKeys},
		{"xgroup", -2, "write denyoom", 2, 2, 1, "stream", cmdXGroup, nil},
		{"xack", -4, "write fast", 1, 1, 1, "stream", cmdXAck, nil},
		{"xpending", -3, "readonly", 1, 1, 1, "stream", cmdXPending, nil},
		{"xclaim", -6, "write fast", 1, 1, 1, "stream", cmdXClaim, nil},
		{"xautoclaim", -6, "write fast", 1, 1, 1, "stream", cmdXAutoClaim, nil},
		{"xinfo", -2, "readonly", 2, 2, 1, "stream", cmdXInfo, nil},
		// hyperloglog
		{"pfadd", -2, "write denyoom fast", 1, 1, 1, "hyperloglog", cmdPFAdd, nil},
		{"pfcount", -2, "readonly", 1, -1, 1, "hyperloglog", cmdPFCount, nil},
		{"pfmerge", -2, "write denyoom", 1, -1, 1, "hyperloglog", cmdPFMerge, nil},
		// geo
		{"geoadd", -5, "write denyoom", 1, 1, 1, "geo", cmdGeoAdd, nil},
		{"geopos", -2, "readonly", 1, 1, 1, "geo", cmdGeoPos, nil},
		{"geodist", -4, "readonly", 1, 1, 1, "geo", cmdGeoDist, nil},
		{"geohash", -2, "readonly", 1, 1, 1, "geo", cmdGeoHash, nil},
		{"georadius", -6, "write denyoom", 1, 1, 1, "geo", cmdGeoRadius, georadiusKeys},
		{"georadius_ro", -6, "readonly", 1, 1, 1, "geo", cmdGeoRadiusRO, nil},
		{"georadiusbymember", -5, "write denyoom", 1, 1, 1, "geo", cmdGeoRadiusByMember, georadiusKeys},
		{"georadiusbymember_ro", -5, "readonly", 1, 1, 1, "geo", cmdGeoRadiusByMemberRO, nil},
		{"geosearch", -7, "readonly", 1, 1, 1, "geo", cmdGeoSearch, nil},
		{"geosearchstore", -8, "write denyoom", 1, 2, 1, "geo", cmdGeoSearchStore, nil},
		// pub/sub
		{"subscribe", -2, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdSubscribe, nil},
		{"unsubscribe", -1, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdUnsubscribe, nil},
		{"psubscribe", -2, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdPSubscribe, nil},
		{"punsubscribe", -1, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdPUnsubscribe, nil},
		{"ssubscribe", -2, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdSSubscribe, nil},
		{"sunsubscribe", -1, "pubsub noscript loading stale", 0, 0, 0, "pubsub", cmdSUnsubscribe, nil},
		{"publish", 3, "pubsub loading stale fast may_replicate", 0, 0, 0, "pubsub", cmdPublish, nil},
		{"spublish", 3, "pubsub loading stale fast may_replicate", 0, 0, 0, "pubsub", cmdSPublish, nil},
		{"pubsub", -2, "pubsub loading stale", 0, 0, 0, "pubsub", cmdPubsub, nil},
		// transactions
		{"multi", 1, "noscript loading stale fast allow_busy txctl", 0, 0, 0, "transaction", cmdMulti, nil},
		{"exec", 1, "noscript loading stale skip_slowlog txctl", 0, 0, 0, "transaction", cmdExec, nil},
		{"discard", 1, "noscript loading stale fast allow_busy txctl", 0, 0, 0, "transaction", cmdDiscard, nil},
		{"watch", -2, "noscript loading stale fast allow_busy txctl", 1, -1, 1, "transaction", cmdWatch, nil},
		{"unwatch", 1, "noscript loading stale fast allow_busy txctl", 0, 0, 0, "transaction", cmdUnwatch, nil},
		// scripting
		{"eval", -3, "noscript skip_monitor may_replicate lockall", 0, 0, 0, "scripting", cmdEval, numKeysAt(2)},
		{"evalsha", -3, "noscript skip_monitor may_replicate lockall", 0, 0, 0, "scripting", cmdEvalSha, numKeysAt(2)},
		{"eval_ro", -3, "noscript skip_monitor readonly lockall", 0, 0, 0, "scripting", cmdEvalRO, numKeysAt(2)},
		{"evalsha_ro", -3, "noscript skip_monitor readonly lockall", 0, 0, 0, "scripting", cmdEvalShaRO, numKeysAt(2)},
		{"script", -2, "noscript allow_busy", 0, 0, 0, "scripting", cmdScript, nil},
		{"fcall", -3, "noscript skip_monitor may_replicate lockall", 0, 0, 0, "scripting", cmdFCall, numKeysAt(2)},
		{"fcall_ro", -3, "noscript skip_monitor readonly lockall", 0, 0, 0, "scripting", cmdFCallRO, numKeysAt(2)},
		{"function", -2, "noscript may_replicate allow_busy", 0, 0, 0, "scripting", cmdFunction, nil},
		// connection
		{"ping", -1, "fast loading stale", 0, 0, 0, "connection", cmdPing, nil},
		{"echo", 2, "fast loading stale", 0, 0, 0, "connection", cmdEcho, nil},
		{"quit", -1, "noscript loading stale fast no_auth allow_busy txctl", 0, 0, 0, "connection", cmdQuit, nil},
		{"reset", 1, "noscript loading stale fast no_auth allow_busy txctl", 0, 0, 0, "connection", cmdReset, nil},
		{"hello", -1, "noscript loading stale fast no_auth allow_busy", 0, 0, 0, "connection", cmdHello, nil},
		{"auth", -2, "noscript loading stale fast no_auth allow_busy", 0, 0, 0, "connection", cmdAuth, nil},
		{"client", -2, "noscript loading stale", 0, 0, 0, "connection", cmdClient, nil},
		// server
		{"command", -1, "loading stale", 0, 0, 0, "server", cmdCommand, nil},
		{"config", -2, "admin noscript loading stale", 0, 0, 0, "server", cmdConfig, nil},
		{"info", -1, "loading stale", 0, 0, 0, "server", cmdInfo, nil},
		{"debug", -2, "admin noscript loading stale", 0, 0, 0, "server", cmdDebug, nil},
		{"time", 1, "loading stale fast", 0, 0, 0, "server", cmdTime, nil},
		{"lastsave", 1, "loading stale fast", 0, 0, 0, "server", cmdLastSave, nil},
		{"save", 1, "admin noscript", 0, 0, 0, "server", cmdSave, nil},
		{"bgsave", -1, "admin noscript", 0, 0, 0, "server", cmdBgSave, nil},
		{"bgrewriteaof", 1, "admin noscript", 0, 0, 0, "server", cmdBgRewriteAOF, nil},
		{"shutdown", -1, "admin noscript loading stale", 0, 0, 0, "server", cmdShutdown, nil},
		{"monitor", 1, "admin noscript loading stale", 0, 0, 0, "server", cmdMonitor, nil},
		{"memory", -2, "readonly", 0, 0, 0, "server", cmdMemory, nil},
		{"latency", -2, "admin noscript loading stale", 0, 0, 0, "server", cmdLatency, nil},
		{"lolwut", -1, "readonly fast", 0, 0, 0, "server", cmdLolwut, nil},
		{"module", -2, "admin noscript", 0, 0, 0, "server", cmdModule, nil},
		{"acl", -2, "noscript loading stale", 0, 0, 0, "server", cmdACL, nil},
		{"slowlog", -2, "admin loading stale", 0, 0, 0, "server", cmdSlowlog, nil},
		{"role", 1, "noscript loading stale fast", 0, 0, 0, "server", cmdRole, nil},
		{"replicaof", 3, "admin noscript stale", 0, 0, 0, "server", cmdReplicaOf, nil},
		{"slaveof", 3, "admin noscript stale", 0, 0, 0, "server", cmdReplicaOf, nil},
		{"sync", 1, "admin noscript", 0, 0, 0, "server", cmdSync, nil},
		{"psync", -3, "admin noscript", 0, 0, 0, "server", cmdSync, nil},
		{"replconf", -1, "admin noscript loading stale allow_busy", 0, 0, 0, "server", cmdReplconf, nil},
		{"wait", 3, "noscript", 0, 0, 0, "server", cmdWait, nil},
		{"waitaof", 4, "noscript", 0, 0, 0, "server", cmdWaitAOF, nil},
		{"cluster", -2, "", 0, 0, 0, "server", cmdCluster, nil},
		{"readonly", 1, "fast loading stale", 0, 0, 0, "connection", cmdCluster, nil},
		{"readwrite", 1, "fast loading stale", 0, 0, 0, "connection", cmdCluster, nil},
	}
	notifyClass := map[string]int64{
		"string": config.NotifyString, "bitmap": config.NotifyString, "keyspace": config.NotifyGeneric,
		"list": config.NotifyList, "hash": config.NotifyHash, "set": config.NotifySet,
		"sortedset": config.NotifyZSet, "stream": config.NotifyStream, "hyperloglog": config.NotifyString,
		"geo": config.NotifyZSet,
	}
	renamed := s.cfg.Renamed()
	s.cmds = make(map[string]*Command, len(specs))
	for _, sp := range specs {
		cmd := &Command{
			Name: sp.name, Arity: sp.arity, Flags: parseFlags(sp.flags), First: sp.first, Last: sp.last,
			Step: sp.step, Group: sp.group, Handler: sp.h, Keys: sp.keys,
		}
		if cmd.Has(FlagWrite) {
			cmd.NotifyClass = notifyClass[sp.group]
			cmd.Event = sp.name
		}
		name := sp.name
		if nn, ok := renamed[name]; ok {
			if nn == "" {
				continue
			}
			name = strings.ToLower(nn)
		}
		s.cmds[name] = cmd
	}
	s.cmds["config"].Subs = []string{"get", "set", "resetstat", "rewrite", "help"}
	s.cmds["client"].Subs = []string{"id", "getname", "setname", "setinfo", "info", "list", "kill", "pause", "unpause", "reply", "no-evict", "no-touch", "unblock", "help"}
	s.cmds["object"].Subs = []string{"encoding", "freq", "idletime", "refcount", "help"}
	s.cmds["xgroup"].Subs = []string{"create", "setid", "destroy", "createconsumer", "delconsumer", "help"}
	s.cmds["xinfo"].Subs = []string{"stream", "groups", "consumers", "help"}
	s.cmds["script"].Subs = []string{"load", "exists", "flush", "kill", "help"}
	s.cmds["function"].Subs = []string{"load", "list", "delete", "flush", "dump", "restore", "stats", "kill", "help"}
}

var flagWire = []struct {
	f    Flag
	name string
}{
	{FlagWrite, "write"}, {FlagReadonly, "readonly"}, {FlagDenyOOM, "denyoom"}, {FlagAdmin, "admin"},
	{FlagPubSub, "pubsub"}, {FlagNoScript, "noscript"}, {FlagBlocking, "blocking"}, {FlagLoading, "loading"},
	{FlagStale, "stale"}, {FlagSkipMonitor, "skip_monitor"}, {FlagSkipSlowlog, "skip_slowlog"},
	{FlagFast, "fast"}, {FlagNoAuth, "no_auth"}, {FlagMayReplicate, "may_replicate"}, {FlagAllowBusy, "allow_busy"},
}

func (c *Ctx) writeCommandInfo(cmd *Command) {
	w := c.W
	w.WriteArrayLen(10)
	w.WriteBulkString(cmd.Name)
	w.WriteInt(int64(cmd.Arity))
	var flags []string
	for _, f := range flagWire {
		if cmd.Has(f.f) {
			flags = append(flags, f.name)
		}
	}
	if cmd.Has(FlagLockAll) && cmd.Group == "scripting" {
		flags = append(flags, "movablekeys")
	} else if cmd.Keys != nil {
		flags = append(flags, "movablekeys")
	}
	w.WriteSetLen(len(flags))
	for _, f := range flags {
		w.WriteSimple(f)
	}
	w.WriteInt(int64(cmd.First))
	w.WriteInt(int64(cmd.Last))
	w.WriteInt(int64(cmd.Step))
	cats := []string{"@" + cmd.Group}
	if cmd.Has(FlagWrite) {
		cats = append(cats, "@write")
	}
	if cmd.Has(FlagReadonly) {
		cats = append(cats, "@read")
	}
	if cmd.Has(FlagFast) {
		cats = append(cats, "@fast")
	} else {
		cats = append(cats, "@slow")
	}
	if cmd.Has(FlagBlocking) {
		cats = append(cats, "@blocking")
	}
	if cmd.Has(FlagAdmin) {
		cats = append(cats, "@admin", "@dangerous")
	}
	w.WriteSetLen(len(cats))
	for _, cat := range cats {
		w.WriteSimple(cat)
	}
	w.WriteSetLen(0)   // tips
	w.WriteArrayLen(0) // key specs
	w.WriteArrayLen(len(cmd.Subs))
	for _, sub := range cmd.Subs {
		w.WriteArrayLen(10)
		w.WriteBulkString(cmd.Name + "|" + sub)
		w.WriteInt(-2)
		w.WriteSetLen(0)
		w.WriteInt(0)
		w.WriteInt(0)
		w.WriteInt(0)
		w.WriteSetLen(0)
		w.WriteSetLen(0)
		w.WriteArrayLen(0)
		w.WriteArrayLen(0)
	}
}

func cmdCommand(c *Ctx) {
	s := c.s
	if len(c.Args) == 1 {
		names := s.sortedCommandNames()
		c.W.WriteArrayLen(len(names))
		for _, n := range names {
			c.writeCommandInfo(s.cmds[n])
		}
		return
	}
	switch strings.ToLower(c.Arg(1)) {
	case "count":
		c.W.WriteInt(int64(len(s.cmds)))
	case "info":
		names := argStrings(c.Args[2:])
		if len(names) == 0 {
			names = s.sortedCommandNames()
		}
		c.W.WriteArrayLen(len(names))
		for _, n := range names {
			if cmd := s.cmds[strings.ToLower(n)]; cmd != nil {
				c.writeCommandInfo(cmd)
			} else {
				c.W.WriteNullArray()
			}
		}
	case "docs":
		names := argStrings(c.Args[2:])
		if len(names) == 0 {
			names = s.sortedCommandNames()
		}
		var found []*Command
		for _, n := range names {
			if cmd := s.cmds[strings.ToLower(n)]; cmd != nil {
				found = append(found, cmd)
			}
		}
		c.W.WriteMapLen(len(found))
		for _, cmd := range found {
			c.W.WriteBulkString(cmd.Name)
			c.W.WriteMapLen(3)
			c.W.WriteBulkString("summary")
			c.W.WriteBulkString("See https://redis.io/commands/" + cmd.Name)
			c.W.WriteBulkString("since")
			c.W.WriteBulkString("1.0.0")
			c.W.WriteBulkString("group")
			c.W.WriteBulkString(cmd.Group)
		}
	case "list":
		names := s.sortedCommandNames()
		if len(c.Args) == 5 && c.Is(2, "filterby") {
			var out []string
			val := strings.ToLower(c.Arg(4))
			for _, n := range names {
				cmd := s.cmds[n]
				switch strings.ToLower(c.Arg(3)) {
				case "aclcat":
					if cmd.Group == strings.TrimPrefix(val, "@") {
						out = append(out, n)
					}
				case "pattern":
					if matchGlob(val, n) {
						out = append(out, n)
					}
				case "module":
				default:
					c.SyntaxErr()
					return
				}
			}
			names = out
		} else if len(c.Args) != 2 {
			c.SyntaxErr()
			return
		}
		c.W.WriteStrings(names)
	case "getkeys", "getkeysandflags":
		if len(c.Args) < 3 {
			c.WrongSubArgs()
			return
		}
		cmd := s.cmds[strings.ToLower(c.Arg(2))]
		if cmd == nil {
			c.Err("ERR Invalid command specified")
			return
		}
		args := c.Args[2:]
		if (cmd.Arity > 0 && len(args) != cmd.Arity) || (cmd.Arity < 0 && len(args) < -cmd.Arity) {
			c.Err("ERR Invalid number of arguments specified for command")
			return
		}
		pos, _ := cmd.KeyPositions(args)
		if len(pos) == 0 {
			c.Err("ERR The command has no key arguments")
			return
		}
		c.W.WriteArrayLen(len(pos))
		for _, p := range pos {
			if strings.EqualFold(c.Arg(1), "getkeysandflags") {
				c.W.WriteArrayLen(2)
				c.W.WriteBulk(args[p])
				if cmd.Has(FlagWrite) {
					c.W.WriteSetLen(1)
					c.W.WriteSimple("RW")
				} else {
					c.W.WriteSetLen(1)
					c.W.WriteSimple("RO")
				}
			} else {
				c.W.WriteBulk(args[p])
			}
		}
	case "help":
		c.writeHelp("COMMAND", []string{
			"(no subcommand)", "    Return details about all commands.",
			"COUNT", "    Return the total number of commands in this server.",
			"LIST", "    Return a list of all commands in this server.",
			"INFO [<command-name> ...]", "    Return details about multiple commands.",
			"DOCS [<command-name> ...]", "    Return documentation details about multiple commands.",
			"GETKEYS <full-command>", "    Return the keys from a full command.",
			"GETKEYSANDFLAGS <full-command>", "    Return the keys and the access flags from a full command.",
		})
	default:
		c.UnknownSub()
	}
}

func (s *Server) sortedCommandNames() []string {
	names := make([]string, 0, len(s.cmds))
	for n := range s.cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
