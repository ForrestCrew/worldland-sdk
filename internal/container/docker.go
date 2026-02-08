package container

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContainerConfig holds configuration for creating a GPU container
type ContainerConfig struct {
	SessionID   string // Used as container name
	Image       string // e.g., "nvidia/cuda:12.1.1-runtime-ubuntu22.04"
	GPUDeviceID string // NVIDIA UUID (not index)
	SSHPassword string // SSH password for the user
	SSHPort     int    // Host port to bind for SSH (container:22 -> host:SSHPort)
	MemoryBytes int64  // Memory limit in bytes
	CPUCount    int64  // CPU count (in NanoCPUs / 1e9)
	UseImageEntrypoint bool // If true, use the image's default entrypoint (no SSH setup)
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
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error)
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

// ensureImage pulls a Docker image if it's not available locally.
func (s *DockerService) ensureImage(ctx context.Context, imageName string) error {
	// Try to inspect the image first — if it exists locally, no pull needed
	_, err := s.cli.ImageInspect(ctx, imageName)
	if err == nil {
		return nil
	}

	slog.Info("image not found locally, pulling from registry", "image", imageName)

	reader, err := s.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Consume the reader to complete the pull (progress output is discarded)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("error during image pull %s: %w", imageName, err)
	}

	slog.Info("image pulled successfully", "image", imageName)
	return nil
}

// sshSetupScript is the entrypoint script that installs and starts SSH server
// inside any base image (CUDA, PyTorch, TensorFlow, etc.)
const sshSetupScript = `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq openssh-server sudo > /dev/null 2>&1

# Create user with password
useradd -m -s /bin/bash "$USER_NAME" 2>/dev/null || true
echo "$USER_NAME:$SSH_PASSWORD" | chpasswd
echo "$USER_NAME ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# Configure sshd
mkdir -p /run/sshd
sed -i 's/#PasswordAuthentication yes/PasswordAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/#PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config

echo "SSH server ready on port 22"
exec /usr/sbin/sshd -D
`

// CreateContainer creates a GPU container with NVIDIA runtime and SSH access
func (s *DockerService) CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	// Auto-pull image if not available locally
	if err := s.ensureImage(ctx, cfg.Image); err != nil {
		return "", fmt.Errorf("failed to ensure image: %w", err)
	}

	// GPU device selection via NVIDIA_VISIBLE_DEVICES
	// Use "all" by default; for multi-GPU hosts, use device index (e.g., "0", "1")
	// Note: GPU UUIDs don't work with nvidia runtime auto/CDI mode
	gpuDevice := "all"
	if cfg.GPUDeviceID != "" && cfg.GPUDeviceID != "all" && !isGPUUUID(cfg.GPUDeviceID) {
		gpuDevice = cfg.GPUDeviceID // device index like "0", "1"
	}

	var containerConfig *container.Config
	var portBindings nat.PortMap

	if cfg.UseImageEntrypoint {
		// Mining mode: use image's default entrypoint, no SSH
		containerConfig = &container.Config{
			Image: cfg.Image,
			Env: []string{
				fmt.Sprintf("NVIDIA_VISIBLE_DEVICES=%s", gpuDevice),
				"NVIDIA_DRIVER_CAPABILITIES=all",
			},
		}
	} else {
		// Rental mode: inject SSH setup as entrypoint
		containerConfig = &container.Config{
			Image: cfg.Image,
			Env: []string{
				fmt.Sprintf("SSH_PASSWORD=%s", cfg.SSHPassword),
				"USER_NAME=ubuntu",
				fmt.Sprintf("NVIDIA_VISIBLE_DEVICES=%s", gpuDevice),
				"NVIDIA_DRIVER_CAPABILITIES=all",
			},
			ExposedPorts: nat.PortSet{"22/tcp": struct{}{}},
			Entrypoint:   []string{"/bin/bash", "-c"},
			Cmd:          []string{sshSetupScript},
		}
		portBindings = nat.PortMap{
			"22/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: strconv.Itoa(cfg.SSHPort)},
			},
		}
	}

	// Host configuration with nvidia runtime
	hostConfig := &container.HostConfig{
		Runtime: "nvidia",
		Resources: container.Resources{
			Memory:   cfg.MemoryBytes,
			NanoCPUs: cfg.CPUCount * 1e9, // Convert to NanoCPUs
		},
		PortBindings: portBindings,
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

// isGPUUUID returns true if the string looks like a GPU UUID (e.g., "GPU-751b4c38-...")
func isGPUUUID(s string) bool {
	return strings.HasPrefix(s, "GPU-") || strings.HasPrefix(s, "MIG-")
}

// Close closes the Docker client connection
func (s *DockerService) Close() error {
	if s.cli != nil {
		return s.cli.Close()
	}
	return nil
}
