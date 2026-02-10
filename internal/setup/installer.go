package setup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// InstallMissing installs the missing components on Ubuntu/Debian systems
func InstallMissing(missing []string, osID string) error {
	if osID != "ubuntu" && osID != "debian" {
		return fmt.Errorf("automatic installation only supported on Ubuntu/Debian (detected: %s)", osID)
	}

	for _, component := range missing {
		fmt.Printf("  -> Installing %s...\n", component)
		var err error
		switch component {
		case "containerd":
			err = installContainerd()
		case "kubeadm", "kubelet", "kubectl":
			err = installK8sComponents()
		case "nvidia-ctk":
			err = installNvidiaCtk()
		case "nvidia-smi":
			fmt.Printf("     nvidia-smi requires NVIDIA driver — install manually\n")
			continue
		default:
			fmt.Printf("     Unknown component: %s (skipping)\n", component)
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to install %s: %w", component, err)
		}
	}
	return nil
}

func runScript(name, script string) error {
	cmd := exec.Command("bash", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func installContainerd() error {
	script := `
set -e
apt-get update -qq
apt-get install -y -qq containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd
`
	return runScript("containerd install", script)
}

// installK8sComponents installs kubeadm, kubelet, and kubectl together
// since they should all be at the same version
func installK8sComponents() error {
	// Check if already partially installed to avoid re-running
	if isInstalled("kubeadm") && isInstalled("kubelet") && isInstalled("kubectl") {
		return nil
	}

	script := `
set -e

# Install prerequisites (conntrack, socat, ebtables are required by kubeadm)
apt-get update -qq
apt-get install -y -qq apt-transport-https ca-certificates curl gpg conntrack socat ebtables

# Add Kubernetes apt repository (v1.29 — must match master ±1 minor)
KUBE_VERSION="v1.29"
mkdir -p /etc/apt/keyrings
curl -fsSL "https://pkgs.k8s.io/core:/stable:/${KUBE_VERSION}/deb/Release.key" | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg 2>/dev/null || true
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${KUBE_VERSION}/deb/ /" > /etc/apt/sources.list.d/kubernetes.list

# Install
apt-get update -qq
apt-get install -y -qq kubelet kubeadm kubectl

# Hold versions to prevent auto-upgrade
apt-mark hold kubelet kubeadm kubectl

# Enable kubelet (it will wait for kubeadm config)
systemctl enable kubelet
`
	return runScript("K8s components install", script)
}

func installNvidiaCtk() error {
	script := `
set -e

# Add NVIDIA container toolkit repo
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg 2>/dev/null || true
curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
    sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' > /etc/apt/sources.list.d/nvidia-container-toolkit.list

# Install
apt-get update -qq
apt-get install -y -qq nvidia-container-toolkit
`
	return runScript("nvidia-ctk install", script)
}

func isInstalled(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

// deduplicateK8s filters the missing list so k8s components are only installed once
func DeduplicateK8s(missing []string) []string {
	var result []string
	k8sAdded := false
	for _, m := range missing {
		if m == "kubeadm" || m == "kubelet" || m == "kubectl" {
			if !k8sAdded {
				result = append(result, "kubeadm")
				k8sAdded = true
			}
			continue
		}
		result = append(result, m)
	}
	return result
}

// NeedsK8s checks if any K8s component is in the missing list
func NeedsK8s(missing []string) bool {
	for _, m := range missing {
		if m == "kubeadm" || m == "kubelet" || m == "kubectl" {
			return true
		}
	}
	return false
}

// IsRoot checks if the current process is running as root
func IsRoot() bool {
	return os.Geteuid() == 0
}

// CheckRoot returns an error if not running as root
func CheckRoot() error {
	if !IsRoot() {
		return fmt.Errorf("this command requires root privileges — run with sudo")
	}
	return nil
}

// FormatMissing returns a formatted string of missing components
func FormatMissing(missing []string) string {
	return "[" + strings.Join(missing, ", ") + "]"
}
