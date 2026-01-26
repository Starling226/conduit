/*
 * Copyright (c) 2026, Psiphon Inc.
 * All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Psiphon-Inc/conduit/cli/internal/conduit"
	"github.com/Psiphon-Inc/conduit/cli/internal/config"
	"github.com/spf13/cobra"
)

var (
	multiInstances        int
	multiMaxClients       int
	multiBandwidthMbps    float64
	multiPsiphonConfig    string
	multiStatsFilePattern string
)

var multiCmd = &cobra.Command{
	Use:   "run-multi",
	Short: "Run multiple Conduit instances in parallel",
	Long: `Run multiple Conduit inproxy instances in parallel for high-capacity VPS/server deployments.

Each instance gets its own data directory, cryptographic key, and reputation with the
Psiphon broker. This allows you to maximize throughput on servers with multiple CPU cores.

Examples:
  # Run 4 instances with default settings
  conduit run-multi --instances 4 --psiphon-config ./config.json

  # Run 8 instances with custom limits per instance
  conduit run-multi -n 8 -c ./config.json --max-clients 100 --bandwidth 20

  # Run with stats files for monitoring
  conduit run-multi -n 4 -c ./config.json --stats-file stats.json`,
	RunE: runMulti,
}

func init() {
	rootCmd.AddCommand(multiCmd)

	multiCmd.Flags().IntVarP(&multiInstances, "instances", "n", 2, "number of parallel instances (1-32)")
	multiCmd.Flags().IntVarP(&multiMaxClients, "max-clients", "m", config.DefaultMaxClients, "maximum clients per instance (1-1000)")
	multiCmd.Flags().Float64VarP(&multiBandwidthMbps, "bandwidth", "b", config.DefaultBandwidthMbps, "bandwidth limit per instance in Mbps (-1 for unlimited)")
	multiCmd.Flags().StringVarP(&multiStatsFilePattern, "stats-file", "s", "", "stats file pattern (e.g., stats.json creates instance-0-stats.json, etc.)")
	multiCmd.Flags().Lookup("stats-file").NoOptDefVal = "stats.json"

	// Only show --psiphon-config flag if no config is embedded
	if !config.HasEmbeddedConfig() {
		multiCmd.Flags().StringVarP(&multiPsiphonConfig, "psiphon-config", "c", "", "path to Psiphon network config file (JSON)")
	}
}

func runMulti(cmd *cobra.Command, args []string) error {
	// Validate instance count
	if multiInstances < 1 || multiInstances > 32 {
		return fmt.Errorf("instances must be between 1 and 32")
	}

	// Determine psiphon config source
	effectiveConfigPath := multiPsiphonConfig
	useEmbedded := false

	if multiPsiphonConfig != "" {
		if _, err := os.Stat(multiPsiphonConfig); os.IsNotExist(err) {
			return fmt.Errorf("psiphon config file not found: %s", multiPsiphonConfig)
		}
	} else if config.HasEmbeddedConfig() {
		useEmbedded = true
	} else {
		return fmt.Errorf("psiphon config required: use --psiphon-config flag or build with embedded config")
	}

	// Create base data directory
	baseDataDir := GetDataDir()
	if err := os.MkdirAll(baseDataDir, 0700); err != nil {
		return fmt.Errorf("failed to create base data directory: %w", err)
	}

	// Create instance configurations
	var instanceConfigs []*config.Config
	for i := 0; i < multiInstances; i++ {
		instanceDataDir := filepath.Join(baseDataDir, fmt.Sprintf("instance-%d", i))

		// Resolve stats file path for this instance
		var statsFile string
		if multiStatsFilePattern != "" {
			ext := filepath.Ext(multiStatsFilePattern)
			base := multiStatsFilePattern[:len(multiStatsFilePattern)-len(ext)]
			statsFile = filepath.Join(baseDataDir, fmt.Sprintf("%s-instance-%d%s", base, i, ext))
		}

		cfg, err := config.LoadOrCreate(config.Options{
			DataDir:           instanceDataDir,
			PsiphonConfigPath: effectiveConfigPath,
			UseEmbeddedConfig: useEmbedded,
			MaxClients:        multiMaxClients,
			BandwidthMbps:     multiBandwidthMbps,
			Verbosity:         Verbosity(),
			StatsFile:         statsFile,
		})
		if err != nil {
			return fmt.Errorf("failed to create config for instance %d: %w", i, err)
		}
		instanceConfigs = append(instanceConfigs, cfg)
	}

	// Create multi-instance service
	multiService, err := conduit.NewMultiService(instanceConfigs)
	if err != nil {
		return fmt.Errorf("failed to create multi-instance service: %w", err)
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down all instances...")
		cancel()
	}()

	// Print startup message
	bandwidthStr := "unlimited"
	if multiBandwidthMbps != config.UnlimitedBandwidth {
		bandwidthStr = fmt.Sprintf("%.0f Mbps", multiBandwidthMbps)
	}
	fmt.Printf("Starting %d Psiphon Conduit instances (Max Clients/instance: %d, Bandwidth/instance: %s)\n",
		multiInstances, multiMaxClients, bandwidthStr)

	// Run the multi-instance service
	if err := multiService.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("multi-instance service error: %w", err)
	}

	fmt.Println("All instances stopped.")
	return nil
}
