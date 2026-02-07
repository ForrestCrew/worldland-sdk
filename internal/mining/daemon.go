package mining

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/worldland/worldland-node/internal/container"
)

// MiningState represents the current state of the mining daemon
type MiningState string

const (
	MiningStateStopped MiningState = "stopped"
	MiningStateRunning MiningState = "running"
	MiningStatePaused  MiningState = "paused" // Paused for rental
)

// MiningStatus contains current mining daemon status
type MiningStatus struct {
	State       MiningState `json:"state"`
	ContainerID string      `json:"containerId,omitempty"`
	GPUCount    int         `json:"gpuCount"`
	StartedAt   *time.Time  `json:"startedAt,omitempty"`
	PausedAt    *time.Time  `json:"pausedAt,omitempty"`
}

// MiningDaemon manages a Docker-based Worldland mining container.
// It automatically mines when GPUs are idle and pauses for rentals.
//
// Reference: worldland-proxy/internal/provider/mining_manager.go
type MiningDaemon struct {
	docker *container.DockerService
	config MiningConfig

	mu          sync.Mutex
	state       MiningState
	containerID string
	startedAt   *time.Time
	pausedAt    *time.Time
	pausedGPUs  map[string]bool // GPU UUIDs currently rented out

	stopCh chan struct{}
}

// NewMiningDaemon creates a new mining daemon
func NewMiningDaemon(docker *container.DockerService, config MiningConfig) *MiningDaemon {
	return &MiningDaemon{
		docker:     docker,
		config:     config,
		state:      MiningStateStopped,
		pausedGPUs: make(map[string]bool),
		stopCh:     make(chan struct{}),
	}
}

// Start starts the mining container with available GPUs
func (m *MiningDaemon) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		log.Println("Mining daemon disabled by configuration")
		return nil
	}

	if m.state == MiningStateRunning {
		log.Println("Mining daemon already running")
		return nil
	}

	// Build container args for worldland-mio node
	// Equivalent to: --mio --datadir /worldland/data --syncmode full --http ...
	mioArgs := []string{
		"--mio",
		"--datadir", "/worldland/data",
		"--syncmode", "full",
		"--http",
		"--http.addr", "0.0.0.0",
		"--http.api", "eth,net,web3,personal,admin,miner",
		"--http.corsdomain", "*",
	}
	mioArgs = append(mioArgs, m.config.ExtraArgs...)

	// Determine available GPUs (exclude rented ones)
	availableGPUs := m.getAvailableGPUs()
	if len(availableGPUs) == 0 {
		log.Println("No available GPUs for mining, pausing")
		m.state = MiningStatePaused
		now := time.Now()
		m.pausedAt = &now
		return nil
	}

	// Use first available GPU for mining container
	// In future: support multi-GPU mining
	gpuDeviceID := availableGPUs[0]

	containerConfig := container.ContainerConfig{
		SessionID:    "worldland-mining",
		Image:        m.config.Image,
		GPUDeviceID:  gpuDeviceID,
		SSHPublicKey: "", // No SSH needed for mining
		MemoryBytes:  8 * 1024 * 1024 * 1024, // 8GB
		CPUCount:     2,
	}

	containerID, err := m.docker.CreateContainer(ctx, containerConfig)
	if err != nil {
		return fmt.Errorf("failed to create mining container: %w", err)
	}

	if err := m.docker.StartContainer(ctx, containerID); err != nil {
		// Cleanup on failure
		_ = m.docker.RemoveContainer(ctx, containerID, true)
		return fmt.Errorf("failed to start mining container: %w", err)
	}

	m.containerID = containerID
	m.state = MiningStateRunning
	now := time.Now()
	m.startedAt = &now
	m.pausedAt = nil

	log.Printf("Mining daemon started: container=%s gpu=%s image=%s",
		containerID[:12], gpuDeviceID, m.config.Image)

	return nil
}

// Stop stops the mining container completely
func (m *MiningDaemon) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == MiningStateStopped {
		return nil
	}

	if m.containerID != "" {
		if err := m.docker.StopContainer(ctx, m.containerID, 10); err != nil {
			log.Printf("Warning: failed to stop mining container: %v", err)
		}
		if err := m.docker.RemoveContainer(ctx, m.containerID, true); err != nil {
			log.Printf("Warning: failed to remove mining container: %v", err)
		}
		log.Printf("Mining container stopped: %s", m.containerID[:12])
	}

	m.containerID = ""
	m.state = MiningStateStopped
	m.startedAt = nil
	m.pausedAt = nil

	return nil
}

