package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ComponentStatus represents the installation status of a required component
type ComponentStatus struct {
	Name      string
	Installed bool
	Version   string
}

// PreflightResult contains the results of the preflight check
type PreflightResult struct {
	Components []ComponentStatus
	OSId       string // "ubuntu", "debian", etc.
	OSVersion  string // "22.04", "12", etc.
	GPUFound   bool
	GPUName    string
}

// RunPreflight checks for all required components and system info
func RunPreflight() (*PreflightResult, error) {
	result := &PreflightResult{}

	// Detect OS
	result.OSId, result.OSVersion = detectOS()

	// Detect GPU
	result.GPUFound, result.GPUName = detectNvidiaGPU()

	// Check components
	result.Components = []ComponentStatus{
		checkComponent("nvidia-smi", "nvidia-smi", "--query-gpu=driver_version --format=csv,noheader"),
		checkComponent("containerd", "containerd", "--version"),
		checkComponent("kubeadm", "kubeadm", "version -o short"),
		checkComponent("kubelet", "kubelet", "--version"),
		checkComponent("kubectl", "kubectl", "version --client --short 2>/dev/null || kubectl version --client -o yaml 2>/dev/null | head -1"),
		checkComponent("nvidia-ctk", "nvidia-ctk", "--version"),
	}

	return result, nil
}

// MissingComponents returns the names of components that are not installed
func (r *PreflightResult) MissingComponents() []string {
	var missing []string
	for _, c := range r.Components {
		if !c.Installed {
			missing = append(missing, c.Name)
		}
	}
	return missing
}

// PrintStatus prints the preflight check results
func (r *PreflightResult) PrintStatus() {
	for _, c := range r.Components {
		if c.Installed {
			fmt.Printf("  \u2713 %s: %s\n", c.Name, c.Version)
		} else {
			fmt.Printf("  \u2717 %s: NOT INSTALLED\n", c.Name)
		}
	}
	fmt.Printf("  OS: %s %s\n", r.OSId, r.OSVersion)
	if r.GPUFound {
		fmt.Printf("  GPU: %s\n", r.GPUName)
	}
}

func checkComponent(name, binary, versionArgs string) ComponentStatus {
	cs := ComponentStatus{Name: name}

	// Check if binary exists
	path, err := exec.LookPath(binary)
	if err != nil {
		return cs
	}
	_ = path

	// Get version
	cmd := exec.Command("bash", "-c", binary+" "+versionArgs)
	out, err := cmd.Output()
	if err != nil {
		// Binary exists but version command failed — still installed
		cs.Installed = true
		cs.Version = "(version unknown)"
		return cs
	}

	cs.Installed = true
	cs.Version = strings.TrimSpace(string(out))
	// Truncate long version strings
	if len(cs.Version) > 60 {
		cs.Version = cs.Version[:60]
	}
	return cs
}

func detectOS() (id, version string) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "unknown", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), "\"")
		}
		if strings.HasPrefix(line, "VERSION_ID=") {
			version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
		}
	}
	return id, version
}

func detectNvidiaGPU() (found bool, name string) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	out, err := cmd.Output()
	if err != nil {
		return false, ""
	}
	gpuName := strings.TrimSpace(string(out))
	if gpuName == "" {
		return false, ""
	}
	// Take first line if multiple GPUs
	lines := strings.Split(gpuName, "\n")
	return true, strings.TrimSpace(lines[0])
}
