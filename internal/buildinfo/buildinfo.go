// Package buildinfo carries the version stamped at link time by
// scripts/build.sh (-X mailsense/internal/buildinfo.Version=…).
package buildinfo

// Version is the build version; "dev" for unstamped builds.
var Version = "dev"
