package synchronization

import "time"

// seenCache remembers processed event IDs for a bounded time and count. It is
// not safe for concurrent use; the engine guards it with its mutex.
type seenCache struct {
	ttl   time.Duration
	max   int
	at    map[string]time.Time
	order []string // insertion order, oldest first
}

func newSeenCache(ttl time.Duration, max int) *seenCache {
	return &seenCache{ttl: ttl, max: max, at: make(map[string]time.Time)}
}

// Seen reports whether id was recorded and has not expired.
func (c *seenCache) Seen(id string, now time.Time) bool {
	t, ok := c.at[id]
	return ok && now.Sub(t) <= c.ttl
}

// Add records id at time now.
func (c *seenCache) Add(id string, now time.Time) {
	if _, ok := c.at[id]; !ok {
		c.order = append(c.order, id)
	}
	c.at[id] = now
	c.prune(now)
}

func (c *seenCache) prune(now time.Time) {
	drop := 0
	for drop < len(c.order) {
		id := c.order[drop]
		if len(c.order)-drop <= c.max && now.Sub(c.at[id]) <= c.ttl {
			break
		}
		delete(c.at, id)
		drop++
	}
	if drop > 0 {
		c.order = append(c.order[:0:0], c.order[drop:]...)
	}
}

// Len returns the number of remembered IDs.
func (c *seenCache) Len() int { return len(c.order) }
