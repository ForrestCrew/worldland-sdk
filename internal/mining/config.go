package mining

// MiningConfig holds configuration for the mining daemon
type MiningConfig struct {
	// Enabled controls whether mining runs when GPU is idle
	Enabled bool

	// WalletAddress is the Ethereum address for mining rewards
	WalletAddress string

	// Image is the Docker image for the mining container
	// Default: "mingeyom/worldland-mio:latest"
	Image string

	// GPUDeviceIDs lists GPU UUIDs to use for mining
	// If empty, all available GPUs will be used
	GPUDeviceIDs []string

	// DataDir is the host path for blockchain data persistence
	// Default: "/data/worldland"
	DataDir string

	// ChainID for the Worldland network
	// Default: 10396
	ChainID int

	// P2PPort for blockchain P2P networking
	// Default: 30303
	P2PPort int

	// HTTPRPCPort for blockchain HTTP RPC
	// Default: 8545
	HTTPRPCPort int

	// ExtraArgs are additional arguments for the mining node
	ExtraArgs []string
}

// DefaultMiningConfig returns default mining configuration
func DefaultMiningConfig() MiningConfig {
	return MiningConfig{
		Enabled:     true,
		Image:       "mingeyom/worldland-mio:latest",
		DataDir:     "/data/worldland",
		ChainID:     10396,
		P2PPort:     30303,
		HTTPRPCPort: 8545,
	}
}

// Validate checks that the mining config is valid
func (c *MiningConfig) Validate() error {
	if c.Enabled && c.WalletAddress == "" {
		// Wallet is optional for mining - mio node can mine without explicit wallet
		// It will use the coinbase account
	}
	return nil
}
