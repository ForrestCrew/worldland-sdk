package domain

// GPUProvider abstracts GPU metrics collection for testing
type GPUProvider interface {
	// Init initializes the GPU provider (NVML or mock)
	Init() error
	// Shutdown cleanly shuts down the provider
	Shutdown() error
	// GetDeviceCount returns number of GPUs
	GetDeviceCount() (int, error)
	// GetMetrics returns current metrics for all GPUs
	GetMetrics() ([]GPUMetrics, error)
	// GetSpecs returns static specifications for all GPUs
	GetSpecs() ([]GPUSpec, error)
}
