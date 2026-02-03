package port

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrNoAvailablePorts = errors.New("no available ports in range")
	ErrPortNotAllocated = errors.New("port not allocated")
)

// Allocation tracks a single port allocation
type Allocation struct {
	SessionID   string
	AllocatedAt time.Time
	ReleasedAt  *time.Time // nil if still in use
}

// PortManager manages SSH port allocation for containers
type PortManager struct {
	mu          sync.Mutex
	minPort     int
	maxPort     int
	gracePeriod time.Duration // Time before released port can be reused
	allocations map[int]*Allocation
}

// NewPortManager creates a new port manager
// minPort: start of port range (inclusive)
// maxPort: end of port range (inclusive)
// gracePeriod: wait time before reusing released ports
func NewPortManager(minPort, maxPort int, gracePeriod time.Duration) *PortManager {
	return &PortManager{
		minPort:     minPort,
		maxPort:     maxPort,
		gracePeriod: gracePeriod,
		allocations: make(map[int]*Allocation),
	}
}

// Allocate finds and reserves an available port for the given session
// Returns the allocated port number or error if no ports available
func (pm *PortManager) Allocate(sessionID string) (int, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()

	// Scan for available port
	for port := pm.minPort; port <= pm.maxPort; port++ {
		alloc, exists := pm.allocations[port]

		if !exists {
			// Port never used, allocate it
			pm.allocations[port] = &Allocation{
				SessionID:   sessionID,
				AllocatedAt: now,
			}
			return port, nil
		}

		// Check if port was released and grace period expired
		if alloc.ReleasedAt != nil && now.Sub(*alloc.ReleasedAt) >= pm.gracePeriod {
			// Reuse port
			pm.allocations[port] = &Allocation{
				SessionID:   sessionID,
				AllocatedAt: now,
			}
			return port, nil
		}
	}

	return 0, ErrNoAvailablePorts
}

// Release marks a port as released (starts grace period countdown)
func (pm *PortManager) Release(port int) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	alloc, exists := pm.allocations[port]
	if !exists || alloc.ReleasedAt != nil {
		return ErrPortNotAllocated
	}

	now := time.Now()
	alloc.ReleasedAt = &now
	return nil
}

// IsAvailable checks if a port is currently available for allocation
func (pm *PortManager) IsAvailable(port int) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	alloc, exists := pm.allocations[port]
	if !exists {
		return true
	}

	if alloc.ReleasedAt != nil && time.Since(*alloc.ReleasedAt) >= pm.gracePeriod {
		return true
	}

	return false
}

// GetAllocation returns the current allocation for a port (for debugging/monitoring)
func (pm *PortManager) GetAllocation(port int) (*Allocation, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	alloc, exists := pm.allocations[port]
	if !exists {
		return nil, false
	}
	// Return copy to prevent external mutation
	copy := *alloc
	return &copy, true
}

// AvailableCount returns the number of currently available ports
func (pm *PortManager) AvailableCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	count := 0
	now := time.Now()

	for port := pm.minPort; port <= pm.maxPort; port++ {
		alloc, exists := pm.allocations[port]
		if !exists {
			count++
			continue
		}
		if alloc.ReleasedAt != nil && now.Sub(*alloc.ReleasedAt) >= pm.gracePeriod {
			count++
		}
	}

	return count
}