// PauseForRental pauses mining to release GPUs for a rental.
// If all GPUs are rented, mining stops entirely.
// If some GPUs remain, mining continues with remaining GPUs.
func (m *MiningDaemon) PauseForRental(ctx context.Context, gpuDeviceIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Mark GPUs as rented
	for _, gpu := range gpuDeviceIDs {
		m.pausedGPUs[gpu] = true
	}

	log.Printf("GPU(s) allocated for rental: %v (total rented: %d)", gpuDeviceIDs, len(m.pausedGPUs))

	// If mining is running, we need to restart with fewer GPUs or stop
	if m.state == MiningStateRunning && m.containerID != "" {
		// Stop current mining container
		if err := m.docker.StopContainer(ctx, m.containerID, 10); err != nil {
			log.Printf("Warning: failed to stop mining for rental: %v", err)
		}
		_ = m.docker.RemoveContainer(ctx, m.containerID, true)
		m.containerID = ""

		// Check if we have remaining GPUs
		available := m.getAvailableGPUs()
		if len(available) == 0 {
			m.state = MiningStatePaused
			now := time.Now()
			m.pausedAt = &now
			log.Println("Mining paused: all GPUs allocated for rentals")
			return nil
		}

		// Restart mining with remaining GPUs (in background)
		m.mu.Unlock()
		go func() {
			restartCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.Start(restartCtx); err != nil {
				log.Printf("Failed to restart mining with remaining GPUs: %v", err)
			}
		}()
		m.mu.Lock()
	}

	return nil
}

// ResumeAfterRental resumes mining after a rental ends and GPUs become available.
func (m *MiningDaemon) ResumeAfterRental(ctx context.Context, gpuDeviceIDs []string) error {
	m.mu.Lock()

	// Unmark GPUs from rental
	for _, gpu := range gpuDeviceIDs {
		delete(m.pausedGPUs, gpu)
	}

	log.Printf("GPU(s) returned from rental: %v (remaining rented: %d)", gpuDeviceIDs, len(m.pausedGPUs))

	// If mining was paused and GPUs are now available, restart
	if m.state == MiningStatePaused || m.state == MiningStateStopped {
		m.mu.Unlock()
		return m.Start(ctx)
	}

	m.mu.Unlock()
	return nil
}

// Status returns the current mining status
func (m *MiningDaemon) Status() MiningStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	return MiningStatus{
		State:       m.state,
		ContainerID: m.containerID,
		GPUCount:    len(m.getAvailableGPUs()),
		StartedAt:   m.startedAt,
		PausedAt:    m.pausedAt,
	}
}

// getAvailableGPUs returns GPUs not currently rented (caller must hold lock)
func (m *MiningDaemon) getAvailableGPUs() []string {
	if len(m.config.GPUDeviceIDs) == 0 {
		// If no specific GPUs configured, return a default "all" device
		if len(m.pausedGPUs) > 0 {
			return nil
		}
		return []string{"all"}
	}

	available := make([]string, 0)
	for _, gpu := range m.config.GPUDeviceIDs {
		if !m.pausedGPUs[gpu] {
			available = append(available, gpu)
		}
	}
	return available
}

// MonitorLoop runs a background loop that checks mining container health
// and restarts it if it crashes
func (m *MiningDaemon) MonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.state == MiningStateRunning && m.containerID != "" {
				// Check container health
				info, err := m.docker.InspectContainer(ctx, m.containerID)
				if err != nil || info.State != "running" {
					log.Printf("Mining container died, restarting...")
					m.containerID = ""
					m.state = MiningStateStopped
					m.mu.Unlock()

					// Restart in background
					restartCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					if err := m.Start(restartCtx); err != nil {
						log.Printf("Failed to restart mining: %v", err)
					}
					cancel()
					continue
				}
			}
			m.mu.Unlock()
		}
	}
}

// Close stops monitoring and cleans up
func (m *MiningDaemon) Close() {
	close(m.stopCh)
}
