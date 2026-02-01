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

// Package filter provides IP filtering based on country
package filter

import (
	"net"
	"sync"

	"github.com/oschwald/geoip2-golang"
)

// CountryFilter filters connections based on country
type CountryFilter struct {
	db               *geoip2.Reader
	allowedCountries map[string]bool
	mu               sync.RWMutex

	// Stats
	allowedCount int64
	blockedCount int64
	relayCount   int64
}

// NewCountryFilter creates a new country filter
func NewCountryFilter(dbPath string, allowedCountries []string) (*CountryFilter, error) {
	db, err := geoip2.Open(dbPath)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool)
	for _, cc := range allowedCountries {
		allowed[cc] = true
	}

	return &CountryFilter{
		db:               db,
		allowedCountries: allowed,
	}, nil
}

// IsAllowed checks if an IP is allowed based on country
// Returns: allowed (bool), countryCode (string), isRelay (bool for private IPs)
func (f *CountryFilter) IsAllowed(ipStr string) (bool, string, bool) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		// Invalid IP, block it
		f.mu.Lock()
		f.blockedCount++
		f.mu.Unlock()
		return false, "", false
	}

	// Allow private/loopback IPs (TURN relay connections)
	if isPrivateIP(ip) {
		f.mu.Lock()
		f.relayCount++
		f.mu.Unlock()
		return true, "RELAY", true
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	record, err := f.db.Country(ip)
	if err != nil || record.Country.IsoCode == "" {
		// Can't determine country, block it
		f.blockedCount++
		return false, "UNKNOWN", false
	}

	countryCode := record.Country.IsoCode
	if f.allowedCountries[countryCode] {
		f.allowedCount++
		return true, countryCode, false
	}

	f.blockedCount++
	return false, countryCode, false
}

// GetStats returns the current filter statistics
func (f *CountryFilter) GetStats() (allowed, blocked, relay int64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.allowedCount, f.blockedCount, f.relayCount
}

// Close closes the GeoIP database
func (f *CountryFilter) Close() error {
	if f.db != nil {
		return f.db.Close()
	}
	return nil
}

// isPrivateIP checks if an IP is private/internal
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
