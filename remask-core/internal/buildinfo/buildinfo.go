// Package buildinfo contains values injected by the release build.
//
// Keeping the values in a tiny package lets the private Core build pipeline
// stamp a binary without making the desktop project depend on Core source.
package buildinfo

// Version is the Core semantic version. Development builds deliberately use
// an explicit dev value instead of pretending to be a release.
var Version = "0.1.0-dev"

// APIVersion is the versioned HTTP contract consumed by the desktop client.
var APIVersion = "v1"

// BuildID identifies the immutable CI build which produced the binary. It is
// informational and must never be used as an authorization credential.
var BuildID = "local"

// BuildTime is an RFC3339 timestamp supplied by the release pipeline.
var BuildTime = ""
