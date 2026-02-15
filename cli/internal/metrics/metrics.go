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

// Package metrics provides Prometheus metrics for the Conduit service
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Psiphon-Inc/conduit/cli/internal/geo"
	"github.com/Psiphon-Inc/conduit/cli/internal/logging"
	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/buildinfo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "conduit"

// Metrics holds all Prometheus metrics for the Conduit service
type Metrics struct {
	// Gauges
	Announcing        prometheus.Gauge
	ConnectingClients prometheus.Gauge
	ConnectedClients  prometheus.Gauge
	IsLive            prometheus.Gauge
	MaxClients        prometheus.Gauge
	BandwidthLimit    prometheus.Gauge
	BytesUploaded     prometheus.Gauge
	BytesDownloaded   prometheus.Gauge

	// Geo metrics (by country)
	geoConnectedClients   *prometheus.GaugeVec
	geoTotalClients       *prometheus.CounterVec
	geoBytesUploadedVec   *prometheus.CounterVec
	geoBytesDownloadedVec *prometheus.CounterVec

	// Info
	BuildInfo *prometheus.GaugeVec

	registry *prometheus.Registry
	server   *http.Server

	// State for counter delta tracking
	geoMu       sync.Mutex
	geoPrevious map[string]geo.Result // key: country_code
}

// GaugeFuncs holds functions that compute metrics at scrape time
type GaugeFuncs struct {
	GetUptimeSeconds func() float64
	GetIdleSeconds   func() float64
}

// New creates a new Metrics instance with all metrics registered
func New(gaugeFuncs GaugeFuncs) *Metrics {
	registry := prometheus.NewRegistry()

	// Add standard Go metrics
	registerCollector(collectors.NewGoCollector(), registry)
	registerCollector(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		registry,
	)

	m := &Metrics{
		Announcing: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "announcing",
				Help:      "Number of inproxy announcement requests in flight",
			},
			registry,
		),
		ConnectingClients: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "connecting_clients",
				Help:      "Number of clients currently connecting to the proxy",
			},
			registry,
		),
		ConnectedClients: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "connected_clients",
				Help:      "Number of clients currently connected to the proxy",
			},
			registry,
		),
		IsLive: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "is_live",
				Help:      "Whether the service is connected to the Psiphon broker (1 = connected, 0 = disconnected)",
			},
			registry,
		),
		MaxClients: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "max_clients",
				Help:      "Maximum number of proxy clients allowed",
			},
			registry,
		),
		BandwidthLimit: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "bandwidth_limit_bytes_per_second",
				Help:      "Configured bandwidth limit in bytes per second (0 = unlimited)",
			},
			registry,
		),
		BytesUploaded: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "bytes_uploaded",
				Help:      "Total number of bytes uploaded through the proxy",
			},
			registry,
		),
		BytesDownloaded: newGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "bytes_downloaded",
				Help:      "Total number of bytes downloaded through the proxy",
			},
			registry,
		),
		geoConnectedClients: newGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "geo_connected_clients",
				Help:      "Number of currently connected clients by country",
			},
			[]string{"country_code"},
			registry,
		),
		geoTotalClients: newCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "geo_clients_total",
				Help:      "Total unique clients by country since start",
			},
			[]string{"country_code"},
			registry,
		),
		geoBytesUploadedVec: newCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "geo_bytes_uploaded_total",
				Help:      "Total bytes uploaded by country",
			},
			[]string{"country_code"},
			registry,
		),
		geoBytesDownloadedVec: newCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "geo_bytes_downloaded_total",
				Help:      "Total bytes downloaded by country",
			},
			[]string{"country_code"},
			registry,
		),
		BuildInfo: newGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "build_info",
				Help:      "Build information about the Conduit service",
			},
			[]string{"build_repo", "build_rev", "go_version", "values_rev"},
			registry,
		),

		// Internal state
		geoPrevious: make(map[string]geo.Result),
		registry:    registry,
	}

	// Create GaugeFunc metrics (computed at scrape time)
	newGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "uptime_seconds",
			Help:      "Number of seconds since the service started",
		},
		gaugeFuncs.GetUptimeSeconds,
		registry,
	)
	newGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "idle_seconds",
			Help:      "Number of seconds the proxy has been idle (0 connecting and 0 connected clients)",
		},
		gaugeFuncs.GetIdleSeconds,
		registry,
	)

	// Set build info
	buildInfo := buildinfo.GetBuildInfo()
	m.BuildInfo.
		WithLabelValues(
			buildInfo.BuildRepo,
			buildInfo.BuildRev,
			buildInfo.GoVersion,
			buildInfo.ValuesRev).
		Set(1)

	return m
}

