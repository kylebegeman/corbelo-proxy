package proxy

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CooldownTracker records temporary upstream rate-limit or overload windows per
// model key. Requests arriving while a model is cooling down receive an
// explicit 429 + Retry-After response immediately, with strictly zero silent
// fallback to any paid provider.
type CooldownTracker struct {
	mu      sync.Mutex
	entries map[string]cooldownEntry
	nowFn   func() time.Time
}

type cooldownEntry struct {
	until  time.Time
	reason string
}

func NewCooldownTracker(nowFn func() time.Time) *CooldownTracker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &CooldownTracker{
		entries: make(map[string]cooldownEntry),
		nowFn:   nowFn,
	}
}

func normalizeModelKey(model string) string {
	k := strings.ToLower(strings.TrimSpace(model))
	if k == "" {
		return "_default"
	}
	return k
}

// Active reports whether the given model key is currently in cooldown and how
// many seconds remain.
func (t *CooldownTracker) Active(model string) (active bool, retryAfterSec int, until time.Time, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := normalizeModelKey(model)
	now := t.nowFn()
	entry, ok := t.entries[key]
	if !ok {
		return false, 0, time.Time{}, ""
	}
	remaining := entry.until.Sub(now)
	if remaining <= 0 {
		delete(t.entries, key)
		return false, 0, time.Time{}, ""
	}
	secs := int(math.Ceil(remaining.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return true, secs, entry.until, entry.reason
}

// Record stores a cooldown window derived from Retry-After or defaultDur.
func (t *CooldownTracker) Record(model, retryAfterHeader string, defaultDur time.Duration, reason string) (int, time.Time) {
	now := t.nowFn()
	dur := ParseRetryAfter(retryAfterHeader, now, defaultDur)
	if dur < time.Second {
		dur = time.Second
	}
	if dur > 15*time.Minute {
		dur = 15 * time.Minute
	}
	until := now.Add(dur)

	t.mu.Lock()
	t.entries[normalizeModelKey(model)] = cooldownEntry{until: until, reason: reason}
	t.mu.Unlock()

	secs := int(math.Ceil(dur.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs, until
}

// ActiveCount returns the number of non-expired cooldown entries for health checks.
func (t *CooldownTracker) ActiveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.nowFn()
	count := 0
	for k, e := range t.entries {
		if e.until.After(now) {
			count++
		} else {
			delete(t.entries, k)
		}
	}
	return count
}

// ParseRetryAfter parses an HTTP Retry-After header (delta-seconds or HTTP-date).
func ParseRetryAfter(raw string, now time.Time, fallback time.Duration) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(trimmed); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(trimmed); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return fallback
}
