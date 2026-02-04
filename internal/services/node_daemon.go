package services

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/mtls"
	"github.com/worldland/worldland-node/internal/domain"
)

// NodeDaemon manages the node lifecycle
type NodeDaemon struct {
	gpuProvider     domain.GPUProvider
	mtlsClient      *mtls.Client
	nodeID          string
	metricsInterval time.Duration
	stopCh          chan struct{}
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
	case "start_job":
		// TODO: Implement in Phase 4
		log.Printf("Starting job (Phase 4 implementation)")
		return mtls.CommandAck{CommandID: cmd.ID, Status: "ok"}
	case "stop_job":
		// TODO: Implement in Phase 4
		log.Printf("Stopping job (Phase 4 implementation)")
		return mtls.CommandAck{CommandID: cmd.ID, Status: "ok"}
	case "join_k8s":
		// Handle K8s cluster join command
		return d.handleK8sJoin(cmd)
	default:
		log.Printf("Unknown command type: %s", cmd.Type)
		return mtls.CommandAck{CommandID: cmd.ID, Status: "error", Error: "unknown command"}
	}
}

// handleK8sJoin executes kubeadm join to add this node to the K8s cluster
func (d *NodeDaemon) handleK8sJoin(cmd mtls.Command) mtls.CommandAck {
	log.Printf("Processing K8s join command")

	// Extract join command from payload
	joinCommand, ok := cmd.Payload["join_command"].(string)
	if !ok || joinCommand == "" {
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "error",
			Error:     "missing join_command in payload",
		}
	}

	// Check if already joined (look for kubelet running)
	if d.isAlreadyK8sNode() {
		log.Printf("Node is already part of K8s cluster")
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "ok",
			Error:     "already_joined",
		}
	}

	// Execute the join command
	log.Printf("Executing kubeadm join...")
	if err := d.executeKubeadmJoin(joinCommand); err != nil {
		log.Printf("kubeadm join failed: %v", err)
		return mtls.CommandAck{
			CommandID: cmd.ID,
			Status:    "error",
			Error:     fmt.Sprintf("join failed: %v", err),
		}
	}

	log.Printf("Successfully joined K8s cluster")
	return mtls.CommandAck{
		CommandID: cmd.ID,
		Status:    "ok",
	}
}

// isAlreadyK8sNode checks if this node is already part of a K8s cluster
func (d *NodeDaemon) isAlreadyK8sNode() bool {
	// Check if kubelet is running and configured
	cmd := exec.Command("systemctl", "is-active", "kubelet")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "active"
}

// executeKubeadmJoin runs the kubeadm join command
func (d *NodeDaemon) executeKubeadmJoin(joinCommand string) error {
	// Parse the join command into arguments
	// The join command looks like: sudo kubeadm join IP:PORT --token TOKEN --discovery-token-ca-cert-hash HASH
	parts := strings.Fields(joinCommand)

	// Find the actual kubeadm command (skip sudo if present)
	cmdStart := 0
	for i, p := range parts {
		if p == "kubeadm" {
			cmdStart = i
			break
		}
	}

	if cmdStart >= len(parts) {
		return fmt.Errorf("invalid join command: kubeadm not found")
	}

	// Execute with sudo
	args := append([]string{"kubeadm"}, parts[cmdStart+1:]...)
	cmd := exec.Command("sudo", args...)

	log.Printf("Running: sudo %s", strings.Join(args, " "))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}

	log.Printf("kubeadm join output: %s", string(output))
	return nil
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
			// TODO: Send metrics to Hub in Phase 3
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
