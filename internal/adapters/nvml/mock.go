package nvml

import "github.com/worldland/worldland-node/internal/domain"

// MockGPUProvider provides fake GPU data for testing
type MockGPUProvider struct {
	Metrics []domain.GPUMetrics
	Specs   []domain.GPUSpec
	InitErr error
}

func NewMockGPUProvider(metrics []domain.GPUMetrics, specs []domain.GPUSpec) *MockGPUProvider {
	return &MockGPUProvider{Metrics: metrics, Specs: specs}
}

func (p *MockGPUProvider) Init() error {
	return p.InitErr
}

func (p *MockGPUProvider) Shutdown() error {
	return nil
}

func (p *MockGPUProvider) GetDeviceCount() (int, error) {
	return len(p.Metrics), nil
}

func (p *MockGPUProvider) GetMetrics() ([]domain.GPUMetrics, error) {
	return p.Metrics, nil
}

func (p *MockGPUProvider) GetSpecs() ([]domain.GPUSpec, error) {
	return p.Specs, nil
}

// Compile-time interface check
var _ domain.GPUProvider = (*MockGPUProvider)(nil)
