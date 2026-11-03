package security

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// KeyedLimiter applies an independent token bucket per key (for example per
// remote IP) and forgets idle keys to bound memory use.
type KeyedLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	maxKeys int
	idle    time.Duration
	entries map[string]*limiterEntry
	now     func() time.Time
}

type limiterEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewKeyedLimiter creates a limiter allowing perSecond events with the given burst per key.
func NewKeyedLimiter(perSecond float64, burst, maxKeys int) *KeyedLimiter {
	return &KeyedLimiter{
		limit:   rate.Limit(perSecond),
		burst:   burst,
		maxKeys: maxKeys,
		idle:    10 * time.Minute,
		entries: make(map[string]*limiterEntry),
		now:     time.Now,
	}
}

// Allow reports whether an event for key may proceed now.
func (k *KeyedLimiter) Allow(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	e, ok := k.entries[key]
	if !ok {
		if len(k.entries) >= k.maxKeys {
			k.evictLocked(now)
			if len(k.entries) >= k.maxKeys {
				// Under a flood of distinct keys, fail closed rather than grow unbounded.
				return false
			}
		}
		e = &limiterEntry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.entries[key] = e
	}
	e.lastSeen = now
	return e.lim.AllowN(now, 1)
}

func (k *KeyedLimiter) evictLocked(now time.Time) {
	for key, e := range k.entries {
		if now.Sub(e.lastSeen) > k.idle {
			delete(k.entries, key)
		}
	}
}
