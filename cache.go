package main

import (
	"sync"
	"time"
)

const cacheTTL = 15 * time.Minute

type CacheEntry[T any] struct {
	Data      T
	ExpiresAt time.Time
}

func (e *CacheEntry[T]) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

type UserCache struct {
	Issues *CacheEntry[[]IssuesByProject]
	mu     sync.RWMutex
}

type Cache struct {
	users map[string]*UserCache
	mu    sync.RWMutex
}

var cache = &Cache{
	users: make(map[string]*UserCache),
}

func (c *Cache) getUserCache(accountID string) *UserCache {
	c.mu.RLock()
	uc := c.users[accountID]
	c.mu.RUnlock()

	if uc != nil {
		return uc
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if uc = c.users[accountID]; uc != nil {
		return uc
	}

	uc = &UserCache{}
	c.users[accountID] = uc
	return uc
}

func (c *Cache) GetIssues(accountID string) ([]IssuesByProject, bool) {
	uc := c.getUserCache(accountID)

	uc.mu.RLock()
	defer uc.mu.RUnlock()

	if uc.Issues == nil || uc.Issues.IsExpired() {
		return nil, false
	}

	return uc.Issues.Data, true
}

func (c *Cache) SetIssues(accountID string, issues []IssuesByProject) {
	uc := c.getUserCache(accountID)

	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.Issues = &CacheEntry[[]IssuesByProject]{
		Data:      issues,
		ExpiresAt: time.Now().Add(cacheTTL),
	}
}

func (c *Cache) InvalidateIssues(accountID string) {
	uc := c.getUserCache(accountID)

	uc.mu.Lock()
	defer uc.mu.Unlock()

	uc.Issues = nil
}

func (c *Cache) InvalidateAll(accountID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.users, accountID)
}
