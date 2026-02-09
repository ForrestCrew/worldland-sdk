package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/nvml"
	"github.com/worldland/worldland-node/internal/api"
	"github.com/worldland/worldland-node/internal/auth"
	"github.com/worldland/worldland-node/internal/container"
	"github.com/worldland/worldland-node/internal/domain"
	"github.com/worldland/worldland-node/internal/mining"
	"github.com/worldland/worldland-node/internal/port"
	"github.com/worldland/worldland-node/internal/rental"
	"github.com/worldland/worldland-node/internal/services"
)

// Default certificate directory
func defaultCertDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".worldland/certs"
	}
	return filepath.Join(home, ".worldland", "certs")
}

// certsExist checks if all required certificate files exist
func certsExist(certFile, keyFile, caFile string) bool {
	for _, f := range []string{certFile, keyFile, caFile} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// getMachineID returns a unique identifier for the machine
// Uses /etc/machine-id on Linux, falls back to hostname + MAC address
func getMachineID() string {
	// Try /etc/machine-id first (standard on systemd systems)
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		machineID := strings.TrimSpace(string(data))
		if machineID != "" {
			return "MACHINE-" + machineID[:16] // Use first 16 chars
		}
	}

	// Fallback: use hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return "CPU-" + hostname
}

// getGPUUUID returns the UUID of the first GPU, or a fallback ID
func getGPUUUID() string {
	// Try to get real GPU UUID from NVML
	provider := nvml.NewNVMLProvider()
	if err := provider.Init(); err != nil {
		return "GPU-UNKNOWN"
	}
	defer provider.Shutdown()

	specs, err := provider.GetSpecs()
	if err != nil || len(specs) == 0 {
		return "GPU-UNKNOWN"
	}

	return specs[0].UUID
}

// saveCertificates saves the certificate bundle to disk
func saveCertificates(certDir string, bundle *auth.CertificateBundle) (certPath, keyPath, caPath string, err error) {
	// Create cert directory with secure permissions
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return "", "", "", err
	}

	certPath = filepath.Join(certDir, "node.crt")
	keyPath = filepath.Join(certDir, "node.key")
	caPath = filepath.Join(certDir, "ca.crt")

	// Save certificate
	if err := os.WriteFile(certPath, []byte(bundle.Certificate), 0644); err != nil {
		return "", "", "", err
	}

	// Save private key with restricted permissions
	if err := os.WriteFile(keyPath, []byte(bundle.PrivateKey), 0600); err != nil {
		return "", "", "", err
	}

	// Save CA certificate
	if err := os.WriteFile(caPath, []byte(bundle.CACertificate), 0644); err != nil {
		return "", "", "", err
	}

	return certPath, keyPath, caPath, nil
}

