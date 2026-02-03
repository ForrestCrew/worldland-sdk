package rental

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/worldland/worldland-node/internal/container"
)

var (
	ErrSessionAlreadyActive = errors.New("session already has active rental")
	ErrSessionNotFound      = errors.New("rental session not found")
	ErrContainerNotHealthy  = errors.New("container failed health check within timeout")
)

// RentalState tracks an active rental's runtime state
type RentalState struct {
	SessionID   string
	ContainerID string
	SSHPort     int
	StartedAt   time.Time
	StoppedAt   *time.Time
}

// ConnectionInfo provides SSH connection details for the user
type ConnectionInfo struct {
	Host       string // Host IP or domain
	Port       int    // SSH port
	User       string // SSH username (ubuntu)
	Command    string // Ready-to-use SSH command
	ContainerID string
}

// StartRentalRequest contains parameters for starting a rental
type StartRentalRequest struct {
	SessionID    string
	Image        string
	GPUDeviceID  string
	SSHPublicKey string
	MemoryBytes  int64
	CPUCount     int64
	Host         string // Host address for SSH command (e.g., "provider.example.com")
}

// DockerServiceInterface defines operations needed from Docker service
type DockerServiceInterface interface {
	CreateContainer(ctx context.Context, cfg container.ContainerConfig) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	InspectContainer(ctx context.Context, containerID string) (*container.ContainerInfo, error)
}

// PortManagerInterface defines operations needed from port manager
type PortManagerInterface interface {
	Allocate(sessionID string) (int, error)
	Release(port int) error
}

// RentalExecutor orchestrates container lifecycle for GPU rentals
type RentalExecutor struct {
	docker         DockerServiceInterface
	portManager    PortManagerInterface
	mu             sync.RWMutex
	activeRentals  map[string]*RentalState // sessionID -> RentalState
	gracePeriod    time.Duration           // Time before container cleanup
	healthTimeout  time.Duration           // Max time to wait for health check
	healthInterval time.Duration           // Interval between health checks
}

// NewRentalExecutor creates a new rental executor
func NewRentalExecutor(docker DockerServiceInterface, portManager PortManagerInterface, gracePeriod time.Duration) *RentalExecutor {
	return &RentalExecutor{
		docker:         docker,
		portManager:    portManager,
		activeRentals:  make(map[string]*RentalState),
		gracePeriod:    gracePeriod,
		healthTimeout:  60 * time.Second, // Per RESEARCH.md Pattern 2
		healthInterval: 2 * time.Second,
	}
}

// StartRental allocates port, creates container, starts it, waits for health, returns connection info
func (re *RentalExecutor) StartRental(ctx context.Context, req StartRentalRequest) (*ConnectionInfo, error) {
	// Check for duplicate session
	re.mu.Lock()
	if _, exists := re.activeRentals[req.SessionID]; exists {
		re.mu.Unlock()
		return nil, ErrSessionAlreadyActive
	}
	re.mu.Unlock()

	// Allocate SSH port
	sshPort, err := re.portManager.Allocate(req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate port: %w", err)
	}

	// Cleanup on failure (defer pattern)
	var containerID string
	cleanupOnError := func() {
		if containerID != "" {
			// Remove container if created
			_ = re.docker.RemoveContainer(context.Background(), containerID, true)
		}
		// Release port
		_ = re.portManager.Release(sshPort)
	}

	// Create container
	containerConfig := container.ContainerConfig{
		SessionID:    req.SessionID,
		Image:        req.Image,
		GPUDeviceID:  req.GPUDeviceID,
		SSHPublicKey: req.SSHPublicKey,
		MemoryBytes:  req.MemoryBytes,
		CPUCount:     req.CPUCount,
	}

	containerID, err = re.docker.CreateContainer(ctx, containerConfig)
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Start container with retry
	if err := re.docker.StartContainer(ctx, containerID); err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	// Wait for health check
	if err := re.waitForHealth(ctx, containerID); err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("failed health check: %w", err)
	}

	// Track active rental
	state := &RentalState{
		SessionID:   req.SessionID,
		ContainerID: containerID,
		SSHPort:     sshPort,
		StartedAt:   time.Now(),
	}

	re.mu.Lock()
	re.activeRentals[req.SessionID] = state
	re.mu.Unlock()

	// Return connection info
	connInfo := &ConnectionInfo{
		Host:        req.Host,
		Port:        sshPort,
		User:        "ubuntu",
		Command:     fmt.Sprintf("ssh -p %d ubuntu@%s", sshPort, req.Host),
		ContainerID: containerID,
	}

	return connInfo, nil
}

