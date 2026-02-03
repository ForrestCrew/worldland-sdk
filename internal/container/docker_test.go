package container

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockDockerClient implements DockerClient interface for testing
type MockDockerClient struct {
	// Track method calls
	CreateCalled   int
	StartCalled    int
	StopCalled     int
	RemoveCalled   int
	InspectCalled  int
	WaitCalled     int
	CloseCalled    int

	// Configurable return values
	CreateResponse container.CreateResponse
	CreateError    error

	StartErrors []error // For testing retry logic
	startCallIdx int

	StopError error

	RemoveError error

	InspectResponse types.ContainerJSON
	InspectError    error

	WaitResponse container.WaitResponse
	WaitError    error

	// Track arguments
	LastCreateConfig *container.Config
	LastHostConfig   *container.HostConfig
	LastContainerName string
}

func (m *MockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error) {
	m.CreateCalled++
	m.LastCreateConfig = config
	m.LastHostConfig = hostConfig
	m.LastContainerName = containerName
	return m.CreateResponse, m.CreateError
}

func (m *MockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	m.StartCalled++
	if len(m.StartErrors) > 0 {
		if m.startCallIdx < len(m.StartErrors) {
			err := m.StartErrors[m.startCallIdx]
			m.startCallIdx++
			return err
		}
		// If we've exhausted the error list, return the last error indefinitely
		return m.StartErrors[len(m.StartErrors)-1]
	}
	return nil
}

func (m *MockDockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	m.StopCalled++
	return m.StopError
}

func (m *MockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	m.RemoveCalled++
	return m.RemoveError
}

func (m *MockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	m.InspectCalled++
	return m.InspectResponse, m.InspectError
}

func (m *MockDockerClient) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	m.WaitCalled++
	waitCh := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)

	if m.WaitError != nil {
		errCh <- m.WaitError
	} else {
		waitCh <- m.WaitResponse
	}

	return waitCh, errCh
}

func (m *MockDockerClient) Close() error {
	m.CloseCalled++
	return nil
}

func TestCreateContainer_Success(t *testing.T) {
	mock := &MockDockerClient{
		CreateResponse: container.CreateResponse{
			ID: "container-123",
		},
	}

	svc := NewDockerServiceWithClient(mock)

	cfg := ContainerConfig{
		SessionID:    "session-abc",
		Image:        "nvidia/cuda:12.1-runtime-ubuntu22.04",
		GPUDeviceID:  "GPU-uuid-123",
		SSHPublicKey: "ssh-rsa AAAAB3...",
		MemoryBytes:  8 * 1024 * 1024 * 1024, // 8GB
		CPUCount:     4,
	}

	containerID, err := svc.CreateContainer(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, "container-123", containerID)
	assert.Equal(t, 1, mock.CreateCalled)
	assert.Equal(t, "session-abc", mock.LastContainerName)
	assert.Equal(t, cfg.Image, mock.LastCreateConfig.Image)
	assert.Contains(t, mock.LastCreateConfig.Env, "PUBLIC_KEY=ssh-rsa AAAAB3...")
	assert.Contains(t, mock.LastCreateConfig.Env, "USER_NAME=ubuntu")
	assert.Contains(t, mock.LastCreateConfig.Env, "SUDO_ACCESS=true")
	assert.NotNil(t, mock.LastCreateConfig.ExposedPorts["22/tcp"])
	assert.True(t, mock.LastHostConfig.PublishAllPorts)
	assert.Equal(t, cfg.MemoryBytes, mock.LastHostConfig.Resources.Memory)
	assert.Equal(t, cfg.CPUCount*1e9, mock.LastHostConfig.Resources.NanoCPUs)
}

func TestCreateContainer_SetsGPUDeviceRequest(t *testing.T) {
	mock := &MockDockerClient{
		CreateResponse: container.CreateResponse{
			ID: "container-123",
		},
	}

	svc := NewDockerServiceWithClient(mock)

	cfg := ContainerConfig{
		SessionID:    "session-abc",
		Image:        "nvidia/cuda:12.1-runtime-ubuntu22.04",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAAB3...",
		MemoryBytes:  4 * 1024 * 1024 * 1024,
		CPUCount:     2,
	}

	_, err := svc.CreateContainer(context.Background(), cfg)

	require.NoError(t, err)
	assert.Equal(t, 1, len(mock.LastHostConfig.Resources.DeviceRequests))
	deviceReq := mock.LastHostConfig.Resources.DeviceRequests[0]
	assert.Equal(t, "nvidia", deviceReq.Driver)
	assert.Equal(t, []string{"GPU-uuid-456"}, deviceReq.DeviceIDs)
	assert.Equal(t, [][]string{{"gpu"}}, deviceReq.Capabilities)
}

