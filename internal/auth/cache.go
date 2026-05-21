package auth

import (
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/google/uuid"
)

// userCache is a small in-memory LRU mapping Auth0 sub → internal user UUID.
// Avoids a DB round-trip on every authenticated request for known users.
type userCache struct {
	c *lru.Cache[string, uuid.UUID]
}

// NewUserCache creates a userCache capped at size entries.
func NewUserCache(size int) (*userCache, error) {
	c, err := lru.New[string, uuid.UUID](size)
	if err != nil {
		return nil, err
	}
	return &userCache{c: c}, nil
}

func (uc *userCache) Get(sub string) (uuid.UUID, bool) { return uc.c.Get(sub) }
func (uc *userCache) Put(sub string, id uuid.UUID)     { uc.c.Add(sub, id) }
func (uc *userCache) Invalidate(sub string)             { uc.c.Remove(sub) }