// waitForHealth polls container health until healthy or timeout
func (re *RentalExecutor) waitForHealth(ctx context.Context, containerID string) error {
	deadline := time.Now().Add(re.healthTimeout)

	for {
		// Check timeout
		if time.Now().After(deadline) {
			return ErrContainerNotHealthy
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Inspect container
		info, err := re.docker.InspectContainer(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to inspect container: %w", err)
		}

		// Check if container is running and healthy
		if info.State == "running" {
			// If no health check defined, consider running as healthy
			if info.Health == "" || info.Health == "healthy" {
				return nil
			}
			// If health check exists but still starting, continue polling
			if info.Health == "starting" {
				time.Sleep(re.healthInterval)
				continue
			}
			// If health check failed, return error
			if info.Health == "unhealthy" {
				return ErrContainerNotHealthy
			}
		}

		// If container stopped or failed, return error
		if info.State == "exited" || info.State == "dead" {
			return fmt.Errorf("container stopped during startup: state=%s", info.State)
		}

		// Wait before next poll
		time.Sleep(re.healthInterval)
	}
}

// StopRental stops the container and schedules cleanup after grace period
func (re *RentalExecutor) StopRental(ctx context.Context, sessionID string) error {
	re.mu.Lock()
	state, exists := re.activeRentals[sessionID]
	if !exists {
		re.mu.Unlock()
		return ErrSessionNotFound
	}

	// Mark as stopped
	now := time.Now()
	state.StoppedAt = &now
	re.mu.Unlock()

	// Stop container gracefully
	if err := re.docker.StopContainer(ctx, state.ContainerID, 10); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	// Schedule cleanup in background after grace period
	go re.scheduleCleanup(sessionID, state.ContainerID, state.SSHPort)

	return nil
}

// scheduleCleanup waits for grace period then removes container and releases port
func (re *RentalExecutor) scheduleCleanup(sessionID, containerID string, sshPort int) {
	time.Sleep(re.gracePeriod)

	// Remove container
	ctx := context.Background()
	_ = re.docker.RemoveContainer(ctx, containerID, true)

	// Release port
	_ = re.portManager.Release(sshPort)

	// Remove from active rentals
	re.mu.Lock()
	delete(re.activeRentals, sessionID)
	re.mu.Unlock()
}

// GetRentalStatus returns the current state of a rental
func (re *RentalExecutor) GetRentalStatus(sessionID string) (*RentalState, error) {
	re.mu.RLock()
	defer re.mu.RUnlock()

	state, exists := re.activeRentals[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	// Return copy to prevent external mutation
	stateCopy := *state
	if state.StoppedAt != nil {
		t := *state.StoppedAt
		stateCopy.StoppedAt = &t
	}

	return &stateCopy, nil
}

// ListActiveRentals returns all active rental states
func (re *RentalExecutor) ListActiveRentals() []*RentalState {
	re.mu.RLock()
	defer re.mu.RUnlock()

	rentals := make([]*RentalState, 0, len(re.activeRentals))
	for _, state := range re.activeRentals {
		// Return copy
		stateCopy := *state
		if state.StoppedAt != nil {
			t := *state.StoppedAt
			stateCopy.StoppedAt = &t
		}
		rentals = append(rentals, &stateCopy)
	}

	return rentals
}
