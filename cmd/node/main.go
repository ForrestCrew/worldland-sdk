package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/nvml"
	"github.com/worldland/worldland-node/internal/api"
	"github.com/worldland/worldland-node/internal/container"
	"github.com/worldland/worldland-node/internal/domain"
	"github.com/worldland/worldland-node/internal/port"
	"github.com/worldland/worldland-node/internal/rental"
	"github.com/worldland/worldland-node/internal/services"
)

func main() {
	log.Println("Worldland Node starting...")

	// Command line flags
	hubAddr := flag.String("hub", "localhost:8443", "Hub mTLS address")
	apiPort := flag.String("api-port", "8444", "Node API mTLS port")
	hostAddr := flag.String("host", "", "Public host address for SSH connections (e.g., provider.example.com)")
	certFile := flag.String("cert", "node.crt", "Node certificate file")
	keyFile := flag.String("key", "node.key", "Node private key file")
	caFile := flag.String("ca", "ca.crt", "CA certificate file")
	nodeID := flag.String("node-id", "", "Node ID (from registration, defaults to certificate CN)")
	flag.Parse()

	if *hostAddr == "" {
		log.Println("Warning: host address not specified, defaulting to localhost")
		*hostAddr = "localhost"
	}

	// Load certificates
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("Failed to load certificate: %v", err)
	}

	caCert, err := os.ReadFile(*caFile)
	if err != nil {
		log.Fatalf("Failed to read CA certificate: %v", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		log.Fatal("Failed to parse CA certificate")
	}

	// If node-id not provided, extract from certificate CN
	if *nodeID == "" {
		parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			log.Fatalf("Failed to parse certificate for CN extraction: %v", err)
		}
		if parsedCert.Subject.CommonName == "" {
			log.Fatal("node-id is required (certificate has no CN)")
		}
		*nodeID = parsedCert.Subject.CommonName
		log.Printf("Using certificate CN as node-id: %s", *nodeID)
	}

	// Initialize GPU provider (try real NVML first, fall back to mock)
	var gpuProvider domain.GPUProvider
	realNVML := nvml.NewNVMLProvider()
	if err := realNVML.Init(); err != nil {
		log.Printf("Warning: NVML not available (%v), using mock provider", err)
		// Use mock provider for development without NVIDIA hardware
		gpuProvider = nvml.NewMockGPUProvider(
			[]domain.GPUMetrics{
				{
					UUID:        "mock-gpu-1",
					Name:        "Mock GPU",
					MemoryTotal: 24000,
					MemoryUsed:  8000,
					GPUUtil:     50,
					MemoryUtil:  33,
					Temperature: 60,
				},
			},
			[]domain.GPUSpec{
				{
					UUID:        "mock-gpu-1",
					Name:        "Mock GPU",
					MemoryTotal: 24000,
					DriverVer:   "535.129.03",
				},
			},
		)
	} else {
		realNVML.Shutdown()
		gpuProvider = realNVML
	}

	// Create daemon for GPU monitoring and Hub connection
	daemon := services.NewNodeDaemon(gpuProvider, *nodeID)

	// Connect to Hub for heartbeat and metrics reporting
	if err := daemon.ConnectToHub(*hubAddr, cert, caCertPool); err != nil {
		log.Fatalf("Failed to connect to Hub: %v", err)
	}

	// Start daemon in background
	go func() {
		if err := daemon.Start(); err != nil {
			log.Fatalf("Daemon error: %v", err)
		}
	}()

	log.Println("Node daemon running")

	// Initialize rental infrastructure
	dockerService, err := container.NewDockerService()
	if err != nil {
		log.Fatalf("Failed to initialize Docker service: %v", err)
	}

	// Create port manager (30000-32000 range, 30-minute grace period)
	portManager := port.NewPortManager(30000, 32000, 30*time.Minute)

	// Create rental executor
	rentalExecutor := rental.NewRentalExecutor(dockerService, portManager, 30*time.Minute)

	// Create API handler
	rentalHandler := api.NewRentalHandler(rentalExecutor, *hostAddr)

	// Create HTTP mux with routes
	mux := http.NewServeMux()
	mux.HandleFunc("/rentals/start", rentalHandler.HandleStartRental)
	mux.HandleFunc("/rentals/stop", rentalHandler.HandleStopRental)
	mux.HandleFunc("/rentals/status", rentalHandler.HandleGetStatus)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Configure mTLS server
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caCertPool,
		MinVersion:   tls.VersionTLS13, // TLS 1.3 only per Phase 2 decision
	}

	server := &http.Server{
		Addr:      ":" + *apiPort,
		Handler:   mux,
		TLSConfig: tlsConfig,
	}

	// Start API server in background
	go func() {
		log.Printf("Starting rental API server on port %s (mTLS)", *apiPort)
		if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	log.Printf("Node ready - API on port %s, metrics daemon connected to %s", *apiPort, *hubAddr)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")

	// Stop daemon
	daemon.Stop()

	// Shutdown API server with 5-second timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("API server shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
}
