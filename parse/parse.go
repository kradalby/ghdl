// Package parse derives platform dimensions (os, arch, packaging format) from a
// release asset filename. It is deliberately lenient: an unrecognised name
// yields empty fields rather than an error, and callers always keep the raw
// filename so nothing is lost when a name doesn't parse.
package parse

import "strings"

// Asset is the platform breakdown parsed from a release asset filename. Empty
// fields mean "not identifiable from the name".
type Asset struct {
	OS     string
	Arch   string
	Format string
}

// osTokens maps a filename token to a canonical OS name.
var osTokens = map[string]string{
	"linux":   "linux",
	"darwin":  "darwin",
	"macos":   "darwin",
	"freebsd": "freebsd",
	"windows": "windows",
	"win":     "windows",
}

// archTokens maps a filename token to a canonical arch name. The arm variants
// are kept distinct (headscale ships armv5/6/7) and are preferred over a bare
// "arm" when both appear.
var archTokens = map[string]string{
	"amd64":   "amd64",
	"x86_64":  "amd64",
	"arm64":   "arm64",
	"aarch64": "arm64",
	"386":     "386",
	"i386":    "386",
	"armv5":   "armv5",
	"armv6":   "armv6",
	"armv7":   "armv7",
	"arm":     "arm",
}

// Filename parses os/arch/format out of a release asset filename such as
// "headscale_0.26.0_linux_arm64.deb". A raw platform binary with no extension
// (e.g. "headscale_0.26.0_linux_amd64") is reported with format "bin".
func Filename(name string) Asset {
	// Collapse "x86_64" first: the underscore would otherwise split it into
	// "x86" and "64", so the compound token could never match.
	lower := strings.ReplaceAll(strings.ToLower(name), "x86_64", "amd64")

	ext := knownExt(lower)
	base := lower
	if ext != "" {
		base = strings.TrimSuffix(lower, "."+ext)
	}

	a := Asset{Format: ext}
	for _, t := range strings.FieldsFunc(base, isSep) {
		if a.OS == "" {
			if v, ok := osTokens[t]; ok {
				a.OS = v
				continue
			}
		}
		// Last match wins. First-match-wins loses the real arch whenever an
		// earlier token happens to name one too — "i386-tools_1.0_linux_amd64"
		// would file amd64 downloads under 386, and UpsertSeries never rewrites
		// os/arch/format, so the mislabel would outlive the fix. The armvN
		// guard is kept: a bare "arm" must not overwrite a more specific
		// variant that already matched.
		if v, ok := archTokens[t]; ok && (v != "arm" || !strings.HasPrefix(a.Arch, "armv")) {
			a.Arch = v
		}
	}

	// No recognised extension but it names a platform → a raw binary.
	if a.Format == "" && a.OS != "" && a.Arch != "" {
		a.Format = "bin"
	}

	return a
}

// knownExt returns the packaging format for a recognised extension, or "".
// Order matters: ".tar.gz" must be checked before a bare extension.
func knownExt(lower string) string {
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".deb"):
		return "deb"
	case strings.HasSuffix(lower, ".rpm"):
		return "rpm"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".txt"):
		return "txt"
	default:
		return ""
	}
}

func isSep(r rune) bool {
	return r == '_' || r == '-' || r == '.'
}
