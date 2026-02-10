package setup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ConfigureRuntime configures containerd with CRI and NVIDIA runtime support
func ConfigureRuntime() error {
	// Step 0: K8s networking prerequisites (br_netfilter, ip_forward)
	fmt.Println("  -> Configuring K8s networking prerequisites...")
	if err := configureK8sNetworking(); err != nil {
		fmt.Printf("     Networking warning: %v (continuing)\n", err)
	}

	// Step 1: Generate containerd config if needed
	fmt.Println("  -> Generating containerd config...")
	if err := ensureContainerdConfig(); err != nil {
		return fmt.Errorf("containerd config: %w", err)
	}

	// Step 2: Configure NVIDIA runtime
	fmt.Println("  -> Configuring NVIDIA runtime...")
	if err := configureNvidiaRuntime(); err != nil {
		return fmt.Errorf("nvidia runtime: %w", err)
	}

	// Step 2.5: Set nvidia as default runtime in main config.toml
	fmt.Println("  -> Setting nvidia as default containerd runtime...")
	if err := ensureNvidiaDefaultRuntime(); err != nil {
		return fmt.Errorf("nvidia default runtime: %w", err)
	}

	// Step 3: Ensure SystemdCgroup = true
	fmt.Println("  -> Ensuring SystemdCgroup = true...")
	if err := ensureSystemdCgroup(); err != nil {
		return fmt.Errorf("systemd cgroup: %w", err)
	}

	// Step 4: Restart containerd and kubelet
	// Both must restart for CNI plugin to initialize properly after config changes
	fmt.Println("  -> Restarting containerd and kubelet...")
	if err := runScript("restart services", "systemctl restart containerd && sleep 2 && systemctl enable kubelet && systemctl restart kubelet"); err != nil {
		return err
	}

	// Step 5: Validate CRI
	fmt.Println("  -> Validating CRI...")
	if err := validateCRI(); err != nil {
		fmt.Printf("     CRI validation warning: %v (continuing)\n", err)
	} else {
		fmt.Println("  -> CRI validation OK")
	}

	return nil
}

func ensureContainerdConfig() error {
	configPath := "/etc/containerd/config.toml"

	needsRegenerate := false

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		needsRegenerate = true
	} else {
		// Check if CRI is disabled (Docker installs containerd with disabled_plugins = ["cri"])
		data, err := os.ReadFile(configPath)
		if err == nil && strings.Contains(string(data), `"cri"`) {
			needsRegenerate = true
		}
	}

	if needsRegenerate {
		if err := os.MkdirAll("/etc/containerd", 0755); err != nil {
			return err
		}
		cmd := exec.Command("containerd", "config", "default")
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("generate config: %w", err)
		}
		// Ensure conf.d imports are included
		config := string(out)
		if !strings.Contains(config, "conf.d") {
			config += "\nimports = [\"/etc/containerd/conf.d/*.toml\"]\n"
		}
		if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
			return err
		}
	}

	return nil
}

func configureNvidiaRuntime() error {
	// Check if nvidia-ctk is available
	if _, err := exec.LookPath("nvidia-ctk"); err != nil {
		return fmt.Errorf("nvidia-ctk not found — install nvidia-container-toolkit first")
	}

	// Write to conf.d drop-in file instead of main config.toml
	// This prevents nvidia-ctk from overwriting CRI settings with disabled_plugins = ["cri"]
	confDir := "/etc/containerd/conf.d"
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return err
	}

	cmd := exec.Command("nvidia-ctk", "runtime", "configure",
		"--runtime=containerd",
		"--config="+confDir+"/99-nvidia.toml",
		"--set-as-default")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ensureNvidiaDefaultRuntime sets nvidia as the default runtime in the main
// containerd config.toml. The nvidia-ctk --set-as-default flag only applies to
// the drop-in file (conf.d/99-nvidia.toml), but the K8s nvidia device plugin
// requires nvidia to be the default runtime in the main config to access GPU
// libraries.
func ensureNvidiaDefaultRuntime() error {
	configPath := "/etc/containerd/config.toml"

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	content := string(data)
	old := `default_runtime_name = "runc"`
	new := `default_runtime_name = "nvidia"`

	if strings.Contains(content, new) {
		fmt.Println("     Already set to nvidia, skipping")
		return nil
	}

	if !strings.Contains(content, old) {
		return fmt.Errorf("%s does not contain %q — cannot patch default runtime", configPath, old)
	}

	content = strings.Replace(content, old, new, 1)
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	fmt.Println("     Patched default_runtime_name to nvidia")
	return nil
}

func ensureSystemdCgroup() error {
	// Fix SystemdCgroup in both main config and conf.d files
	for _, configPath := range []string{
		"/etc/containerd/config.toml",
		"/etc/containerd/conf.d/99-nvidia.toml",
	} {
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		content := string(data)
		if strings.Contains(content, "SystemdCgroup = false") {
			content = strings.ReplaceAll(content, "SystemdCgroup = false", "SystemdCgroup = true")
			if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func configureK8sNetworking() error {
	script := `
modprobe br_netfilter
modprobe overlay

cat > /etc/modules-load.d/k8s.conf <<EOF
br_netfilter
overlay
EOF

cat > /etc/sysctl.d/k8s.conf <<EOF
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sysctl --system > /dev/null 2>&1
`
	return runScript("k8s networking", script)
}

func validateCRI() error {
	cmd := exec.Command("crictl", "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// crictl might not be installed, try containerd socket directly
		cmd2 := exec.Command("ctr", "--address", "/run/containerd/containerd.sock", "version")
		out2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("CRI not accessible: %s / %s", string(out), string(out2))
		}
		_ = out2
		return nil
	}
	_ = out
	return nil
}
