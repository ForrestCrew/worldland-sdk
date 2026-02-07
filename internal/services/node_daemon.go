package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/mtls"
	"github.com/worldland/worldland-node/internal/domain"
	"github.com/worldland/worldland-node/internal/mining"
	"github.com/worldland/worldland-node/internal/rental"
)

// NodeDaemon manages the node lifecycle, handles Hub commands via mTLS,
// and executes Docker-based GPU rentals.
type NodeDaemon struct {
	gpuProvider     domain.GPUProvider
	mtlsClient      *mtls.Client
	nodeID          string
	hostAddr        string // Public host address for SSH connections
	metricsInterval time.Duration
	stopCh          chan struct{}

	// Docker rental executor (set via WithRentalExecutor)
	rentalExecutor *rental.RentalExecutor

	// Mining daemon (set via WithMiningDaemon)
	miningDaemon *mining.MiningDaemon
}

// NewNodeDaemon creates a new node daemon
func NewNodeDaemon(gpuProvider domain.GPUProvider, nodeID string) *NodeDaemon {
	return &NodeDaemon{
		gpuProvider:     gpuProvider,
		nodeID:          nodeID,
		metricsInterval: 30 * time.Second,
		stopCh:          make(chan struct{}),
	}
}

// WithRentalExecutor sets the rental executor for Docker-based rentals
func (d *NodeDaemon) WithRentalExecutor(executor *rental.RentalExecutor, hostAddr string) *NodeDaemon {
	d.rentalExecutor = executor
	d.hostAddr = hostAddr
	return d
}

// WithMiningDaemon sets the mining daemon for auto-mining when GPU is idle
func (d *NodeDaemon) WithMiningDaemon(md *mining.MiningDaemon) *NodeDaemon {
	d.miningDaemon = md
	return d
}

// ConnectToHub establishes mTLS connection to Hub
func (d *NodeDaemon) ConnectToHub(hubAddr string, cert tls.Certificate, rootCAs *x509.CertPool) error {
	d.mtlsClient = mtls.NewClient(hubAddr, cert, rootCAs)

	// Set up command handler
	d.mtlsClient.OnCommand = d.handleCommand

	if err := d.mtlsClient.Connect(); err != nil {
		return err
	}

	log.Printf("Connected to Hub at %s", hubAddr)
	return nil
}

// Start begins the daemon's main loops
func (d *NodeDaemon) Start() error {
	// Initialize GPU provider
	if err := d.gpuProvider.Init(); err != nil {
		log.Printf("Warning: GPU provider init failed: %v (continuing without GPU metrics)", err)
	} else {
		defer d.gpuProvider.Shutdown()
	}

	// Start listening for commands
	go d.mtlsClient.Listen()

	// Start metrics reporting
	go d.reportMetrics()

	// Wait for stop signal
	<-d.stopCh
	return nil
}

// handleCommand processes commands received from Hub
func (d *NodeDaemon) handleCommand(cmd mtls.Command) mtls.CommandAck {
	log.Printf("Received command: %s (type: %s)", cmd.ID, cmd.Type)

	switch cmd.Type {
	case "start_rental":
		return d.handleStartRental(cmd)
	case "stop_rental":
		return d.handleStopRental(cmd)
	case "start_job":
		// Legacy alias for start_rental
		return d.handleStartRental(cmd)
	case "stop_job":
		// Legacy alias for stop_rental
		return d.handleStopRental(cmd)
	default:
		log.Printf("Unknown command type: %s", cmd.Type)
		return mtls.CommandAck{CommandID: cmd.ID, Status: "error", Error: "unknown command"}
	}
}

