package rental

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/worldland/worldland-node/internal/container"
)

// MockDockerService implements DockerServiceInterface for testing
type MockDockerService struct {
	createContainerFunc  func(ctx context.Context, cfg container.ContainerConfig) (string, error)
	startContainerFunc   func(ctx context.Context, containerID string) error
	stopContainerFunc    func(ctx context.Context, containerID string, timeoutSeconds int) error
	removeContainerFunc  func(ctx context.Context, containerID string, force bool) error
	inspectContainerFunc func(ctx context.Context, containerID string) (*container.ContainerInfo, error)

	// Call tracking
	CreateCalls  []container.ContainerConfig
	StartCalls   []string
	StopCalls    []string
	RemoveCalls  []string
	InspectCalls []string
}

func (m *MockDockerService) CreateContainer(ctx context.Context, cfg container.ContainerConfig) (string, error) {
	m.CreateCalls = append(m.CreateCalls, cfg)
	if m.createContainerFunc != nil {
		return m.createContainerFunc(ctx, cfg)
	}
	return "container-123", nil
}

func (m *MockDockerService) StartContainer(ctx context.Context, containerID string) error {
	m.StartCalls = append(m.StartCalls, containerID)
	if m.startContainerFunc != nil {
		return m.startContainerFunc(ctx, containerID)
	}
	return nil
}

func (m *MockDockerService) StopContainer(ctx context.Context, containerID string, timeoutSeconds int) error {
	m.StopCalls = append(m.StopCalls, containerID)
	if m.stopContainerFunc != nil {
		return m.stopContainerFunc(ctx, containerID, timeoutSeconds)
	}
	return nil
}

func (m *MockDockerService) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	m.RemoveCalls = append(m.RemoveCalls, containerID)
	if m.removeContainerFunc != nil {
		return m.removeContainerFunc(ctx, containerID, force)
	}
	return nil
}

func (m *MockDockerService) InspectContainer(ctx context.Context, containerID string) (*container.ContainerInfo, error) {
	m.InspectCalls = append(m.InspectCalls, containerID)
	if m.inspectContainerFunc != nil {
		return m.inspectContainerFunc(ctx, containerID)
	}
	return &container.ContainerInfo{
		ContainerID: containerID,
		SSHPort:     30000,
		State:       "running",
		Health:      "healthy",
	}, nil
}

// MockPortManager implements PortManagerInterface for testing
type MockPortManager struct {
	allocateFunc func(sessionID string) (int, error)
	releaseFunc  func(port int) error

	// Call tracking
	AllocateCalls []string
	ReleaseCalls  []int

	nextPort int
}

func (m *MockPortManager) Allocate(sessionID string) (int, error) {
	m.AllocateCalls = append(m.AllocateCalls, sessionID)
	if m.allocateFunc != nil {
		return m.allocateFunc(sessionID)
	}
	m.nextPort++
	return 30000 + m.nextPort, nil
}

func (m *MockPortManager) Release(port int) error {
	m.ReleaseCalls = append(m.ReleaseCalls, port)
	if m.releaseFunc != nil {
		return m.releaseFunc(port)
	}
	return nil
}

func TestStartRental_AllocatesPortAndCreatesContainer(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	req := StartRentalRequest{
		SessionID:    "session-123",
		Image:        "nvidia/cuda:12.1-runtime-ubuntu22.04",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPassword: "ssh-rsa AAAA...",
		MemoryBytes:  8 * 1024 * 1024 * 1024,
		CPUCount:     4,
		Host:         "provider.example.com",
	}

	connInfo, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, connInfo)

	// Verify port was allocated
	assert.Len(t, mockPort.AllocateCalls, 1)
	assert.Equal(t, "session-123", mockPort.AllocateCalls[0])

	// Verify container was created
	assert.Len(t, mockDocker.CreateCalls, 1)
	assert.Equal(t, "session-123", mockDocker.CreateCalls[0].SessionID)
	assert.Equal(t, "GPU-uuid-456", mockDocker.CreateCalls[0].GPUDeviceID)

	// Verify container was started
	assert.Len(t, mockDocker.StartCalls, 1)
	assert.Equal(t, "container-123", mockDocker.StartCalls[0])
}

func TestStartRental_ReturnsConnectionInfo(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	req := StartRentalRequest{
		SessionID: "session-123",
		Host:      "provider.example.com",
	}

	connInfo, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "provider.example.com", connInfo.Host)
	assert.Equal(t, 30001, connInfo.Port)
	assert.Equal(t, "ubuntu", connInfo.User)
	assert.Equal(t, "ssh -p 30001 ubuntu@provider.example.com", connInfo.Command)
	assert.Equal(t, "container-123", connInfo.ContainerID)
}

