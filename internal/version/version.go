package version

// Version is overridden at build time with -ldflags "-X .../internal/version.Version=..."
var Version = "dev"

// String returns the one-line version banner.
func String() string {
	return "restore-drill " + Version
}
