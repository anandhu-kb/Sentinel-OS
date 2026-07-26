// Package db provides shared database utilities for Sentinel-OS.
// All engines receive a *sql.DB reference from app.go — this package
// holds helper functions for common query patterns.
//
// Utilities added incrementally as each engine is implemented.
package db