func main() {
	log.Println("Worldland Node starting...")

	// Command line flags
	hubAddr := flag.String("hub", "localhost:8443", "Hub mTLS address")
	hubHTTP := flag.String("hub-http", "", "Hub HTTP API URL for authentication (e.g., http://localhost:8080)")
	apiPort := flag.String("api-port", "8444", "Node API mTLS port")
	hostAddr := flag.String("host", "", "Public host address for SSH connections (e.g., provider.example.com)")
	certFile := flag.String("cert", "", "Node certificate file (auto-generated if not specified)")
	keyFile := flag.String("key", "", "Node private key file (auto-generated if not specified)")
	caFile := flag.String("ca", "", "CA certificate file (auto-generated if not specified)")
	certDir := flag.String("cert-dir", defaultCertDir(), "Directory for auto-generated certificates")
	nodeID := flag.String("node-id", "", "Node ID (from registration, defaults to certificate CN)")

	// Mining flags
	enableMining := flag.Bool("enable-mining", true, "Enable automatic GPU mining when idle")
	miningImage := flag.String("mining-image", "mingeyom/worldland-mio:latest", "Docker image for mining")
	miningDataDir := flag.String("mining-data-dir", "/data/worldland", "Host path for mining blockchain data")

	// Wallet authentication flags
	privateKey := flag.String("private-key", "", "Ethereum private key (hex) for wallet authentication")
	privateKeyFile := flag.String("private-key-file", "", "Path to file containing private key")
	siweDomain := flag.String("siwe-domain", "", "SIWE domain for authentication (defaults to hub-http host)")
	gpuType := flag.String("gpu-type", "NVIDIA RTX 4090", "GPU type for registration")
	memoryGB := flag.Int("memory-gb", 24, "GPU memory in GB for registration")
	pricePerSec := flag.String("price-per-sec", "2777777777778", "Price per second in wei (default: 0.01 WLC/hr)")

	flag.Parse()

	// Validate minimum price: 0.01 WLC/hr = 2777777777778 wei/sec
	minPricePerSec := new(big.Int)
	minPricePerSec.SetString("2777777777778", 10)
	userPrice := new(big.Int)
	if _, ok := userPrice.SetString(*pricePerSec, 10); !ok {
		log.Fatalf("Invalid price-per-sec value: %s", *pricePerSec)
	}
	if userPrice.Cmp(minPricePerSec) < 0 {
		log.Fatalf("price-per-sec must be at least 2777777777778 (0.01 WLC/hr), got: %s", *pricePerSec)
	}

	if *hostAddr == "" {
		log.Println("Warning: host address not specified, defaulting to localhost")
		*hostAddr = "localhost"
	}

	// Set default cert paths if not specified
	if *certFile == "" {
		*certFile = filepath.Join(*certDir, "node.crt")
	}
	if *keyFile == "" {
		*keyFile = filepath.Join(*certDir, "node.key")
	}
	if *caFile == "" {
		*caFile = filepath.Join(*certDir, "ca.crt")
	}

	// Early GPU detection to determine node type before registration
	// This check runs before SIWE auth so we register with correct GPU info
	isCPUNode := false
	detectedGPUType := ""
	detectedMemoryGB := 0
	testNVML := nvml.NewNVMLProvider()
	if err := testNVML.Init(); err != nil {
		log.Printf("No GPU detected (%v) - will register as CPU Node", err)
		isCPUNode = true
		// Override default GPU flags for CPU node
		if *gpuType == "NVIDIA RTX 4090" { // default value not changed by user
			*gpuType = "CPU Node"
			*memoryGB = 1 // Minimum non-zero value for API validation
		}
	} else {
		// Auto-detect GPU type and memory from NVML
		specs, err := testNVML.GetSpecs()
		if err == nil && len(specs) > 0 {
			detectedGPUType = specs[0].Name
			detectedMemoryGB = int(specs[0].MemoryTotal / 1024) // Convert MB to GB
			log.Printf("GPU detected: %s (%d GB)", detectedGPUType, detectedMemoryGB)

			// Only override if user didn't specify custom values
			if *gpuType == "NVIDIA RTX 4090" { // default value not changed by user
				*gpuType = detectedGPUType
			}
			if *memoryGB == 24 { // default value not changed by user
				*memoryGB = detectedMemoryGB
			}
		} else {
			log.Printf("GPU detected but failed to get specs: %v", err)
		}
		testNVML.Shutdown()
		log.Println("GPU detected - will register as GPU Node")
	}

	// Wallet authentication and certificate bootstrap
	var walletAddress string
	var siweClient *auth.SIWEClient

	privKeyHex := *privateKey
	if privKeyHex == "" && *privateKeyFile != "" {
		// Read from file
		data, err := os.ReadFile(*privateKeyFile)
		if err != nil {
			log.Fatalf("Failed to read private key file: %v", err)
		}
		privKeyHex = strings.TrimSpace(string(data))
	}

	if privKeyHex != "" {
		// Derive Hub HTTP URL from mTLS address if not specified
		hubHTTPURL := *hubHTTP
		if hubHTTPURL == "" {
			// Convert hub:8443 to http://hub:8080
			hubHost := strings.Split(*hubAddr, ":")[0]
			hubHTTPURL = "http://" + hubHost + ":8080"
		}

		log.Printf("Authenticating with wallet to Hub at %s...", hubHTTPURL)

		var err error
		siweClient, err = auth.NewSIWEClientWithDomain(hubHTTPURL, privKeyHex, *siweDomain)
		if err != nil {
			log.Fatalf("Failed to create SIWE client: %v", err)
		}

		walletAddress = siweClient.GetAddress()
		log.Printf("Wallet address: %s", walletAddress)

		// Login with SIWE
		if err := siweClient.Login(); err != nil {
			log.Fatalf("SIWE authentication failed: %v", err)
		}
		log.Println("SIWE authentication successful")

		// Check if certificates need to be bootstrapped
		if !certsExist(*certFile, *keyFile, *caFile) {
			log.Println("Certificates not found, requesting bootstrap certificate from Hub...")

			bundle, err := siweClient.IssueCertificate()
			if err != nil {
				log.Fatalf("Failed to issue bootstrap certificate: %v", err)
			}

			// Save certificates to disk
			certPath, keyPath, caPath, err := saveCertificates(*certDir, bundle)
			if err != nil {
				log.Fatalf("Failed to save certificates: %v", err)
			}

			*certFile = certPath
			*keyFile = keyPath
			*caFile = caPath

			log.Printf("Bootstrap certificates saved to %s", *certDir)
			log.Printf("  Certificate: %s", certPath)
			log.Printf("  Private Key: %s", keyPath)
			log.Printf("  CA Cert: %s", caPath)
			log.Printf("  Expires: %s", bundle.ExpiresAt)
		} else {
			log.Printf("Using existing certificates from %s", *certDir)
		}

		// Register node via HTTP API
		// Use GPU UUID for GPU nodes, Machine ID for CPU nodes
		var deviceUUID string
		if isCPUNode {
			deviceUUID = getMachineID()
		} else {
			// Get real GPU UUID from NVML
			deviceUUID = getGPUUUID()
		}
		registeredNodeID, err := siweClient.RegisterNode(deviceUUID, *gpuType, *memoryGB, *pricePerSec)
		if err != nil {
			// Node might already be registered, continue
			log.Printf("Node registration: %v (may already exist)", err)
		} else {
			log.Printf("Node registered: %s", registeredNodeID)
			if *nodeID == "" {
				*nodeID = registeredNodeID
			}
		}
	} else {
		log.Println("No private key provided - using existing certificates (mock wallet mode)")
		// In mock mode, certificates must already exist
		if !certsExist(*certFile, *keyFile, *caFile) {
			log.Fatalf("Certificates not found at %s. Either provide -private-key for auto-bootstrap or manually place certificates.", *certDir)
		}
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

	// Initialize GPU provider (use early detection result)
	var gpuProvider domain.GPUProvider
	if isCPUNode {
		// CPU node uses a minimal mock provider for the daemon interface
		gpuProvider = nvml.NewMockGPUProvider(
			[]domain.GPUMetrics{
				{
					UUID:        "cpu-node",
					Name:        "CPU Only",
					MemoryTotal: 0,
					MemoryUsed:  0,
					GPUUtil:     0,
					MemoryUtil:  0,
					Temperature: 0,
				},
			},
			[]domain.GPUSpec{
				{
					UUID:        "cpu-node",
					Name:        "CPU Only",
					MemoryTotal: 0,
					DriverVer:   "N/A",
				},
			},
		)
	} else {
		realNVML := nvml.NewNVMLProvider()
		if err := realNVML.Init(); err != nil {
			log.Fatalf("GPU was detected earlier but failed to initialize: %v", err)
		}
		gpuProvider = realNVML
	}

	// Initialize rental infrastructure (before daemon so it can receive commands)
	dockerService, err := container.NewDockerService()
	if err != nil {
		log.Fatalf("Failed to initialize Docker service: %v", err)
	}

	// Create port manager (30000-32000 range, 30-minute grace period)
	portManager := port.NewPortManager(30000, 32000, 30*time.Minute)

	// Create rental executor
	rentalExecutor := rental.NewRentalExecutor(dockerService, portManager, 30*time.Minute)

	// Initialize mining daemon if enabled
	var miningDaemon *mining.MiningDaemon
	if *enableMining && !isCPUNode {
		// Collect GPU UUIDs for mining config
		var gpuUUIDs []string
		if testNVML := nvml.NewNVMLProvider(); testNVML != nil {
			if err := testNVML.Init(); err == nil {
				specs, err := testNVML.GetSpecs()
				if err == nil {
					for _, spec := range specs {
						gpuUUIDs = append(gpuUUIDs, spec.UUID)
					}
				}
				testNVML.Shutdown()
			}
		}

		miningCfg := mining.MiningConfig{
			Enabled:       true,
			WalletAddress: walletAddress,
			Image:         *miningImage,
			GPUDeviceIDs:  gpuUUIDs,
			DataDir:       *miningDataDir,
			ChainID:       10396,
			P2PPort:       30303,
			HTTPRPCPort:   8545,
		}

		miningDaemon = mining.NewMiningDaemon(dockerService, miningCfg)
		log.Printf("Mining daemon initialized: image=%s gpus=%d", *miningImage, len(gpuUUIDs))
	}

	// Create daemon for GPU monitoring and Hub connection
	// Wire rental executor so daemon can handle start_rental/stop_rental mTLS commands
	daemon := services.NewNodeDaemon(gpuProvider, *nodeID)
	daemon.WithRentalExecutor(rentalExecutor, *hostAddr)

	// Wire mining daemon if enabled
	if miningDaemon != nil {
		daemon.WithMiningDaemon(miningDaemon)
	}

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

	log.Println("Node daemon running (Docker rental executor enabled)")

	// Start mining daemon in background if configured
	if miningDaemon != nil {
		go func() {
			miningCtx := context.Background()
			if err := miningDaemon.Start(miningCtx); err != nil {
				log.Printf("Mining daemon start error: %v", err)
			}
			// Start health monitoring loop
			miningDaemon.MonitorLoop(miningCtx)
		}()
		log.Println("Mining daemon started in background")
	}

	// Create API handler (HTTP fallback for direct Node API calls)
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

	// Stop mining daemon first (releases GPUs)
	if miningDaemon != nil {
		miningDaemon.Close()
		shutdownMiningCtx, cancelMining := context.WithTimeout(context.Background(), 10*time.Second)
		if err := miningDaemon.Stop(shutdownMiningCtx); err != nil {
			log.Printf("Mining daemon shutdown error: %v", err)
		}
		cancelMining()
	}

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
