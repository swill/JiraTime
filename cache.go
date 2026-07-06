package main

import (
	"strings"
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
	// Site-level caches (shared across users of the same Jira site)
	projectSubtasks   map[string]*CacheEntry[[]string]        // keyed by "{cloudID}/{projectKey}"
	projectIssueTypes map[string]*CacheEntry[[]JiraIssueType] // keyed by "{cloudID}/{projectKey}"
	issueTypes        map[string]*CacheEntry[map[string]string] // keyed by cloudID
	mu                sync.RWMutex
}

var cache = &Cache{
	users:             make(map[string]*UserCache),
	projectSubtasks:   make(map[string]*CacheEntry[[]string]),
	projectIssueTypes: make(map[string]*CacheEntry[[]JiraIssueType]),
	issueTypes:        make(map[string]*CacheEntry[map[string]string]),
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

func (c *Cache) GetProjectSubtasks(cloudID, projectKey string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry := c.projectSubtasks[cloudID+"/"+projectKey]
	if entry == nil || entry.IsExpired() {
		return nil, false
	}

	return entry.Data, true
}

func (c *Cache) SetProjectSubtasks(cloudID, projectKey string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.projectSubtasks[cloudID+"/"+projectKey] = &CacheEntry[[]string]{
		Data:      ids,
		ExpiresAt: time.Now().Add(cacheTTL),
	}
}

func (c *Cache) GetProjectIssueTypes(cloudID, projectKey string) ([]JiraIssueType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry := c.projectIssueTypes[cloudID+"/"+projectKey]
	if entry == nil || entry.IsExpired() {
		return nil, false
	}

	return entry.Data, true
}

func (c *Cache) SetProjectIssueTypes(cloudID, projectKey string, types []JiraIssueType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.projectIssueTypes[cloudID+"/"+projectKey] = &CacheEntry[[]JiraIssueType]{
		Data:      types,
		ExpiresAt: time.Now().Add(cacheTTL),
	}
}

func (c *Cache) GetIssueTypes(cloudID string) (map[string]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry := c.issueTypes[cloudID]
	if entry == nil || entry.IsExpired() {
		return nil, false
	}

	return entry.Data, true
}

func (c *Cache) SetIssueTypes(cloudID string, names map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.issueTypes[cloudID] = &CacheEntry[map[string]string]{
		Data:      names,
		ExpiresAt: time.Now().Add(cacheTTL),
	}
}

// InvalidateSite clears site-level caches for a cloud ID (used by manual refresh
// so billable sub-task config changes are picked up promptly)
func (c *Cache) InvalidateSite(cloudID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.issueTypes, cloudID)
	for key := range c.projectSubtasks {
		if strings.HasPrefix(key, cloudID+"/") {
			delete(c.projectSubtasks, key)
		}
	}
	for key := range c.projectIssueTypes {
		if strings.HasPrefix(key, cloudID+"/") {
			delete(c.projectIssueTypes, key)
		}
	}
}
