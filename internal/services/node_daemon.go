package services

import (
	"crypto/tls"
	"crypto/x509"
	"log"
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
	default:
		log.Printf("Unknown command type: %s", cmd.Type)
		return mtls.CommandAck{CommandID: cmd.ID, Status: "error", Error: "unknown command"}
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