// handleStartRental creates and starts a Docker container for a GPU rental
func (d *NodeDaemon) handleStartRental(cmd mtls.Command) mtls.CommandAck {
	if d.rentalExecutor == nil {
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "error",
			Error:     "rental executor not configured",
		}
	}

	// Extract payload fields
	sessionID, _ := cmd.Payload["session_id"].(string)
	if sessionID == "" {
		return mtls.CommandAck{CommandID: cmd.ID, Status: "error", Error: "missing session_id"}
	}

	image, _ := cmd.Payload["image"].(string)
	if image == "" {
		image = "nvidia/cuda:12.1.1-runtime-ubuntu22.04"
	}

	gpuDeviceID, _ := cmd.Payload["gpu_device_id"].(string)
	sshPassword, _ := cmd.Payload["ssh_password"].(string)

	cpuCount := int64(4)
	if v, ok := cmd.Payload["cpu_count"].(float64); ok {
		cpuCount = int64(v)
	}

	memoryMB := int64(16384)
	if v, ok := cmd.Payload["memory_mb"].(float64); ok {
		memoryMB = int64(v)
	}

	log.Printf("Starting rental: session=%s image=%s gpu=%s", sessionID, image, gpuDeviceID)

	// Pause mining to release GPU for rental
	if d.miningDaemon != nil && gpuDeviceID != "" {
		pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := d.miningDaemon.PauseForRental(pauseCtx, []string{gpuDeviceID}); err != nil {
			log.Printf("Warning: failed to pause mining for rental: %v", err)
		}
		pauseCancel()
	}

	// Use rental executor to create and start Docker container
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	connInfo, err := d.rentalExecutor.StartRental(ctx, rental.StartRentalRequest{
		SessionID:    sessionID,
		Image:        image,
		GPUDeviceID:  gpuDeviceID,
		SSHPublicKey: sshPassword, // Reused as password/key
		MemoryBytes:  memoryMB * 1024 * 1024,
		CPUCount:     cpuCount,
		Host:         d.hostAddr,
	})
	if err != nil {
		log.Printf("Failed to start rental %s: %v", sessionID, err)
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "error",
			Error:     fmt.Sprintf("failed to start rental: %v", err),
		}
	}

	log.Printf("Rental started: session=%s ssh=%s:%d", sessionID, connInfo.Host, connInfo.Port)

	// Resolve public IP if host is hostname
	sshHost := d.hostAddr
	if sshHost == "" || sshHost == "localhost" {
		// Try to get the machine's public IP
		sshHost = getOutboundIP()
	}

	return mtls.CommandAck{
		CommandID: cmd.ID,
		Status:    "ok",
		Payload: map[string]interface{}{
			"session_id":   sessionID,
			"ssh_host":     sshHost,
			"ssh_port":     float64(connInfo.Port),
			"ssh_user":     connInfo.User,
			"container_id": connInfo.ContainerID,
		},
	}
}

// handleStopRental stops and cleans up a Docker container for a GPU rental
func (d *NodeDaemon) handleStopRental(cmd mtls.Command) mtls.CommandAck {
	if d.rentalExecutor == nil {
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "error",
			Error:     "rental executor not configured",
		}
	}

	sessionID, _ := cmd.Payload["session_id"].(string)
	if sessionID == "" {
		return mtls.CommandAck{CommandID: cmd.ID, Status: "error", Error: "missing session_id"}
	}

	log.Printf("Stopping rental: session=%s", sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := d.rentalExecutor.StopRental(ctx, sessionID); err != nil {
		log.Printf("Failed to stop rental %s: %v", sessionID, err)
		// Return ok even on error - Hub should mark session as stopped regardless
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "ok",
			Error:     fmt.Sprintf("stop warning: %v", err),
		}
	}

	log.Printf("Rental stopped: session=%s", sessionID)

	// Resume mining after rental ends - GPU is now available
	if d.miningDaemon != nil {
		resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := d.miningDaemon.ResumeAfterRental(resumeCtx, []string{}); err != nil {
			log.Printf("Warning: failed to resume mining after rental: %v", err)
		}
		resumeCancel()
	}

	return mtls.CommandAck{
		CommandID: cmd.ID,
		Status:    "ok",
		Payload: map[string]interface{}{
			"session_id": sessionID,
		},
	}
}

// reportMetrics periodically collects and reports GPU metrics
func (d *NodeDaemon) reportMetrics() {
	ticker := time.NewTicker(d.metricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			metrics, err := d.gpuProvider.GetMetrics()
			if err != nil {
				log.Printf("Failed to collect GPU metrics: %v", err)
				continue
			}
			log.Printf("Collected metrics for %d GPU(s)", len(metrics))
			// TODO: Send metrics to Hub via mTLS
		}
	}
}

// Stop gracefully stops the daemon
func (d *NodeDaemon) Stop() {
	close(d.stopCh)
	if d.mtlsClient != nil {
		d.mtlsClient.Close()
	}
}

// getOutboundIP gets the preferred outbound IP of this machine
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
