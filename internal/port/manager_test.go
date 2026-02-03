package port

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllocate_ReturnsFirstAvailablePort(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	port, err := pm.Allocate("session-1")

	require.NoError(t, err)
	assert.Equal(t, 30000, port) // First in range
}

func TestAllocate_ReturnsSequentialPorts(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	port1, _ := pm.Allocate("session-1")
	port2, _ := pm.Allocate("session-2")
	port3, _ := pm.Allocate("session-3")

	assert.Equal(t, 30000, port1)
	assert.Equal(t, 30001, port2)
	assert.Equal(t, 30002, port3)
}

func TestAllocate_FailsWhenRangeExhausted(t *testing.T) {
	pm := NewPortManager(30000, 30002, 30*time.Minute) // Only 3 ports

	_, _ = pm.Allocate("session-1")
	_, _ = pm.Allocate("session-2")
	_, _ = pm.Allocate("session-3")
	_, err := pm.Allocate("session-4")

	assert.ErrorIs(t, err, ErrNoAvailablePorts)
}

func TestRelease_MarksPortAsReleased(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	port, _ := pm.Allocate("session-1")
	err := pm.Release(port)

	require.NoError(t, err)

	alloc, exists := pm.GetAllocation(port)
	assert.True(t, exists)
	assert.NotNil(t, alloc.ReleasedAt)
}

func TestRelease_FailsForUnallocatedPort(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	err := pm.Release(30000)

	assert.ErrorIs(t, err, ErrPortNotAllocated)
}

func TestIsAvailable_TrueForUnusedPort(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	assert.True(t, pm.IsAvailable(30000))
}

func TestIsAvailable_FalseForAllocatedPort(t *testing.T) {
	pm := NewPortManager(30000, 30010, 30*time.Minute)

	port, _ := pm.Allocate("session-1")

	assert.False(t, pm.IsAvailable(port))
}

func TestIsAvailable_FalseDuringGracePeriod(t *testing.T) {
	pm := NewPortManager(30000, 30010, 1*time.Hour) // Long grace period

	port, _ := pm.Allocate("session-1")
	_ = pm.Release(port)

	assert.False(t, pm.IsAvailable(port)) // Still in grace period
}

func TestAllocate_ReusesReleasedPortAfterGracePeriod(t *testing.T) {
	pm := NewPortManager(30000, 30002, 0) // Zero grace period for test

	// Allocate all 3 ports
	_, _ = pm.Allocate("session-1")
	port2, _ := pm.Allocate("session-2")
	_, _ = pm.Allocate("session-3")

	// Release middle port
	_ = pm.Release(port2)

	// Should reuse released port (grace period is 0)
	port, err := pm.Allocate("session-4")
	require.NoError(t, err)
	assert.Equal(t, port2, port)
}

func TestAvailableCount_ReturnsCorrectCount(t *testing.T) {
	pm := NewPortManager(30000, 30009, 30*time.Minute) // 10 ports

	assert.Equal(t, 10, pm.AvailableCount())

	_, _ = pm.Allocate("session-1")
	_, _ = pm.Allocate("session-2")

	assert.Equal(t, 8, pm.AvailableCount())
}

func TestConcurrentAllocations(t *testing.T) {
	pm := NewPortManager(30000, 30099, 30*time.Minute) // 100 ports

	done := make(chan int, 50)

	// Spawn 50 goroutines allocating ports
	for i := 0; i < 50; i++ {
		go func(sessionNum int) {
			port, err := pm.Allocate(fmt.Sprintf("session-%d", sessionNum))
			if err != nil {
				done <- -1
			} else {
				done <- port
			}
		}(i)
	}

	// Collect results
	ports := make(map[int]bool)
	for i := 0; i < 50; i++ {
		port := <-done
		require.NotEqual(t, -1, port, "allocation should succeed")
		require.False(t, ports[port], "port should be unique")
		ports[port] = true
	}

	assert.Len(t, ports, 50) // 50 unique ports allocated
}
