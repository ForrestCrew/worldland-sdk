package cli

import (
	"fmt"
	"strings"
)

// PrintHeader prints a section header
func PrintHeader(title string) {
	fmt.Printf("\n=== %s ===\n", title)
}

// PrintField prints a labeled field
func PrintField(label, value string) {
	fmt.Printf("  %-14s %s\n", label+":", value)
}

// PrintProviderInfo displays provider information
func PrintProviderInfo(p *ProviderInfo) {
	PrintHeader("Provider Info")
	PrintField("ID", p.ID)
	PrintField("Wallet", p.WalletAddress)
	PrintField("Type", p.ProviderType)
	PrintField("Status", p.Status)
	if p.ClusterHost != nil {
		PrintField("Cluster", *p.ClusterHost)
	}
}

// PrintNodesTable displays nodes in a table format
func PrintNodesTable(nodes []NodeInfo) {
	PrintHeader(fmt.Sprintf("Nodes (%d)", len(nodes)))

	if len(nodes) == 0 {
		fmt.Println("  (no nodes registered)")
		return
	}

	// Table header
	fmt.Printf("  %-38s %-20s %-10s %-15s\n", "ID", "GPU", "Status", "Price/hr")
	fmt.Printf("  %-38s %-20s %-10s %-15s\n",
		strings.Repeat("-", 36), strings.Repeat("-", 20),
		strings.Repeat("-", 10), strings.Repeat("-", 15))

	for _, n := range nodes {
		gpuType := n.GPUType
		if len(gpuType) > 20 {
			gpuType = gpuType[:17] + "..."
		}
		priceHr := n.PricePerHour
		if priceHr == "" {
			priceHr = n.PricePerSecond
		}

		fmt.Printf("  %-38s %-20s %-10s %-15s\n",
			n.ID, gpuType, n.Status, priceHr)
	}
}

// PrintMiningStatus displays mining status
func PrintMiningStatus(status *MiningStatus) {
	PrintHeader("Mining")

	if status.Mining != nil {
		// Parse mining map for readable output
		if m, ok := status.Mining.(map[string]interface{}); ok {
			active, _ := m["active"].(bool)
			if active {
				gpuCount := m["gpuCount"]
				podName := m["podName"]
				phase := m["phase"]
				fmt.Printf("  Status:    running\n")
				fmt.Printf("  GPU Count: %v\n", gpuCount)
				if podName != nil {
					fmt.Printf("  Pod:       %v\n", podName)
				}
				if phase != nil {
					fmt.Printf("  Phase:     %v\n", phase)
				}
			} else {
				fmt.Printf("  Status:    stopped\n")
			}
		} else {
			fmt.Printf("  Status:    %v\n", status.Mining)
		}
	} else {
		fmt.Println("  Status:    not running")
	}

	if status.Allocation != nil {
		a := status.Allocation
		fmt.Printf("  Pool:      Total=%d  Mining=%d  Rental=%d  Available=%d\n",
			a.TotalGPUs, a.MiningGPUs, a.RentalGPUs, a.AvailableGPUs)
	}
}

// PrintStep prints a step in a multi-step process
func PrintStep(current, total int, message string) {
	fmt.Printf("\n[%d/%d] %s\n", current, total, message)
}

// PrintSuccess prints a success message
func PrintSuccess(message string) {
	fmt.Printf("\n%s\n", message)
}

// PrintError prints an error message
func PrintError(message string) {
	fmt.Printf("\nError: %s\n", message)
}
