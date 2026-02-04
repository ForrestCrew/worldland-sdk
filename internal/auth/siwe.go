package auth

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// SIWEClient handles Sign-In with Ethereum authentication
type SIWEClient struct {
	hubURL     string
	privateKey *ecdsa.PrivateKey
	address    string
	token      string // JWT token after successful login
	httpClient *http.Client
}

// NewSIWEClient creates a new SIWE authentication client
func NewSIWEClient(hubURL string, privateKeyHex string) (*SIWEClient, error) {
	// Remove 0x prefix if present
	privateKeyHex = strings.TrimPrefix(privateKeyHex, "0x")

	// Parse private key
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key hex: %w", err)
	}

	privateKey, err := crypto.ToECDSA(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Derive address from private key
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to get public key")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA).Hex()

	return &SIWEClient{
		hubURL:     strings.TrimSuffix(hubURL, "/"),
		privateKey: privateKey,
		address:    address,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// GetAddress returns the wallet address
func (c *SIWEClient) GetAddress() string {
	return c.address
}

// GetToken returns the JWT token (empty if not logged in)
func (c *SIWEClient) GetToken() string {
	return c.token
}

// Login performs SIWE authentication and stores the JWT token
func (c *SIWEClient) Login() error {
	// Step 1: Get nonce
	nonce, err := c.getNonce()
	if err != nil {
		return fmt.Errorf("failed to get nonce: %w", err)
	}

	// Step 2: Create SIWE message (EIP-4361 format)
	message := c.createSIWEMessage(nonce)

	// Step 3: Sign message
	signature, err := c.signMessage(message)
	if err != nil {
		return fmt.Errorf("failed to sign message: %w", err)
	}

	// Step 4: Login with signature
	token, err := c.loginWithSignature(message, signature)
	if err != nil {
		return fmt.Errorf("failed to login: %w", err)
	}

	c.token = token
	return nil
}

func (c *SIWEClient) getNonce() (string, error) {
	resp, err := c.httpClient.Get(c.hubURL + "/api/v1/auth/nonce")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("nonce request failed: %d - %s", resp.StatusCode, string(body))
	}

	var result struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Nonce, nil
}

func (c *SIWEClient) createSIWEMessage(nonce string) string {
	// Parse domain from hub URL
	domain := "localhost"
	if strings.Contains(c.hubURL, "://") {
		parts := strings.Split(c.hubURL, "://")
		if len(parts) > 1 {
			hostPart := strings.Split(parts[1], ":")[0]
			hostPart = strings.Split(hostPart, "/")[0]
			if hostPart != "" {
				domain = hostPart
			}
		}
	}

	uri := c.hubURL
	statement := "Sign in to Worldland GPU Rental Platform as Provider"
	version := "1"
	chainId := "56" // BNB Chain (use 31337 for Hardhat)
	issuedAt := time.Now().UTC().Format(time.RFC3339)

	// EIP-4361 message format
	message := fmt.Sprintf(`%s wants you to sign in with your Ethereum account:
%s

%s

URI: %s
Version: %s
Chain ID: %s
Nonce: %s
Issued At: %s`, domain, c.address, statement, uri, version, chainId, nonce, issuedAt)

	return message
}

func (c *SIWEClient) signMessage(message string) (string, error) {
	// Ethereum signed message prefix
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Sign the hash
	signature, err := crypto.Sign(hash.Bytes(), c.privateKey)
	if err != nil {
		return "", err
	}

	// Adjust v value for Ethereum compatibility (add 27)
	if signature[64] < 27 {
		signature[64] += 27
	}

	return "0x" + hex.EncodeToString(signature), nil
}

func (c *SIWEClient) loginWithSignature(message, signature string) (string, error) {
	payload := map[string]string{
		"message":   message,
		"signature": signature,
	}
	body, _ := json.Marshal(payload)

	resp, err := c.httpClient.Post(
		c.hubURL+"/api/v1/auth/login",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed: %d - %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.Token, nil
}

// RegisterNode registers the node with Hub using JWT authentication
func (c *SIWEClient) RegisterNode(gpuUUID, gpuType string, memoryGB int, pricePerSec string) (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("not authenticated - call Login() first")
	}

	payload := map[string]interface{}{
		"gpu_uuid":      gpuUUID,
		"gpu_type":      gpuType,
		"memory_gb":     memoryGB,
		"price_per_sec": pricePerSec,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", c.hubURL+"/api/v1/nodes", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("registration failed: %d - %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.NodeID, nil
}
