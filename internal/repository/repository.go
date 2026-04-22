// Package repository defines cache keys and repository constants for the mailservice.
//
// This package provides centralized key definitions for the local cache system
// used throughout the service.

package repository

import (
	"github.com/swayrider/swlib/cache"
)

// Cache key constants for the local cache.
const (
	// JwtPublicKeys is the cache key for storing JWT public keys fetched from authservice.
	JwtPublicKeys cache.LocalCacheKey = "jwt_public_keys"
)
