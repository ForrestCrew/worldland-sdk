package setup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecuteJoin runs kubeadm reset + join to add this node to a K8s cluster
func ExecuteJoin(joinCommand string) error {
	// Step 1: Reset any previous kubeadm state
	fmt.Println("  -> Resetting previous state...")
	if err := resetKubeadm(); err != nil {
		fmt.Printf("     Reset warning: %v (continuing)\n", err)
	}

	// Step 2: Clean up CNI to avoid conflicts
	fmt.Println("  -> Cleaning CNI state...")
	cleanCNI()

	// Step 2.5: Ensure containerd is running after reset
	fmt.Println("  -> Ensuring containerd is ready...")
	restartContainerd := exec.Command("systemctl", "restart", "containerd")
	restartContainerd.Run()
	time.Sleep(3 * time.Second)

	// Step 3: Execute kubeadm join
	fmt.Println("  -> Executing kubeadm join...")
	if err := runJoinCommand(joinCommand); err != nil {
		return fmt.Errorf("kubeadm join failed: %w", err)
	}

	// Step 4: Restart kubelet to ensure CNI plugin initializes properly
	fmt.Println("  -> Restarting kubelet for CNI initialization...")
	restartCmd := exec.Command("systemctl", "restart", "kubelet")
	restartCmd.Stdout = os.Stdout
	restartCmd.Stderr = os.Stderr
	if err := restartCmd.Run(); err != nil {
		fmt.Printf("     kubelet restart warning: %v (continuing)\n", err)
	}

	return nil
}

func resetKubeadm() error {
	cmd := exec.Command("kubeadm", "reset", "-f")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func cleanCNI() {
	// Remove CNI config and state to avoid conflicts
	paths := []string{"/etc/cni/net.d", "/var/lib/cni"}
	for _, p := range paths {
		os.RemoveAll(p)
	}

	// Delete stale network interfaces from previous joins
	// Without this, flannel fails: "cni0 already has an IP address different from ..."
	for _, iface := range []string{"cni0", "flannel.1"} {
		cmd := exec.Command("ip", "link", "delete", iface)
		cmd.Run() // Ignore errors — interface may not exist
	}
}

func runJoinCommand(joinCommand string) error {
	// Parse the join command — remove "sudo" prefix if present
	joinCommand = strings.TrimSpace(joinCommand)
	joinCommand = strings.TrimPrefix(joinCommand, "sudo ")

	// Split into args
	parts := strings.Fields(joinCommand)
	if len(parts) < 2 {
		return fmt.Errorf("invalid join command: %s", joinCommand)
	}

	// Verify it's a kubeadm join command
	if parts[0] != "kubeadm" || parts[1] != "join" {
		return fmt.Errorf("expected 'kubeadm join' command, got: %s %s", parts[0], parts[1])
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
