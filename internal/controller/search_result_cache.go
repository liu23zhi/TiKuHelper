package controller

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/model"
)

const (
	defaultSearchResultCacheTTL = 10 * time.Minute
	searchResultCacheProvider   = "cache"
)

type searchResultCacheEntry struct {
	answers   [][]string
	expiresAt time.Time
}

type searchResultCache struct {
	mu      sync.RWMutex
	entries map[string]searchResultCacheEntry
	ttl     time.Duration
	now     func() time.Time
}

type searchResultCacheKey struct {
	AccessTokenID int32    `json:"accessTokenId"`
	Question      string   `json:"question"`
	Options       []string `json:"options"`
}

func newSearchResultCache(ttl time.Duration, now func() time.Time) *searchResultCache {
	if ttl <= 0 {
		ttl = defaultSearchResultCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &searchResultCache{
		entries: make(map[string]searchResultCacheEntry),
		ttl:     ttl,
		now:     now,
	}
}

var defaultSearchResultCache = newSearchResultCache(defaultSearchResultCacheTTL, time.Now)

func canUseSearchResultCache(resolved resolvedSearchRequestContext, useQuery string) bool {
	if resolved.SearchMode != searchModeNormal {
		return false
	}
	return !strings.Contains(useQuery, searchBillingProviderLocal) && !strings.Contains(useQuery, searchModeAI)
}

func buildSearchResultCacheKey(accessTokenID int32, req model.SearchRequest) string {
	payload, _ := json.Marshal(searchResultCacheKey{
		AccessTokenID: accessTokenID,
		Question:      req.Question,
		Options:       req.Options,
	})
	return string(payload)
}

func shouldBypassSearchResultCache(req model.SearchRequest) bool {
	return req.SkipCache != nil && *req.SkipCache
}

func (s *searchResultCache) get(key string) ([][]string, bool) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(s.entries, key)
		return nil, false
	}
	return cloneSearchAnswers(entry.answers), true
}

func (s *searchResultCache) put(key string, answers [][]string) {
	if key == "" || !hasUsableSearchAnswers(answers) {
		return
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupExpiredLocked(now)
	s.entries[key] = searchResultCacheEntry{
		answers:   cloneSearchAnswers(answers),
		expiresAt: now.Add(s.ttl),
	}
}

func (s *searchResultCache) cleanupExpiredLocked(now time.Time) {
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

func hasUsableSearchAnswers(answers [][]string) bool {
	for _, group := range answers {
		if len(group) > 0 {
			return true
		}
	}
	return false
}

func cloneSearchAnswers(answers [][]string) [][]string {
	cloned := make([][]string, len(answers))
	for i := range answers {
		cloned[i] = append([]string(nil), answers[i]...)
	}
	return cloned
}
