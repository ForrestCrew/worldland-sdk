package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/nvml"
	"github.com/worldland/worldland-node/internal/auth"
	"github.com/worldland/worldland-node/internal/cli"
	"github.com/worldland/worldland-node/internal/domain"
	"github.com/worldland/worldland-node/internal/services"
	"github.com/worldland/worldland-node/internal/setup"
)

const version = "4.1.0"

func main() {
	// Backward compatibility: support -mode=master|worker
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "-") {
		runLegacyMode()
		return
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "master":
		runMasterCmd()
	case "worker":
		runWorkerCmd()
	case "status":
		runStatusCmd()
	case "nodes":
		runNodesCmd()
	case "mining":
		runMiningCmd()
	case "price":
		runPriceCmd()
	case "version":
		fmt.Printf("Worldland SDK %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`Worldland SDK %s

Usage: node <command> [flags]

Commands:
  master     Run in master mode (mTLS + heartbeat)
  worker     Run in worker mode (auto setup + K8s join)
  status     Show provider, nodes, and mining status
  nodes      List registered nodes
  mining     Manage mining (start/stop/status)
  price      Update node pricing
  version    Show version
  help       Show this help

Legacy mode:
  node -mode=master|worker [flags]

`, version)
}

// runLegacyMode handles the old -mode=master|worker flag style
func runLegacyMode() {
	mode := flag.String("mode", "master", "Operating mode: master or worker")
	hubAddr := flag.String("hub", "localhost:8443", "Hub mTLS address")
	hubHTTP := flag.String("hub-http", "", "Hub HTTP API URL")
	privateKey := flag.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := flag.String("private-key-file", "", "Path to file containing private key")
	siweDomain := flag.String("siwe-domain", "", "SIWE domain for authentication")
	certFile := flag.String("cert", "", "Node certificate file")
	keyFile := flag.String("key", "", "Node private key file")
	caFile := flag.String("ca", "", "CA certificate file")
	certDir := flag.String("cert-dir", defaultCertDir(), "Directory for certificates")
	nodeID := flag.String("node-id", "", "Node ID")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig file")
	masterWallet := flag.String("master-wallet", "", "Master node wallet address (worker mode)")
	flag.Parse()

	switch *mode {
	case "master":
		runMaster(*hubAddr, *hubHTTP, *privateKey, *privateKeyFile, *siweDomain,
			certFile, keyFile, caFile, certDir, nodeID, *kubeconfig)
	case "worker":
		runWorker(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain, *masterWallet)
	default:
		log.Fatalf("Unknown mode: %s (use 'master' or 'worker')", *mode)
	}
}

// =====================================================================
// master command
// =====================================================================

func runMasterCmd() {
	fs := flag.NewFlagSet("master", flag.ExitOnError)
	hubAddr := fs.String("hub", "localhost:8443", "Hub mTLS address")
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	certFile := fs.String("cert", "", "Node certificate file")
	keyFile := fs.String("key", "", "Node private key file")
	caFile := fs.String("ca", "", "CA certificate file")
	certDir := fs.String("cert-dir", defaultCertDir(), "Directory for certificates")
	nodeID := fs.String("node-id", "", "Node ID")
	kubeconfig := fs.String("kubeconfig", "", "Path to kubeconfig file")
	fs.Parse(os.Args[2:])

	runMaster(*hubAddr, *hubHTTP, *privateKey, *privateKeyFile, *siweDomain,
		certFile, keyFile, caFile, certDir, nodeID, *kubeconfig)
}

// =====================================================================
// worker command (automated join)
// =====================================================================

func runWorkerCmd() {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL (required)")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	masterWallet := fs.String("master-wallet", "", "Master node wallet address (required)")
	hostIP := fs.String("host", "", "External IP for SSH access (auto-detect if empty)")
	autoSetup := fs.Bool("auto-setup", true, "Automatically install and configure dependencies")
	fs.Parse(os.Args[2:])

	if *autoSetup {
		runWorkerAutoSetup(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain, *masterWallet, *hostIP)
	} else {
		runWorker(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain, *masterWallet)
	}
}

// runWorkerAutoSetup performs the full automated worker join flow
func runWorkerAutoSetup(hubHTTP, privKey, privKeyFile, siweDomain, masterWallet, hostIP string) {
	if masterWallet == "" {
		log.Fatal("Worker mode requires -master-wallet")
	}
	if hubHTTP == "" {
		log.Fatal("Worker mode requires -hub-http")
	}

	totalSteps := 6

	// Step 1: Preflight check
	cli.PrintStep(1, totalSteps, "Checking prerequisites...")
	result, err := setup.RunPreflight()
	if err != nil {
		log.Fatalf("Preflight check failed: %v", err)
	}
	result.PrintStatus()

	missing := result.MissingComponents()
	// nvidia-smi is not auto-installable
	var autoInstallable []string
	for _, m := range missing {
		if m != "nvidia-smi" {
			autoInstallable = append(autoInstallable, m)
		}
	}
	autoInstallable = setup.DeduplicateK8s(autoInstallable)

	// Step 2: Install missing components
	cli.PrintStep(2, totalSteps, fmt.Sprintf("Installing missing: %s", setup.FormatMissing(autoInstallable)))
	if len(autoInstallable) > 0 {
		if err := setup.CheckRoot(); err != nil {
			log.Fatalf("Cannot install packages: %v", err)
		}
		if err := setup.InstallMissing(autoInstallable, result.OSId); err != nil {
			log.Fatalf("Installation failed: %v", err)
		}
		fmt.Println("  -> Done")
	} else {
		fmt.Println("  -> All components already installed")
	}

	// Step 3: Configure container runtime
	cli.PrintStep(3, totalSteps, "Configuring container runtime...")
	if err := setup.CheckRoot(); err != nil {
		log.Fatalf("Cannot configure runtime: %v", err)
	}
	if err := setup.ConfigureRuntime(); err != nil {
		log.Fatalf("Runtime configuration failed: %v", err)
	}

	// Step 4: Request join token
	cli.PrintStep(4, totalSteps, "Requesting join token from Hub...")
	privKeyHex := resolvePrivateKey(privKey, privKeyFile)
	authToken := authenticateForWorker(hubHTTP, privKeyHex, siweDomain)

	hubClient := cli.NewHubClient(hubHTTP, authToken)
	joinResp, err := hubClient.GetJoinToken(masterWallet)
	if err != nil {
		log.Fatalf("Failed to get join token: %v", err)
	}
	fmt.Printf("  -> Token received (expires in %s)\n", joinResp.ExpiresIn)

	// Step 5: Join K8s cluster
	cli.PrintStep(5, totalSteps, "Joining K8s cluster...")
	if err := setup.ExecuteJoin(joinResp.JoinCommand); err != nil {
		log.Fatalf("Join failed: %v", err)
	}

	// Step 6: Annotate K8s node with metadata (external IP + GPU info via NVML)
	cli.PrintStep(6, totalSteps, "Setting node annotations...")
	hostname, _ := os.Hostname()
	kubeconfig := "/etc/kubernetes/kubelet.conf"

	// 6a: External IP
	externalIP := hostIP
	if externalIP == "" {
		externalIP = detectExternalIP()
	}
	if externalIP != "" {
		cmd := exec.Command("kubectl", "--kubeconfig="+kubeconfig,
			"annotate", "node", hostname,
			"worldland.io/external-ip="+externalIP, "--overwrite")
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("Warning: failed to annotate external IP: %v (%s)", err, string(out))
		} else {
			fmt.Printf("  -> External IP: %s\n", externalIP)
		}
	} else {
		fmt.Println("  -> Could not detect external IP (set manually with -host flag)")
	}

	// 6b: GPU info from NVML (primary) or nvidia-smi (fallback)
	var gpuModel, vramMB, gpuCount string
	gpuProvider := nvml.NewNVMLProvider()
	if err := gpuProvider.Init(); err == nil {
		defer gpuProvider.Shutdown()
		if specs, err := gpuProvider.GetSpecs(); err == nil && len(specs) > 0 {
			gpuModel = specs[0].Name
			vramMB = fmt.Sprintf("%d", specs[0].MemoryTotal)
			gpuCount = fmt.Sprintf("%d", len(specs))
		}
	}
	// Fallback: parse nvidia-smi if NVML failed
	if gpuModel == "" {
		gpuModel, vramMB, gpuCount = detectGPUFromSmi()
	}
	if gpuModel != "" {
		annotations := []string{
			"worldland.io/gpu-model=" + gpuModel,
			"worldland.io/gpu-vram-mb=" + vramMB,
			"worldland.io/gpu-count=" + gpuCount,
		}
		for _, ann := range annotations {
			cmd := exec.Command("kubectl", "--kubeconfig="+kubeconfig,
				"annotate", "node", hostname, ann, "--overwrite")
			cmd.CombinedOutput()
		}
		fmt.Printf("  -> GPU: %s (%s MB VRAM) x%s\n", gpuModel, vramMB, gpuCount)
	} else {
		fmt.Println("  -> No GPU detected")
	}

	cli.PrintSuccess("Successfully joined K8s cluster!")
}

// =====================================================================
// status command
// =====================================================================

func runStatusCmd() {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL (required)")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	fs.Parse(os.Args[2:])

	hubClient := authenticateAndCreateClient(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain)

	// Get provider info
	provider, err := hubClient.GetMyProvider()
	if err != nil {
		log.Fatalf("Failed to get provider info: %v", err)
	}
	cli.PrintProviderInfo(provider)

	// Get nodes
	nodes, err := hubClient.ListNodes()
	if err != nil {
		log.Printf("Failed to list nodes: %v", err)
	} else {
		cli.PrintNodesTable(nodes)
	}

	// Get mining status
	miningStatus, err := hubClient.GetMiningStatus(provider.ID)
	if err != nil {
		log.Printf("Failed to get mining status: %v", err)
	} else {
		cli.PrintMiningStatus(miningStatus)
	}

	fmt.Println()
}

// =====================================================================
// nodes command
// =====================================================================

func runNodesCmd() {
	fs := flag.NewFlagSet("nodes", flag.ExitOnError)
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL (required)")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	fs.Parse(os.Args[2:])

	hubClient := authenticateAndCreateClient(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain)

	nodes, err := hubClient.ListNodes()
	if err != nil {
		log.Fatalf("Failed to list nodes: %v", err)
	}
	cli.PrintNodesTable(nodes)
	fmt.Println()
}

// =====================================================================
// mining command (start/stop/status)
// =====================================================================

func runMiningCmd() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: node mining <start|stop|status> [flags]")
		os.Exit(1)
	}

	action := os.Args[2]
	// Shift args so flag parsing works for the subcommand
	os.Args = append([]string{os.Args[0]}, os.Args[3:]...)

	fs := flag.NewFlagSet("mining", flag.ExitOnError)
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL (required)")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	gpuCount := fs.Int("gpu-count", 1, "Number of GPUs for mining")
	fs.Parse(os.Args[1:])

	hubClient := authenticateAndCreateClient(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain)

	// Get provider ID
	provider, err := hubClient.GetMyProvider()
	if err != nil {
		log.Fatalf("Failed to get provider info: %v", err)
	}

	switch action {
	case "start":
		fmt.Printf("Starting mining with %d GPUs...\n", *gpuCount)
		if err := hubClient.StartMining(provider.ID, *gpuCount); err != nil {
			log.Fatalf("Failed to start mining: %v", err)
		}
		fmt.Println("Mining started successfully!")

	case "stop":
		fmt.Println("Stopping mining...")
		if err := hubClient.StopMining(provider.ID); err != nil {
			log.Fatalf("Failed to stop mining: %v", err)
		}
		fmt.Println("Mining stopped successfully!")

	case "status":
		miningStatus, err := hubClient.GetMiningStatus(provider.ID)
		if err != nil {
			log.Fatalf("Failed to get mining status: %v", err)
		}
		cli.PrintMiningStatus(miningStatus)
		fmt.Println()

	default:
		fmt.Fprintf(os.Stderr, "Unknown mining action: %s (use start/stop/status)\n", action)
		os.Exit(1)
	}
}

// =====================================================================
// price command
// =====================================================================

func runPriceCmd() {
	fs := flag.NewFlagSet("price", flag.ExitOnError)
	hubHTTP := fs.String("hub-http", "", "Hub HTTP API URL (required)")
	privateKey := fs.String("private-key", "", "Ethereum private key (hex)")
	privateKeyFile := fs.String("private-key-file", "", "Path to file containing private key")
	siweDomain := fs.String("siwe-domain", "", "SIWE domain for authentication")
	nodeID := fs.String("node-id", "", "Node ID to update (required)")
	price := fs.String("price", "", "New price per second in wei (required)")
	fs.Parse(os.Args[2:])

	if *nodeID == "" || *price == "" {
		fmt.Println("Usage: node price -hub-http=... -private-key=... -node-id=ID -price=WEI")
		os.Exit(1)
	}

	hubClient := authenticateAndCreateClient(*hubHTTP, *privateKey, *privateKeyFile, *siweDomain)

	fmt.Printf("Updating price for node %s to %s wei/sec...\n", *nodeID, *price)
	if err := hubClient.UpdateNodePrice(*nodeID, *price); err != nil {
		log.Fatalf("Failed to update price: %v", err)
	}
	fmt.Println("Price updated successfully!")
}

// =====================================================================
// Shared helpers
// =====================================================================

// authenticateAndCreateClient performs SIWE auth and returns a HubClient
func authenticateAndCreateClient(hubHTTP, privKey, privKeyFile, siweDomain string) *cli.HubClient {
	if hubHTTP == "" {
		log.Fatal("Required flag: -hub-http")
	}

	privKeyHex := resolvePrivateKey(privKey, privKeyFile)
	if privKeyHex == "" {
		log.Fatal("Required flag: -private-key or -private-key-file")
	}

	siweClient, err := auth.NewSIWEClientWithDomain(hubHTTP, privKeyHex, siweDomain)
	if err != nil {
		log.Fatalf("Failed to create SIWE client: %v", err)
	}

	if err := siweClient.Login(); err != nil {
		log.Fatalf("SIWE authentication failed: %v", err)
	}

	return cli.NewHubClient(hubHTTP, siweClient.GetToken())
}

// authenticateForWorker authenticates and returns the token string
func authenticateForWorker(hubHTTP, privKeyHex, siweDomain string) string {
	if privKeyHex == "" {
		return ""
	}

	siweClient, err := auth.NewSIWEClientWithDomain(hubHTTP, privKeyHex, siweDomain)
	if err != nil {
		log.Fatalf("Failed to create SIWE client: %v", err)
	}
	if err := siweClient.Login(); err != nil {
		log.Fatalf("SIWE authentication failed: %v", err)
	}
	log.Println("Worker authenticated successfully")
	return siweClient.GetToken()
}

// =====================================================================
// Original master/worker implementations
// =====================================================================

func runMaster(hubAddr, hubHTTP, privKey, privKeyFile, siweDomain string,
	certFile, keyFile, caFile, certDir, nodeID *string, kubeconfigPath string) {

	log.Println("Worldland SDK V4 starting (master mode)...")

	privKeyHex := resolvePrivateKey(privKey, privKeyFile)
	if privKeyHex == "" {
		log.Fatal("Master mode requires -private-key or -private-key-file")
	}

	hubHTTPURL := resolveHubHTTP(hubHTTP, hubAddr)
	log.Printf("Authenticating with Hub at %s...", hubHTTPURL)

	siweClient, err := auth.NewSIWEClientWithDomain(hubHTTPURL, privKeyHex, siweDomain)
	if err != nil {
		log.Fatalf("Failed to create SIWE client: %v", err)
	}
	log.Printf("Wallet address: %s", siweClient.GetAddress())

	if err := siweClient.Login(); err != nil {
		log.Fatalf("SIWE authentication failed: %v", err)
	}
	log.Println("SIWE authentication successful")

	setCertDefaults(certFile, keyFile, caFile, *certDir)
	if !certsExist(*certFile, *keyFile, *caFile) {
		bootstrapCerts(siweClient, certDir, certFile, keyFile, caFile)
	}

	if kubeconfigPath != "" {
		kubeconfigData, err := os.ReadFile(kubeconfigPath)
		if err != nil {
			log.Fatalf("Failed to read kubeconfig: %v", err)
		}

		providerID, err := siweClient.RegisterK8sProvider(string(kubeconfigData))
		if err != nil {
			log.Printf("K8s provider registration: %v (may already exist)", err)
		} else {
			log.Printf("K8s provider registered: %s", providerID)
		}
	}

	gpuType, memoryGB := detectGPU()
	deviceUUID := getDeviceUUID(gpuType == "CPU Node")
	registeredNodeID, err := siweClient.RegisterNode(deviceUUID, gpuType, memoryGB, "277777777777777")
	if err != nil {
		log.Printf("Node registration: %v (may already exist)", err)
	} else {
		log.Printf("Node registered: %s", registeredNodeID)
		if *nodeID == "" {
			*nodeID = registeredNodeID
		}
	}

	cert, caCertPool := loadCerts(*certFile, *keyFile, *caFile)

	if *nodeID == "" {
		parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			log.Fatalf("Failed to parse certificate: %v", err)
		}
		*nodeID = parsedCert.Subject.CommonName
	}

	gpuProvider := initGPUProvider()

	daemon := services.NewNodeDaemon(gpuProvider, *nodeID)
	if err := daemon.ConnectToHub(hubAddr, cert, caCertPool); err != nil {
		log.Fatalf("Failed to connect to Hub: %v", err)
	}

	go func() {
		if err := daemon.Start(); err != nil {
			log.Fatalf("Daemon error: %v", err)
		}
	}()

	log.Printf("SDK master mode running (node: %s)", *nodeID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	daemon.Stop()
	log.Println("Shutdown complete")
}

func runWorker(hubHTTP, privKey, privKeyFile, siweDomain, masterWallet string) {
	if masterWallet == "" {
		log.Fatal("Worker mode requires -master-wallet")
	}

	privKeyHex := resolvePrivateKey(privKey, privKeyFile)

	hubHTTPURL := hubHTTP
	if hubHTTPURL == "" {
		log.Fatal("Worker mode requires -hub-http")
	}

	log.Printf("Requesting join token for master wallet %s...", masterWallet)

	var authToken string
	if privKeyHex != "" {
		siweClient, err := auth.NewSIWEClientWithDomain(hubHTTPURL, privKeyHex, siweDomain)
		if err != nil {
			log.Fatalf("Failed to create SIWE client: %v", err)
		}
		if err := siweClient.Login(); err != nil {
			log.Fatalf("SIWE authentication failed: %v", err)
		}
		log.Println("Worker authenticated successfully")
		authToken = siweClient.GetToken()

		gpuType, memoryGB := detectGPU()
		log.Printf("Hardware detected: %s (%d GB)", gpuType, memoryGB)
	}

	joinToken, err := requestJoinToken(hubHTTPURL, masterWallet, authToken)
	if err != nil {
		log.Fatalf("Failed to get join token: %v", err)
	}

	log.Println("===========================================")
	log.Println("K8s Join Command (run as root on this machine):")
	log.Println("===========================================")
	log.Println(joinToken)
	log.Println("===========================================")
}

// =====================================================================
// Utility functions
// =====================================================================

func defaultCertDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".worldland/certs"
	}
	return filepath.Join(home, ".worldland", "certs")
}

func certsExist(certFile, keyFile, caFile string) bool {
	for _, f := range []string{certFile, keyFile, caFile} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func saveCertificates(certDir string, bundle *auth.CertificateBundle) (certPath, keyPath, caPath string, err error) {
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return "", "", "", err
	}

	certPath = filepath.Join(certDir, "node.crt")
	keyPath = filepath.Join(certDir, "node.key")
	caPath = filepath.Join(certDir, "ca.crt")

	if err := os.WriteFile(certPath, []byte(bundle.Certificate), 0644); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(keyPath, []byte(bundle.PrivateKey), 0600); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(caPath, []byte(bundle.CACertificate), 0644); err != nil {
		return "", "", "", err
	}

	return certPath, keyPath, caPath, nil
}

func resolvePrivateKey(privKey, privKeyFile string) string {
	if privKey != "" {
		return privKey
	}
	if privKeyFile != "" {
		data, err := os.ReadFile(privKeyFile)
		if err != nil {
			log.Fatalf("Failed to read private key file: %v", err)
		}
		return strings.TrimSpace(string(data))
	}
	return ""
}

func resolveHubHTTP(hubHTTP, hubAddr string) string {
	if hubHTTP != "" {
		return hubHTTP
	}
	hubHost := strings.Split(hubAddr, ":")[0]
	return "http://" + hubHost + ":8080"
}

func setCertDefaults(certFile, keyFile, caFile *string, certDir string) {
	if *certFile == "" {
		*certFile = filepath.Join(certDir, "node.crt")
	}
	if *keyFile == "" {
		*keyFile = filepath.Join(certDir, "node.key")
	}
	if *caFile == "" {
		*caFile = filepath.Join(certDir, "ca.crt")
	}
}

func bootstrapCerts(siweClient *auth.SIWEClient, certDir, certFile, keyFile, caFile *string) {
	log.Println("Certificates not found, requesting bootstrap certificate from Hub...")
	bundle, err := siweClient.IssueCertificate()
	if err != nil {
		log.Fatalf("Failed to issue bootstrap certificate: %v", err)
	}

	certPath, keyPath, caPath, err := saveCertificates(*certDir, bundle)
	if err != nil {
		log.Fatalf("Failed to save certificates: %v", err)
	}

	*certFile = certPath
	*keyFile = keyPath
	*caFile = caPath
	log.Printf("Bootstrap certificates saved to %s", *certDir)
}

func loadCerts(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("Failed to load certificate: %v", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		log.Fatalf("Failed to read CA certificate: %v", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		log.Fatal("Failed to parse CA certificate")
	}

	return cert, caCertPool
}

func detectGPU() (gpuType string, memoryGB int) {
	provider := nvml.NewNVMLProvider()
	if err := provider.Init(); err != nil {
		return "CPU Node", 1
	}
	defer provider.Shutdown()

	specs, err := provider.GetSpecs()
	if err != nil || len(specs) == 0 {
		return "CPU Node", 1
	}

	return specs[0].Name, int(specs[0].MemoryTotal / 1024)
}

func getDeviceUUID(isCPU bool) string {
	if isCPU {
		hostname, _ := os.Hostname()
		return "CPU-" + hostname
	}
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

func initGPUProvider() domain.GPUProvider {
	provider := nvml.NewNVMLProvider()
	if err := provider.Init(); err != nil {
		log.Printf("No GPU detected, using mock provider")
		return nvml.NewMockGPUProvider(
			[]domain.GPUMetrics{{UUID: "cpu-node", Name: "CPU Only"}},
			[]domain.GPUSpec{{UUID: "cpu-node", Name: "CPU Only", DriverVer: "N/A"}},
		)
	}
	provider.Shutdown()
	return nvml.NewNVMLProvider()
}

func requestJoinToken(hubHTTP, masterWallet, authToken string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/providers/%s/join-token", hubHTTP, masterWallet)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request join token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("join token request failed: %d - %s", resp.StatusCode, string(body))
	}

	var result struct {
		JoinCommand string `json:"joinCommand"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	return result.JoinCommand, nil
}

// detectExternalIP auto-detects the external IP address.
// Tries GCP metadata first, then falls back to ipify.
func detectExternalIP() string {
	// 1. GCP metadata
	req, err := http.NewRequest("GET",
		"http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip",
		nil)
	if err == nil {
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if ip := strings.TrimSpace(string(body)); ip != "" {
				return ip
			}
		}
	}
	// 2. ipify (universal)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("https://api.ipify.org")
	if err == nil && resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return strings.TrimSpace(string(body))
	}
	return ""
}

// detectGPUFromSmi parses nvidia-smi output to get GPU model, VRAM, and count.
// Used as fallback when NVML is not available (e.g., older glibc).
func detectGPUFromSmi() (model, vramMB, count string) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return "", "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return "", "", ""
	}
	parts := strings.SplitN(lines[0], ", ", 2)
	if len(parts) < 2 {
		return "", "", ""
	}
	model = strings.TrimSpace(parts[0])
	vramMB = strings.TrimSpace(parts[1])
	count = fmt.Sprintf("%d", len(lines))
	return model, vramMB, count
}
