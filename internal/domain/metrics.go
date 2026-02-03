package domain

// GPUMetrics represents collected GPU metrics from NVML
type GPUMetrics struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	MemoryTotal uint64 `json:"memory_total_mb"`
	MemoryUsed  uint64 `json:"memory_used_mb"`
	GPUUtil     uint32 `json:"gpu_util_percent"`
	MemoryUtil  uint32 `json:"memory_util_percent"`
	Temperature uint32 `json:"temperature_c"`
}

// GPUSpec represents static GPU specifications for node registration
type GPUSpec struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	MemoryTotal uint64 `json:"memory_total_mb"`
	DriverVer   string `json:"driver_version"`
}
