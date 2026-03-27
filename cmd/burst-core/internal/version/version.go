// Package version holds the binary version, injected at build time by goreleaser.
package version

// Version is the current burst-core binary version.
// It is set via -ldflags at build time:
//
//	go build -ldflags="-X github.com/scttfrdmn/burst-core/cmd/burst-core/internal/version.Version=v1.2.3"
var Version = "dev"
