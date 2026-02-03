//go:build !nonvml
// +build !nonvml

package nvml

import (
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/worldland/worldland-node/internal/domain"
)

type NVMLProvider struct{}

func NewNVMLProvider() *NVMLProvider {
	return &NVMLProvider{}
}

func (p *NVMLProvider) Init() error {
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("NVML init failed: %v", nvml.ErrorString(ret))
	}
	return nil
}

func (p *NVMLProvider) Shutdown() error {
	ret := nvml.Shutdown()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("NVML shutdown failed: %v", nvml.ErrorString(ret))
	}
	return nil
}

func (p *NVMLProvider) GetDeviceCount() (int, error) {
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return 0, fmt.Errorf("failed to get device count: %v", nvml.ErrorString(ret))
	}
	return count, nil
}

func (p *NVMLProvider) GetMetrics() ([]domain.GPUMetrics, error) {
	count, err := p.GetDeviceCount()
	if err != nil {
		return nil, err
	}

	metrics := make([]domain.GPUMetrics, 0, count)
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue // Skip failed device
		}

		uuid, _ := device.GetUUID()
		name, _ := device.GetName()
		memInfo, _ := device.GetMemoryInfo()
		util, _ := device.GetUtilizationRates()
		temp, _ := device.GetTemperature(nvml.TEMPERATURE_GPU)

		metrics = append(metrics, domain.GPUMetrics{
			UUID:        uuid,
			Name:        name,
			MemoryTotal: memInfo.Total / (1024 * 1024),
			MemoryUsed:  memInfo.Used / (1024 * 1024),
			GPUUtil:     util.Gpu,
			MemoryUtil:  util.Memory,
			Temperature: temp,
		})
	}
	return metrics, nil
}

func (p *NVMLProvider) GetSpecs() ([]domain.GPUSpec, error) {
	count, err := p.GetDeviceCount()
	if err != nil {
		return nil, err
	}

	specs := make([]domain.GPUSpec, 0, count)
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		uuid, _ := device.GetUUID()
		name, _ := device.GetName()
		memInfo, _ := device.GetMemoryInfo()
		driver, _ := nvml.SystemGetDriverVersion()

		specs = append(specs, domain.GPUSpec{
			UUID:        uuid,
			Name:        name,
			MemoryTotal: memInfo.Total / (1024 * 1024),
			DriverVer:   driver,
		})
	}
	return specs, nil
}

// Compile-time interface check
var _ domain.GPUProvider = (*NVMLProvider)(nil)
