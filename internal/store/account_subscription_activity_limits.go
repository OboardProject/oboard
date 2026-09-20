package store

import (
	"hash/maphash"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const subscriptionLimitShards = 32
const subscriptionLimitEntriesPerShard = 256

type subscriptionRequestMinute struct {
	minute   int64
	count    uint64
	overflow bool
}

type subscriptionMemoryBucket struct {
	level         float64
	at            time.Time
	observedSince time.Time
	requests      [2]subscriptionRequestMinute
}
type subscriptionLimitShard struct {
	sync.Mutex
	buckets map[string]subscriptionMemoryBucket
}

// One authoritative Controller owns subscription ingress. This is not a distributed
// allowance: running multiple writers behind a load balancer is unsupported.
// Empty initial credit and a process-wide warmup prevent restart/eviction bursts.
type subscriptionMemoryLimiter struct {
	once    sync.Once
	seed    maphash.Seed
	started time.Time
	shards  [subscriptionLimitShards]subscriptionLimitShard
}

func (l *subscriptionMemoryLimiter) consume(key string, capacity int, at time.Time) (bool, time.Duration) {
	l.once.Do(func() { l.seed = maphash.MakeSeed(); l.started = at })
	if capacity <= 0 {
		return false, 0
	}
	shard := &l.shards[maphash.String(l.seed, key)%subscriptionLimitShards]
	shard.Lock()
	defer shard.Unlock()
	if shard.buckets == nil {
		shard.buckets = make(map[string]subscriptionMemoryBucket)
	}
	b, exists := shard.buckets[key]
	if !exists {
		if len(shard.buckets) >= subscriptionLimitEntriesPerShard {
			for k, v := range shard.buckets {
				if at.Sub(v.at) >= 2*time.Minute {
					delete(shard.buckets, k)
				}
			}
			if len(shard.buckets) >= subscriptionLimitEntriesPerShard {
				return true, time.Minute
			}
		}
		// New identities never reset the global startup warmup. Evicted entries
		// have been idle longer than a full refill interval.
		b = subscriptionMemoryBucket{level: float64(capacity) * math.Max(0, 1-at.Sub(l.started).Seconds()/60), at: at}
	}
	if strings.HasPrefix(key, "account:") {
		if b.observedSince.IsZero() {
			b.observedSince = at
		}
		if !at.Before(b.at) {
			minute := at.UTC().Truncate(time.Minute).Unix()
			slot := &b.requests[uint64(minute/60)&1]
			if slot.minute != minute {
				*slot = subscriptionRequestMinute{minute: minute}
			}
			if slot.count == math.MaxUint64 {
				slot.overflow = true
			} else {
				slot.count++
			}
		} else {
			// A clock reversal cannot certify a complete natural minute.
			b.observedSince = b.at
		}
	}
	if at.After(b.at) {
		b.level = math.Max(0, b.level-at.Sub(b.at).Seconds()*float64(capacity)/60)
		b.at = at
	}
	limited := b.level+1 > float64(capacity)
	if !limited {
		b.level++
	}
	shard.buckets[key] = b
	if limited {
		return true, time.Duration(math.Ceil((b.level+1-float64(capacity))*60/float64(capacity))) * time.Second
	}
	return false, 0
}
func subscriptionLimitSource(raw string) string {
	a, err := netip.ParseAddr(raw)
	if err != nil {
		return "unavailable"
	}
	a = a.Unmap()
	bits := 56
	if a.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(a, bits).Masked().String()
}

// AllowSubscriptionIngress charges every HTTP request before token lookup. No
// attacker-controlled credential is retained as a key, including invalid tokens.
func (s *Store) AllowSubscriptionIngress(source string, at time.Time) (bool, time.Duration) {
	global, gr := s.subscriptionLimiter.consume("ingress:global", 6000, at)
	local, lr := s.subscriptionLimiter.consume("ingress:source:"+subscriptionLimitSource(source), 120, at)
	if lr > gr {
		gr = lr
	}
	return !global && !local, gr
}
func (s *Store) consumeAccountSubscriptionLimit(userID int64, capacity int, at time.Time) (bool, time.Duration) {
	return s.subscriptionLimiter.consume("account:"+strconv.FormatInt(userID, 10), capacity, at)
}
