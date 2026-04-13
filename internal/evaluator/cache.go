package evaluator

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto/v2"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// CachedFlagState holds cached state for a single flag.
type CachedFlagState struct {
	FlagID     string
	State      pbflagsv1.State
	Value      *pbflagsv1.FlagValue
	Archived   bool
	Conditions []CachedCondition // compiled condition chain (nil for static/unconfigured flags)
	DimMeta    CachedDimMeta     // dimension classification metadata (nil for flags without conditions)
}

// KillSet holds the current set of globally killed flags.
type KillSet struct {
	FlagIDs map[string]struct{}
}

// IsKilled checks if a flag is globally killed.
func (ks *KillSet) IsKilled(flagID string) bool {
	if ks == nil {
		return false
	}
	_, ok := ks.FlagIDs[flagID]
	return ok
}

// CacheStore provides caching for all evaluator data.
//
// Ristretto caches provide fast lookups with TTL-based freshness. When an entry
// expires, it is evicted. To satisfy the design requirement that stale data is
// served indefinitely during server outages, a parallel staleFlagMap retains
// the last-known values without TTL. The stale map is consulted only when the
// Ristretto cache misses AND the server fetch fails.
type CacheStore struct {
	flagCache *ristretto.Cache[string, *CachedFlagState]

	staleFlagMu  sync.RWMutex
	staleFlagMap map[string]*CachedFlagState

	killSetMu sync.RWMutex
	killSet   *KillSet

	flagTTL       time.Duration
	jitterPercent int
}

// CacheStoreConfig configures cache sizes and TTLs.
type CacheStoreConfig struct {
	FlagTTL       time.Duration
	JitterPercent int
}

// NewCacheStore creates a cache store.
func NewCacheStore(cfg CacheStoreConfig) (*CacheStore, error) {
	flagCache, err := ristretto.NewCache(&ristretto.Config[string, *CachedFlagState]{
		NumCounters: 10_000,
		MaxCost:     1_000,
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}

	return &CacheStore{
		flagCache:     flagCache,
		staleFlagMap:  make(map[string]*CachedFlagState),
		killSet:       &KillSet{FlagIDs: make(map[string]struct{})},
		flagTTL:       cfg.FlagTTL,
		jitterPercent: cfg.JitterPercent,
	}, nil
}

// Close releases cache resources.
func (s *CacheStore) Close() {
	s.flagCache.Close()
}

// FlushAll evicts all entries from the hot cache (Ristretto) and the stale
// fallback map, forcing cold-start fetches on the next evaluation.
func (s *CacheStore) FlushAll() {
	s.flagCache.Clear()
	s.staleFlagMu.Lock()
	s.staleFlagMap = make(map[string]*CachedFlagState)
	s.staleFlagMu.Unlock()
}

// FlushHot evicts entries from the hot cache (Ristretto) only. The stale
// fallback map is preserved, simulating natural TTL expiry.
func (s *CacheStore) FlushHot() {
	s.flagCache.Clear()
}

// WaitAll blocks until the Ristretto cache has processed all pending
// writes and evictions.
func (s *CacheStore) WaitAll() {
	s.flagCache.Wait()
}

// GetKillSet returns the current kill set.
func (s *CacheStore) GetKillSet() *KillSet {
	s.killSetMu.RLock()
	defer s.killSetMu.RUnlock()
	return s.killSet
}

// SetKillSet atomically replaces the kill set.
func (s *CacheStore) SetKillSet(ks *KillSet) {
	s.killSetMu.Lock()
	defer s.killSetMu.Unlock()
	s.killSet = ks
}

// GetFlagState returns the cached state for a flag, or nil on miss.
// When flagTTL is 0 (write-through mode), always returns nil to force a fresh fetch.
func (s *CacheStore) GetFlagState(flagID string) *CachedFlagState {
	if s.flagTTL <= 0 {
		return nil
	}
	val, ok := s.flagCache.Get(flagID)
	if !ok {
		return nil
	}
	return val
}

// SetFlagState caches a flag state with TTL + jitter.
// When flagTTL is 0 (write-through mode), only the stale fallback map is populated.
func (s *CacheStore) SetFlagState(state *CachedFlagState) {
	if s.flagTTL > 0 {
		s.flagCache.SetWithTTL(state.FlagID, state, 1, s.jitteredTTL(s.flagTTL))
	}
	s.staleFlagMu.Lock()
	s.staleFlagMap[state.FlagID] = state
	s.staleFlagMu.Unlock()
}

// GetStaleFlagState returns the last-known state from the stale fallback map.
func (s *CacheStore) GetStaleFlagState(flagID string) *CachedFlagState {
	s.staleFlagMu.RLock()
	defer s.staleFlagMu.RUnlock()
	return s.staleFlagMap[flagID]
}

// CachedFlagCount returns the approximate number of cached flag states.
func (s *CacheStore) CachedFlagCount() int32 {
	m := s.flagCache.Metrics
	if m == nil {
		return 0
	}
	return int32(m.KeysAdded() - m.KeysEvicted())
}

func (s *CacheStore) jitteredTTL(base time.Duration) time.Duration {
	if s.jitterPercent <= 0 {
		return base
	}
	jitterRange := float64(base) * float64(s.jitterPercent) / 100.0
	jitter := (rand.Float64()*2 - 1) * jitterRange
	return base + time.Duration(jitter)
}
