package parse

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want Asset
	}{
		// Real headscale release assets.
		{"headscale_0.26.0_linux_amd64.deb", Asset{OS: "linux", Arch: "amd64", Format: "deb"}},
		{"headscale_0.26.0_linux_arm64.deb", Asset{OS: "linux", Arch: "arm64", Format: "deb"}},
		{"headscale_0.26.0_linux_386", Asset{OS: "linux", Arch: "386", Format: "bin"}},
		{"headscale_0.26.0_linux_amd64", Asset{OS: "linux", Arch: "amd64", Format: "bin"}},
		{"headscale_0.26.0_linux_arm64", Asset{OS: "linux", Arch: "arm64", Format: "bin"}},
		{"headscale_0.26.0_linux_armv5", Asset{OS: "linux", Arch: "armv5", Format: "bin"}},
		{"headscale_0.26.0_linux_armv6", Asset{OS: "linux", Arch: "armv6", Format: "bin"}},
		{"headscale_0.26.0_linux_armv7", Asset{OS: "linux", Arch: "armv7", Format: "bin"}},
		{"headscale_0.26.0_linux_armv7.deb", Asset{OS: "linux", Arch: "armv7", Format: "deb"}},
		{"headscale_0.26.0_darwin_amd64", Asset{OS: "darwin", Arch: "amd64", Format: "bin"}},
		{"headscale_0.26.0_darwin_arm64", Asset{OS: "darwin", Arch: "arm64", Format: "bin"}},
		{"headscale_0.26.0_freebsd_amd64", Asset{OS: "freebsd", Arch: "amd64", Format: "bin"}},

		// Non-platform assets: kept, but no os/arch.
		{"checksums.txt", Asset{Format: "txt"}},
		{"headscale_0.26.0.tar.gz", Asset{Format: "tar.gz"}},

		// Other conventions we should still recognise.
		{"tool_1.2.3_windows_amd64.zip", Asset{OS: "windows", Arch: "amd64", Format: "zip"}},
		{"tool-darwin-aarch64.tar.gz", Asset{OS: "darwin", Arch: "arm64", Format: "tar.gz"}},
		{"tool_linux_x86_64.rpm", Asset{OS: "linux", Arch: "amd64", Format: "rpm"}},

		// Unknown: everything empty, nothing crashes.
		{"totally-opaque-name", Asset{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Filename(tt.name)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Filename(%q) mismatch (-want +got):\n%s", tt.name, diff)
			}
		})
	}
}
