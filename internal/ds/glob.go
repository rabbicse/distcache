// Package ds contains the data structures backing DistCache value types:
// lists, sorted sets (skiplist), streams, HyperLogLog, geohash and glob
// matching. They are not safe for concurrent use; the keyspace serialises
// access through shard locks.
package ds

// Match reports whether str matches the Redis glob-style pattern. It is a
// faithful port of Redis' stringmatchlen(): supports * ? [abc] [^a] [a-z] and
// backslash escaping.
func Match(pattern, str string, nocase bool) bool {
	skipLongerMatches := false
	return matchImpl(pattern, str, nocase, &skipLongerMatches, 0)
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func matchImpl(p, s string, nocase bool, skipLonger *bool, nesting int) bool {
	if nesting > 1000 {
		return false
	}
	for len(p) > 0 && len(s) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 1 && p[1] == '*' {
				p = p[1:]
			}
			if len(p) == 1 {
				return true
			}
			for len(s) > 0 {
				if matchImpl(p[1:], s, nocase, skipLonger, nesting+1) {
					return true
				}
				if *skipLonger {
					return false
				}
				s = s[1:]
			}
			*skipLonger = true
			return false
		case '?':
			s = s[1:]
		case '[':
			p = p[1:]
			not := len(p) > 0 && p[0] == '^'
			if not {
				p = p[1:]
			}
			match := false
			for {
				if len(p) == 0 {
					break
				}
				if p[0] == '\\' && len(p) >= 2 {
					p = p[1:]
					if p[0] == s[0] {
						match = true
					}
				} else if p[0] == ']' {
					break
				} else if len(p) >= 3 && p[1] == '-' {
					start, end := p[0], p[2]
					c := s[0]
					if start > end {
						start, end = end, start
					}
					if nocase {
						start, end, c = lower(start), lower(end), lower(c)
					}
					p = p[2:]
					if c >= start && c <= end {
						match = true
					}
				} else {
					if nocase {
						if lower(p[0]) == lower(s[0]) {
							match = true
						}
					} else if p[0] == s[0] {
						match = true
					}
				}
				p = p[1:]
			}
			if len(p) == 0 {
				// Unterminated class: Redis treats end of pattern as ']'.
				p = " "
			}
			if not {
				match = !match
			}
			if !match {
				return false
			}
			s = s[1:]
		case '\\':
			if len(p) >= 2 {
				p = p[1:]
			}
			fallthrough
		default:
			if nocase {
				if lower(p[0]) != lower(s[0]) {
					return false
				}
			} else if p[0] != s[0] {
				return false
			}
			s = s[1:]
		}
		p = p[1:]
		if len(s) == 0 {
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			break
		}
	}
	return len(p) == 0 && len(s) == 0
}
