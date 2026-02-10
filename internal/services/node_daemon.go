package services

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/mtls"
	"github.com/worldland/worldland-node/internal/domain"
)

// ClusterCapacity mirrors proxy's ProviderCapacity for heartbeat reporting.
// Hub uses this to track provider resources.
type ClusterCapacity struct {
	// GPU tracking by type (proxy pattern: map[string]int)
	TotalGPUs     map[string]int `json:"total_gpus,omitempty"`     // {"RTX 4090": 4}
	AvailableGPUs map[string]int `json:"available_gpus,omitempty"` // {"RTX 4090": 2}
	InUseGPUs     map[string]int `json:"in_use_gpus,omitempty"`    // {"RTX 4090": 2}

	// CPU/Memory tracking
	TotalCPUCores     int `json:"total_cpu_cores"`
	AvailableCPUCores int `json:"available_cpu_cores"`
	TotalMemoryMB     int `json:"total_memory_mb"`
	AvailableMemoryMB int `json:"available_memory_mb"`

	// Node info
	NodeCount int `json:"node_count"`
}

// NodeDaemon manages the node lifecycle and Hub mTLS connection.
// V4: Master mode only — no Docker execution, just heartbeat + capacity reporting.
// Mirrors proxy's provider-agent heartbeat pattern.
type NodeDaemon struct {
	gpuProvider     domain.GPUProvider
	mtlsClient      *mtls.Client
	nodeID           string
	clusterCapacity  *ClusterCapacity // Set by master mode after cluster discovery
	metricsInterval  time.Duration
	stopCh           chan struct{}
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

// SetClusterCapacity sets the cluster capacity for heartbeat reporting
func (d *NodeDaemon) SetClusterCapacity(cap *ClusterCapacity) {
	d.clusterCapacity = cap
}

// ConnectToHub establishes mTLS connection to Hub
func (d *NodeDaemon) ConnectToHub(hubAddr string, cert tls.Certificate, rootCAs *x509.CertPool) error {
	d.mtlsClient = mtls.NewClient(hubAddr, cert, rootCAs)

	// V4: No command handling — Hub manages K8s pods directly via kubeconfig
	d.mtlsClient.OnCommand = func(cmd mtls.Command) mtls.CommandAck {
		log.Printf("Received command from Hub: %s (type: %s) — ignoring in master mode", cmd.ID, cmd.Type)
		return mtls.CommandAck{CommandID: cmd.ID, Status: "ok"}
	}

	if err := d.mtlsClient.Connect(); err != nil {
		return err
	}

	log.Printf("Connected to Hub at %s", hubAddr)
	return nil
}

// Start begins the daemon's main loops
func (d *NodeDaemon) Start() error {
	// Initialize GPU provider for local hardware info
	if err := d.gpuProvider.Init(); err != nil {
		log.Printf("Warning: GPU provider init failed: %v (continuing without GPU metrics)", err)
	} else {
		defer d.gpuProvider.Shutdown()
	}

	// Start listening for commands (even if ignored, keeps connection alive)
	go d.mtlsClient.Listen()

	// Start heartbeat loop
	go d.reportMetrics()

	// Wait for stop signal
	<-d.stopCh
	return nil
}

// reportMetrics periodically sends heartbeats with GPU metrics + cluster capacity to Hub.
// Mirrors proxy's heartbeatLoop pattern.
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

			heartbeat := d.buildHeartbeat(metrics)
			if d.mtlsClient != nil {
				if err := d.mtlsClient.Send(heartbeat); err != nil {
					log.Printf("Failed to send heartbeat: %v", err)
				}
			}
		}
	}
}

// buildHeartbeat creates a JSON heartbeat message with metrics + cluster capacity.
// Hub parses this to update provider node capacity in DB.
// Mirrors proxy's HeartbeatMessage structure.
func (d *NodeDaemon) buildHeartbeat(gpuMetrics []domain.GPUMetrics) []byte {
	payload := map[string]any{
		"gpu_metrics": gpuMetrics,
		"mode":        "master",
	}

	// Include cluster capacity if available (proxy's CapacityUpdate pattern)
	if d.clusterCapacity != nil {
		payload["cluster_capacity"] = d.clusterCapacity
	}

	msg := map[string]any{
		"type":    "heartbeat",
		"payload": payload,
	}

	data, _ := json.Marshal(msg)
	return data
}

// Stop gracefully stops the daemon
func (d *NodeDaemon) Stop() {
	close(d.stopCh)
	if d.mtlsClient != nil {
		d.mtlsClient.Close()
	}
}