// SetConfig sets the configuration-related metrics
func (m *Metrics) SetConfig(maxClients int, bandwidthBytesPerSecond int) {
	m.MaxClients.Set(float64(maxClients))
	m.BandwidthLimit.Set(float64(bandwidthBytesPerSecond))
}

// SetAnnouncing updates the announcing gauge
func (m *Metrics) SetAnnouncing(count int) {
	m.Announcing.Set(float64(count))
}

// SetConnectingClients updates the connecting clients gauge
func (m *Metrics) SetConnectingClients(count int) {
	m.ConnectingClients.Set(float64(count))
}

// SetConnectedClients updates the connected clients gauge
func (m *Metrics) SetConnectedClients(count int) {
	m.ConnectedClients.Set(float64(count))
}

// SetIsLive updates the live status gauge
func (m *Metrics) SetIsLive(isLive bool) {
	if isLive {
		m.IsLive.Set(1)
	} else {
		m.IsLive.Set(0)
	}
}

// SetBytesUploaded sets the bytes uploaded gauge
func (m *Metrics) SetBytesUploaded(bytes float64) {
	m.BytesUploaded.Set(bytes)
}

// SetBytesDownloaded sets the bytes downloaded gauge
func (m *Metrics) SetBytesDownloaded(bytes float64) {
	m.BytesDownloaded.Set(bytes)
}

// UpdateGeo updates geo-based metrics from the latest geo collector results.
// It computes deltas against previously seen values to correctly increment
// Prometheus counters, and resets the connected clients gauge each cycle
// so that countries with no active connections are removed.
func (m *Metrics) UpdateGeo(results []geo.Result) {
	m.geoMu.Lock()
	defer m.geoMu.Unlock()
	m.geoConnectedClients.Reset()

	for _, r := range results {
		m.geoConnectedClients.WithLabelValues(r.Code).Set(float64(r.Count))
		prev := m.geoPrevious[r.Code]

		if delta := r.CountTotal - prev.CountTotal; delta > 0 {
			m.geoTotalClients.WithLabelValues(r.Code).Add(float64(delta))
		}
		if delta := r.BytesUp - prev.BytesUp; delta > 0 {
			m.geoBytesUploadedVec.WithLabelValues(r.Code).Add(float64(delta))
		}
		if delta := r.BytesDown - prev.BytesDown; delta > 0 {
			m.geoBytesDownloadedVec.WithLabelValues(r.Code).Add(float64(delta))
		}
		m.geoPrevious[r.Code] = r
	}
}

// StartServer starts the HTTP server for Prometheus metrics
func (m *Metrics) StartServer(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	m.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  10 * time.Second,
		TLSConfig:    nil,
	}

	// Create a listener to verify the port is available before starting the server
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind to %s: %w", addr, err)
	}

	// Start server in background with the pre-created listener
	go func() {
		if err := m.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Printf("[ERROR] Metrics server error: %v\n", err)
		}
	}()

	return nil
}

// Shutdown gracefully shuts down the metrics server
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m.server != nil {
		return m.server.Shutdown(ctx)
	}

	return nil
}
