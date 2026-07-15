package manager

import (
	"strings"
	"sync"
)

type MutationCoordinator struct {
	mu    sync.Mutex
	owner string
}

func NewMutationCoordinator() *MutationCoordinator {
	return &MutationCoordinator{}
}

func (c *MutationCoordinator) TryAcquire(owner string) bool {
	if c == nil || strings.TrimSpace(owner) == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner != "" {
		return false
	}
	c.owner = owner
	return true
}

func (c *MutationCoordinator) Release(owner string) {
	if c == nil || strings.TrimSpace(owner) == "" {
		return
	}
	c.mu.Lock()
	if c.owner == owner {
		c.owner = ""
	}
	c.mu.Unlock()
}