func TestStartContainer_Success(t *testing.T) {
	mock := &MockDockerClient{}
	svc := NewDockerServiceWithClient(mock)

	err := svc.StartContainer(context.Background(), "container-123")

	require.NoError(t, err)
	assert.Equal(t, 1, mock.StartCalled)
}

func TestStartContainer_RetriesOnTransientFailure(t *testing.T) {
	mock := &MockDockerClient{
		StartErrors: []error{
			errors.New("transient error 1"),
			errors.New("transient error 2"),
			nil, // Success on third attempt
		},
	}
	svc := NewDockerServiceWithClient(mock)

	err := svc.StartContainer(context.Background(), "container-123")

	require.NoError(t, err)
	assert.Equal(t, 3, mock.StartCalled, "Should retry twice then succeed")
}

func TestStartContainer_FailsAfterMaxRetries(t *testing.T) {
	mock := &MockDockerClient{
		StartErrors: []error{
			errors.New("permanent error"),
			errors.New("permanent error"),
			errors.New("permanent error"),
			errors.New("permanent error"),
			errors.New("permanent error"),
		},
	}
	svc := NewDockerServiceWithClient(mock)

	err := svc.StartContainer(context.Background(), "container-123")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start container after retries")
	// Should have multiple retry attempts within 30s timeout
	assert.GreaterOrEqual(t, mock.StartCalled, 3, "Should attempt multiple retries")
}

func TestStopContainer_GracefulShutdown(t *testing.T) {
	mock := &MockDockerClient{
		WaitResponse: container.WaitResponse{
			StatusCode: 0,
		},
	}
	svc := NewDockerServiceWithClient(mock)

	err := svc.StopContainer(context.Background(), "container-123", 10)

	require.NoError(t, err)
	assert.Equal(t, 1, mock.StopCalled)
	assert.Equal(t, 1, mock.WaitCalled)
}

func TestRemoveContainer_Success(t *testing.T) {
	mock := &MockDockerClient{}
	svc := NewDockerServiceWithClient(mock)

	err := svc.RemoveContainer(context.Background(), "container-123", false)

	require.NoError(t, err)
	assert.Equal(t, 1, mock.RemoveCalled)
}

func TestInspectContainer_ReturnsSSHPort(t *testing.T) {
	mock := &MockDockerClient{
		InspectResponse: types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				ID: "container-123",
				State: &types.ContainerState{
					Status: "running",
				},
			},
			NetworkSettings: &types.NetworkSettings{
				NetworkSettingsBase: types.NetworkSettingsBase{
					Ports: nat.PortMap{
						"22/tcp": []nat.PortBinding{
							{HostPort: "30123"},
						},
					},
				},
			},
		},
	}
	svc := NewDockerServiceWithClient(mock)

	info, err := svc.InspectContainer(context.Background(), "container-123")

	require.NoError(t, err)
	assert.Equal(t, "container-123", info.ContainerID)
	assert.Equal(t, 30123, info.SSHPort)
	assert.Equal(t, "running", info.State)
	assert.Equal(t, 1, mock.InspectCalled)
}

func TestInspectContainer_ReturnsHealthStatus(t *testing.T) {
	mock := &MockDockerClient{
		InspectResponse: types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				ID: "container-123",
				State: &types.ContainerState{
					Status: "running",
					Health: &types.Health{
						Status: "healthy",
					},
				},
			},
			NetworkSettings: &types.NetworkSettings{
				NetworkSettingsBase: types.NetworkSettingsBase{
					Ports: nat.PortMap{
						"22/tcp": []nat.PortBinding{
							{HostPort: "30456"},
						},
					},
				},
			},
		},
	}
	svc := NewDockerServiceWithClient(mock)

	info, err := svc.InspectContainer(context.Background(), "container-123")

	require.NoError(t, err)
	assert.Equal(t, "container-123", info.ContainerID)
	assert.Equal(t, "healthy", info.Health)
	assert.Equal(t, "running", info.State)
}