func TestStartRental_CleansUpOnContainerCreateFailure(t *testing.T) {
	mockDocker := &MockDockerService{
		createContainerFunc: func(ctx context.Context, cfg container.ContainerConfig) (string, error) {
			return "", errors.New("create failed")
		},
	}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	req := StartRentalRequest{SessionID: "session-123"}

	_, err := executor.StartRental(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create container")

	// Verify port was released
	assert.Len(t, mockPort.ReleaseCalls, 1)
	assert.Equal(t, 30001, mockPort.ReleaseCalls[0])

	// Verify no container was started
	assert.Len(t, mockDocker.StartCalls, 0)
}

func TestStartRental_CleansUpOnStartFailure(t *testing.T) {
	mockDocker := &MockDockerService{
		startContainerFunc: func(ctx context.Context, containerID string) error {
			return errors.New("start failed")
		},
	}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	req := StartRentalRequest{SessionID: "session-123"}

	_, err := executor.StartRental(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start container")

	// Verify container was removed
	assert.Len(t, mockDocker.RemoveCalls, 1)
	assert.Equal(t, "container-123", mockDocker.RemoveCalls[0])

	// Verify port was released
	assert.Len(t, mockPort.ReleaseCalls, 1)
}

func TestStartRental_WaitsForHealthCheck(t *testing.T) {
	inspectCount := 0
	mockDocker := &MockDockerService{
		inspectContainerFunc: func(ctx context.Context, containerID string) (*container.ContainerInfo, error) {
			inspectCount++
			if inspectCount < 3 {
				// First two checks return "starting"
				return &container.ContainerInfo{
					ContainerID: containerID,
					State:       "running",
					Health:      "starting",
				}, nil
			}
			// Third check returns "healthy"
			return &container.ContainerInfo{
				ContainerID: containerID,
				State:       "running",
				Health:      "healthy",
			}, nil
		},
	}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)
	executor.healthInterval = 10 * time.Millisecond // Speed up test

	req := StartRentalRequest{SessionID: "session-123"}

	connInfo, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)
	assert.NotNil(t, connInfo)

	// Verify InspectContainer was called multiple times
	assert.GreaterOrEqual(t, len(mockDocker.InspectCalls), 3)
}

func TestStartRental_FailsOnHealthCheckTimeout(t *testing.T) {
	mockDocker := &MockDockerService{
		inspectContainerFunc: func(ctx context.Context, containerID string) (*container.ContainerInfo, error) {
			// Always return starting
			return &container.ContainerInfo{
				ContainerID: containerID,
				State:       "running",
				Health:      "starting",
			}, nil
		},
	}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)
	executor.healthTimeout = 50 * time.Millisecond  // Short timeout
	executor.healthInterval = 10 * time.Millisecond // Fast polling

	req := StartRentalRequest{SessionID: "session-123"}

	_, err := executor.StartRental(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed health check")
	assert.ErrorIs(t, err, ErrContainerNotHealthy)

	// Verify cleanup occurred
	assert.Len(t, mockDocker.RemoveCalls, 1)
	assert.Len(t, mockPort.ReleaseCalls, 1)
}

func TestStartRental_RejectsDuplicateSession(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	req := StartRentalRequest{SessionID: "session-123"}

	// First start succeeds
	_, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)

	// Second start with same session ID fails
	_, err = executor.StartRental(context.Background(), req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionAlreadyActive)

	// Verify only one container was created
	assert.Len(t, mockDocker.CreateCalls, 1)
}

func TestStopRental_StopsContainer(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	// Start rental first
	req := StartRentalRequest{SessionID: "session-123"}
	_, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)

	// Stop rental
	err = executor.StopRental(context.Background(), "session-123")
	require.NoError(t, err)

	// Verify container was stopped
	assert.Len(t, mockDocker.StopCalls, 1)
	assert.Equal(t, "container-123", mockDocker.StopCalls[0])

	// Verify rental is marked as stopped
	state, err := executor.GetRentalStatus("session-123")
	require.NoError(t, err)
	assert.NotNil(t, state.StoppedAt)
}

func TestStopRental_SchedulesCleanup(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 100*time.Millisecond) // Short grace period

	// Start rental
	req := StartRentalRequest{SessionID: "session-123"}
	_, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)

	// Stop rental
	err = executor.StopRental(context.Background(), "session-123")
	require.NoError(t, err)

	// Cleanup should not have happened yet
	assert.Len(t, mockDocker.RemoveCalls, 0)
	assert.Len(t, mockPort.ReleaseCalls, 0)

	// Wait for grace period
	time.Sleep(150 * time.Millisecond)

	// Cleanup should have happened
	assert.Len(t, mockDocker.RemoveCalls, 1)
	assert.Equal(t, "container-123", mockDocker.RemoveCalls[0])
	assert.Len(t, mockPort.ReleaseCalls, 1)
	assert.Equal(t, 30001, mockPort.ReleaseCalls[0])

	// Rental should be removed from active rentals
	_, err = executor.GetRentalStatus("session-123")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestStopRental_RentalNotFound(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	err := executor.StopRental(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestGetRentalStatus_ReturnsState(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	// Start rental
	req := StartRentalRequest{SessionID: "session-123"}
	_, err := executor.StartRental(context.Background(), req)
	require.NoError(t, err)

	// Get status
	state, err := executor.GetRentalStatus("session-123")
	require.NoError(t, err)
	assert.Equal(t, "session-123", state.SessionID)
	assert.Equal(t, "container-123", state.ContainerID)
	assert.Equal(t, 30001, state.SSHPort)
	assert.Nil(t, state.StoppedAt)
	assert.WithinDuration(t, time.Now(), state.StartedAt, 1*time.Second)
}

func TestListActiveRentals_ReturnsAll(t *testing.T) {
	mockDocker := &MockDockerService{}
	mockPort := &MockPortManager{}
	executor := NewRentalExecutor(mockDocker, mockPort, 1*time.Minute)

	// Start multiple rentals
	req1 := StartRentalRequest{SessionID: "session-1"}
	_, err := executor.StartRental(context.Background(), req1)
	require.NoError(t, err)

	req2 := StartRentalRequest{SessionID: "session-2"}
	_, err = executor.StartRental(context.Background(), req2)
	require.NoError(t, err)

	// List all active rentals
	rentals := executor.ListActiveRentals()
	assert.Len(t, rentals, 2)

	// Verify both sessions are present
	sessionIDs := make(map[string]bool)
	for _, rental := range rentals {
		sessionIDs[rental.SessionID] = true
	}
	assert.True(t, sessionIDs["session-1"])
	assert.True(t, sessionIDs["session-2"])
}
