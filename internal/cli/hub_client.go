package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HubClient wraps Hub REST API calls with authentication
type HubClient struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// NewHubClient creates a new Hub API client
func NewHubClient(baseURL, authToken string) *HubClient {
	return &HubClient{
		baseURL:   baseURL,
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// --- Response types ---

// ProviderInfo represents provider information from Hub
type ProviderInfo struct {
	ID            string        `json:"id"`
	WalletAddress string        `json:"walletAddress"`
	ProviderType  string        `json:"providerType"`
	Status        string        `json:"status"`
	ClusterHost   *string       `json:"clusterHost,omitempty"`
	K8sNodes      []K8sNodeInfo `json:"k8sNodes,omitempty"`
	CreatedAt     string        `json:"createdAt"`
	UpdatedAt     string        `json:"updatedAt"`
}

// K8sNodeInfo represents a K8s node from provider info
type K8sNodeInfo struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Roles      []string `json:"roles"`
	KubeletVer string   `json:"kubeletVersion"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	CPUs       string   `json:"cpus"`
	Memory     string   `json:"memory"`
}

// NodeInfo represents a node from Hub
type NodeInfo struct {
	ID             string `json:"id"`
	ProviderID     string `json:"providerId"`
	GPUUUID        string `json:"gpuUuid"`
	GPUType        string `json:"gpuType"`
	MemoryGB       int    `json:"memoryGb"`
	PricePerSecond string `json:"pricePerSecond"`
	PricePerHour   string `json:"pricePerHour"`
	APIEndpoint    string `json:"apiEndpoint"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// MiningStatus represents mining status from Hub
type MiningStatus struct {
	Mining     interface{}    `json:"mining,omitempty"`
	Allocation *GPUAllocation `json:"allocation,omitempty"`
}

// GPUAllocation represents GPU pool allocation
type GPUAllocation struct {
	TotalGPUs     int `json:"totalGPUs"`
	MiningGPUs    int `json:"miningGPUs"`
	RentalGPUs    int `json:"rentalGPUs"`
	AvailableGPUs int `json:"availableGPUs"`
}

// JoinTokenResponse represents a join token response
type JoinTokenResponse struct {
	JoinCommand string `json:"joinCommand"`
	Token       string `json:"token"`
	CAHash      string `json:"caHash"`
	MasterAddr  string `json:"masterAddr"`
	ExpiresIn   string `json:"expiresIn"`
}

// --- Provider API ---

// GetMyProvider returns the authenticated provider's info
func (c *HubClient) GetMyProvider() (*ProviderInfo, error) {
	var info ProviderInfo
	if err := c.doGet("/api/v1/providers/me", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// --- Node API ---

// ListNodes returns all nodes for the authenticated provider
func (c *HubClient) ListNodes() ([]NodeInfo, error) {
	var resp struct {
		Nodes []NodeInfo `json:"nodes"`
		Count int        `json:"count"`
	}
	if err := c.doGet("/api/v1/nodes", &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

// UpdateNodePrice updates the price for a specific node
func (c *HubClient) UpdateNodePrice(nodeID, pricePerSec string) error {
	payload := map[string]string{
		"price_per_sec": pricePerSec,
	}
	return c.doPatchJSON(fmt.Sprintf("/api/v1/nodes/%s/price", nodeID), payload, nil)
}

// --- Mining API ---

// GetMiningStatus returns the mining status for a provider
func (c *HubClient) GetMiningStatus(providerID string) (*MiningStatus, error) {
	var status MiningStatus
	if err := c.doGet(fmt.Sprintf("/api/v1/mining/%s/status", providerID), &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// StartMining starts mining for a provider
func (c *HubClient) StartMining(providerID string, gpuCount int) error {
	payload := map[string]interface{}{
		"gpuCount": gpuCount,
	}
	return c.doPostJSON(fmt.Sprintf("/api/v1/mining/%s/start", providerID), payload, nil)
}

// StopMining stops mining for a provider
func (c *HubClient) StopMining(providerID string) error {
	return c.doPostJSON(fmt.Sprintf("/api/v1/mining/%s/stop", providerID), nil, nil)
}

// AllocateMiningGPU allocates GPUs for mining
func (c *HubClient) AllocateMiningGPU(providerID string, count int) error {
	payload := map[string]interface{}{
		"gpuCount": count,
	}
	return c.doPostJSON(fmt.Sprintf("/api/v1/mining/%s/allocate", providerID), payload, nil)
}

// ReleaseMiningGPU releases GPUs from mining
func (c *HubClient) ReleaseMiningGPU(providerID string, count int) error {
	payload := map[string]interface{}{
		"gpuCount": count,
	}
	return c.doPostJSON(fmt.Sprintf("/api/v1/mining/%s/release", providerID), payload, nil)
}

// --- Join Token API ---

// GetJoinToken requests a K8s join token for a provider's cluster
func (c *HubClient) GetJoinToken(wallet string) (*JoinTokenResponse, error) {
	var resp JoinTokenResponse
	if err := c.doGet(fmt.Sprintf("/api/v1/providers/%s/join-token", wallet), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// --- HTTP helpers ---

func (c *HubClient) doGet(path string, result interface{}) error {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.doRequest(req, result)
}

func (c *HubClient) doPostJSON(path string, payload interface{}, result interface{}) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest("POST", c.baseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.doRequest(req, result)
}

func (c *HubClient) doPatchJSON(path string, payload interface{}, result interface{}) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest("PATCH", c.baseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.doRequest(req, result)
}

func (c *HubClient) doRequest(req *http.Request, result interface{}) error {
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	if result != nil && len(body) > 0 {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}
	}

	return nil
}
