package container

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContainerConfig holds configuration for creating a GPU container
type ContainerConfig struct {
	SessionID    string // Used as container name
	Image        string // e.g., "nvidia/cuda:12.1.1-runtime-ubuntu22.04"
	GPUDeviceID  string // NVIDIA UUID (not index)
	SSHPublicKey string // User's SSH public key
	MemoryBytes  int64  // Memory limit in bytes
	CPUCount     int64  // CPU count (in NanoCPUs / 1e9)
}

// ContainerInfo contains information about a running container
type ContainerInfo struct {
	ContainerID string
	SSHPort     int    // Dynamically allocated host port for SSH
	State       string // "running", "exited", etc.
	Health      string // "healthy", "unhealthy", "starting", ""
}

// DockerService wraps Docker SDK for GPU container management
type DockerService struct {
	cli DockerClient // Interface for testability
}

// DockerClient interface for Docker operations (mockable)
type DockerClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *specs.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	Close() error
}

// Compile-time interface check
var _ DockerClient = (*client.Client)(nil)

// NewDockerService creates a new DockerService with Docker client
func NewDockerService() (*DockerService, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &DockerService{cli: cli}, nil
}

// NewDockerServiceWithClient creates a DockerService with a provided client (for testing)
func NewDockerServiceWithClient(cli DockerClient) *DockerService {
	return &DockerService{cli: cli}
}

// CreateContainer creates a GPU container with NVIDIA runtime
func (s *DockerService) CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	// Expose SSH port
	exposedPorts := nat.PortSet{
		"22/tcp": struct{}{},
	}

	// Environment variables for SSH setup
	envVars := []string{
		fmt.Sprintf("PUBLIC_KEY=%s", cfg.SSHPublicKey),
		"USER_NAME=ubuntu",
		"SUDO_ACCESS=true",
	}

	// Container configuration
	containerConfig := &container.Config{
		Image:        cfg.Image,
		Env:          envVars,
		ExposedPorts: exposedPorts,
	}

	// Host configuration with GPU device request
	hostConfig := &container.HostConfig{
		// Resource limits with NVIDIA device request
		Resources: container.Resources{
			Memory:   cfg.MemoryBytes,
			NanoCPUs: cfg.CPUCount * 1e9, // Convert to NanoCPUs
			// NVIDIA device request using UUID (not index)
			DeviceRequests: []container.DeviceRequest{
				{
					Driver:       "nvidia",
					DeviceIDs:    []string{cfg.GPUDeviceID},
					Capabilities: [][]string{{"gpu"}},
				},
			},
		},
		// Publish all exposed ports to random host ports
		PublishAllPorts: true,
	}

	// Create container
	resp, err := s.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, cfg.SessionID)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

// StartContainer starts a container with exponential backoff retry
func (s *DockerService) StartContainer(ctx context.Context, containerID string) error {
	// Configure exponential backoff
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 2 * time.Second
	b.MaxInterval = 10 * time.Second
	b.MaxElapsedTime = 30 * time.Second

	// Wrap backoff with context
	backoffWithContext := backoff.WithContext(b, ctx)

	// Retry operation
	operation := func() error {
		err := s.cli.ContainerStart(ctx, containerID, container.StartOptions{})
		if err != nil {
			return fmt.Errorf("failed to start container: %w", err)
		}
		return nil
	}

	// Execute with retry (max 3 attempts within 30s)
	if err := backoff.Retry(operation, backoffWithContext); err != nil {
		return fmt.Errorf("failed to start container after retries: %w", err)
	}

	return nil
}

// StopContainer stops a container gracefully with timeout
func (s *DockerService) StopContainer(ctx context.Context, containerID string, timeoutSeconds int) error {
	// Stop container with timeout (SIGTERM then SIGKILL)
	timeout := timeoutSeconds
	stopOptions := container.StopOptions{
		Timeout: &timeout,
	}

	if err := s.cli.ContainerStop(ctx, containerID, stopOptions); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	// Wait for container to stop
	waitCh, errCh := s.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case <-waitCh:
		return nil
	case err := <-errCh:
		return fmt.Errorf("error waiting for container to stop: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RemoveContainer removes a container and its volumes
func (s *DockerService) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	removeOptions := container.RemoveOptions{
		RemoveVolumes: true,
		Force:         force,
	}

	if err := s.cli.ContainerRemove(ctx, containerID, removeOptions); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	return nil
}

// InspectContainer returns information about a container
func (s *DockerService) InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	inspect, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	// Parse SSH port from port bindings
	var sshPort int
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Ports != nil {
		if bindings, ok := inspect.NetworkSettings.Ports["22/tcp"]; ok && len(bindings) > 0 {
			if port, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				sshPort = port
			}
		}
	}

	// Get health status
	health := ""
	if inspect.State != nil && inspect.State.Health != nil {
		health = inspect.State.Health.Status
	}

	// Get state
	state := ""
	if inspect.State != nil {
		state = inspect.State.Status
	}

	return &ContainerInfo{
		ContainerID: inspect.ID,
		SSHPort:     sshPort,
		State:       state,
		Health:      health,
	}, nil
}

// Close closes the Docker client connection
func (s *DockerService) Close() error {
	if s.cli != nil {
		return s.cli.Close()
	}
	return nil
}
