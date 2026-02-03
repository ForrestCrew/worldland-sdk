//go:build nonvml
// +build nonvml

package nvml

import (
	"fmt"

	"github.com/worldland/worldland-node/internal/domain"
)

// NVMLProvider stub - used when building without NVIDIA libraries
type NVMLProvider struct{}

func NewNVMLProvider() *NVMLProvider {
	return &NVMLProvider{}
}

func (p *NVMLProvider) Init() error {
	return fmt.Errorf("NVML not available (built with nonvml tag)")
}

func (p *NVMLProvider) Shutdown() error {
	return nil
}

func (p *NVMLProvider) GetDeviceCount() (int, error) {
	return 0, fmt.Errorf("NVML not available")
}

func (p *NVMLProvider) GetMetrics() ([]domain.GPUMetrics, error) {
	return nil, fmt.Errorf("NVML not available")
}

func (p *NVMLProvider) GetSpecs() ([]domain.GPUSpec, error) {
	return nil, fmt.Errorf("NVML not available")
}

// Compile-time interface check
var _ domain.GPUProvider = (*NVMLProvider)(nil)
