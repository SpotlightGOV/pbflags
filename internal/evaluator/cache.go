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
	FlagID   string
	State    pbflagsv1.State
	Value    *pbflagsv1.FlagValue
	Archived bool
}

// CachedOverride holds a cached per-entity override.
type CachedOverride struct {
	FlagID   string
	EntityID string
	State    pbflagsv1.State
	Value    *pbflagsv1.FlagValue
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
// served indefinitely during server outages, a parallel staleFlagMap and
// staleOverrideMap retain the last-known values without TTL. These maps are
// consulted only when the Ristretto cache misses AND the server fetch fails.
type CacheStore struct {
	flagCache     *ristretto.Cache[string, *CachedFlagState]
	overrideCache *ristretto.Cache[string, *CachedOverride]

	staleFlagMu      sync.RWMutex
	staleFlagMap     map[string]*CachedFlagState
	staleOverrideMu  sync.RWMutex
	staleOverrideMap map[string]*CachedOverride

	killSetMu sync.RWMutex
	killSet   *KillSet

	flagTTL       time.Duration
	overrideTTL   time.Duration
	jitterPercent int
}

// CacheStoreConfig configures cache sizes and TTLs.
type CacheStoreConfig struct {
	FlagTTL         time.Duration
	OverrideTTL     time.Duration
	OverrideMaxSize int64
	JitterPercent   int
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

	overrideCache, err := ristretto.NewCache(&ristretto.Config[string, *CachedOverride]{
		NumCounters: cfg.OverrideMaxSize * 10,
		MaxCost:     cfg.OverrideMaxSize,
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}

	return &CacheStore{
		flagCache:        flagCache,
		overrideCache:    overrideCache,
		staleFlagMap:     make(map[string]*CachedFlagState),
		staleOverrideMap: make(map[string]*CachedOverride),
		killSet:          &KillSet{FlagIDs: make(map[string]struct{})},
		flagTTL:          cfg.FlagTTL,
		overrideTTL:      cfg.OverrideTTL,
		jitterPercent:    cfg.JitterPercent,
	}, nil
}

// Close releases cache resources.
func (s *CacheStore) Close() {
	s.flagCache.Close()
	s.overrideCache.Close()
}

// FlushAll evicts all entries from the hot caches (Ristretto) but preserves
// the stale fallback maps.
func (s *CacheStore) FlushAll() {
	s.flagCache.Clear()
	s.overrideCache.Clear()
}

// WaitAll blocks until both Ristretto caches have processed all pending
// writes and evictions.
func (s *CacheStore) WaitAll() {
	s.flagCache.Wait()
	s.overrideCache.Wait()
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
func (s *CacheStore) GetFlagState(flagID string) *CachedFlagState {
	val, ok := s.flagCache.Get(flagID)
	if !ok {
		return nil
	}
	return val
}

// SetFlagState caches a flag state with TTL + jitter.
func (s *CacheStore) SetFlagState(state *CachedFlagState) {
	s.flagCache.SetWithTTL(state.FlagID, state, 1, s.jitteredTTL(s.flagTTL))
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

// OverrideCacheSize returns the approximate number of cached overrides.
func (s *CacheStore) OverrideCacheSize() int64 {
	m := s.overrideCache.Metrics
	if m == nil {
		return 0
	}
	return int64(m.KeysAdded() - m.KeysEvicted())
}

// GetOverride returns the cached override, or nil on miss.
func (s *CacheStore) GetOverride(flagID, entityID string) *CachedOverride {
	val, ok := s.overrideCache.Get(flagID + ":" + entityID)
	if !ok {
		return nil
	}
	return val
}

// SetOverride caches an override with TTL + jitter.
func (s *CacheStore) SetOverride(o *CachedOverride) {
	key := o.FlagID + ":" + o.EntityID
	s.overrideCache.SetWithTTL(key, o, 1, s.jitteredTTL(s.overrideTTL))
	s.staleOverrideMu.Lock()
	s.staleOverrideMap[key] = o
	s.staleOverrideMu.Unlock()
}

// GetStaleOverride returns the last-known override from the stale fallback map.
func (s *CacheStore) GetStaleOverride(flagID, entityID string) *CachedOverride {
	s.staleOverrideMu.RLock()
	defer s.staleOverrideMu.RUnlock()
	return s.staleOverrideMap[flagID+":"+entityID]
}

func (s *CacheStore) jitteredTTL(base time.Duration) time.Duration {
	if s.jitterPercent <= 0 {
		return base
	}
	jitterRange := float64(base) * float64(s.jitterPercent) / 100.0
	jitter := (rand.Float64()*2 - 1) * jitterRange
	return base + time.Duration(jitter)
}
