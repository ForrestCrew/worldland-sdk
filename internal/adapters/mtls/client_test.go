package mtls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/worldland/worldland-node/internal/adapters/mtls"
)

// Test helper: Generate test CA certificate
func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate CA key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Test CA",
			Organization: []string{"Worldland Test"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("failed to create CA certificate: %v", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("failed to parse CA certificate: %v", err)
	}

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	return caCert, caKey, caCertPEM
}

// Test helper: Generate client/server certificate signed by CA
func generateCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, isServer bool) tls.Certificate {
	t.Helper()

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Worldland GPU Network"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
	}

	if isServer {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.DNSNames = []string{"localhost"}
		// Add IP SAN for 127.0.0.1
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &certKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(certKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to create key pair: %v", err)
	}

	return cert
}

func TestClient_ConnectsToHub(t *testing.T) {
	// Generate test CA
	caCert, caKey, caCertPEM := generateTestCA(t)

	// Generate server certificate
	serverCert := generateCert(t, caCert, caKey, "localhost", true)

	// Generate client certificate
	clientCert := generateCert(t, caCert, caKey, "test-node", false)

	// Create CA pool
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	// Start mock Hub server
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}

	listener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer listener.Close()

	hubAddr := listener.Addr().String()

	// Accept connections in background
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Keep connection alive
		buf := make([]byte, 1024)
		conn.Read(buf)
	}()

	// Create client
	client := mtls.NewClient(hubAddr, clientCert, caPool)

	// Connect
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	t.Log("Client successfully connected to Hub via mTLS")
}

func TestClient_ReceivesCommand(t *testing.T) {
	// Generate test CA
	caCert, caKey, caCertPEM := generateTestCA(t)

	// Generate server and client certificates
	serverCert := generateCert(t, caCert, caKey, "localhost", true)
	clientCert := generateCert(t, caCert, caKey, "test-node", false)

	// Create CA pool
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	// Start mock Hub server
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}

	listener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer listener.Close()

	hubAddr := listener.Addr().String()

	// Channel to signal command received
	commandReceived := make(chan mtls.Command, 1)

	// Accept connections and send command
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Send command to client
		command := mtls.Command{
			ID:      "cmd-123",
			Type:    "start_job",
			Payload: map[string]interface{}{"job_id": "job-456"},
		}
		data, _ := json.Marshal(command)
		conn.Write(data)

		// Keep connection alive
		buf := make([]byte, 1024)
		conn.Read(buf)
	}()

	// Create client with command handler
	client := mtls.NewClient(hubAddr, clientCert, caPool)
	client.OnCommand = func(cmd mtls.Command) mtls.CommandAck {
		commandReceived <- cmd
		return mtls.CommandAck{CommandID: cmd.ID, Status: "ok"}
	}

	// Connect and listen
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	// Start listening in background
	go client.Listen()

	// Wait for command
	select {
	case cmd := <-commandReceived:
		if cmd.Type != "start_job" {
			t.Errorf("expected command type 'start_job', got %q", cmd.Type)
		}
		if cmd.ID != "cmd-123" {
			t.Errorf("expected command ID 'cmd-123', got %q", cmd.ID)
		}
		t.Log("Command successfully received")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for command")
	}
}

func TestClient_EnforcesTLS13(t *testing.T) {
	// Generate test CA
	caCert, caKey, caCertPEM := generateTestCA(t)

	// Generate certificates
	serverCert := generateCert(t, caCert, caKey, "localhost", true)
	clientCert := generateCert(t, caCert, caKey, "test-node", false)

	// Create CA pool
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	// Start server with TLS 1.2 only
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12, // Force TLS 1.2
	}

	listener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer listener.Close()

	hubAddr := listener.Addr().String()

	// Accept connections
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			conn.Close()
		}
	}()

	// Create client (requires TLS 1.3)
	client := mtls.NewClient(hubAddr, clientCert, caPool)

	// Connection should fail due to TLS version mismatch
	err = client.Connect()
	if err == nil {
		client.Close()
		t.Fatal("connection should fail with TLS 1.2 server (client requires TLS 1.3)")
	}

	t.Logf("Client correctly enforces TLS 1.3: %v", err)
}
