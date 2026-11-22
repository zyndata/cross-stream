package server

import (
	"strconv"
	"strings"
)

// Version is a parsed vX.Y.Z software version.
type Version [3]int

// ParseVersion reads the vX.Y.Z prefix of a version string such as
// "v1.2.3" or "v1.2.3-4-gabcdef-dirty" (git describe). Anything else, like
// "dev" or a bare commit hash, is not a version.
func ParseVersion(s string) (Version, bool) {
	s, ok := strings.CutPrefix(s, "v")
	if !ok {
		return Version{}, false
	}
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		v[i] = n
	}
	return v, true
}

// Less reports whether v is older than w.
func (v Version) Less(w Version) bool {
	for i := range v {
		if v[i] != w[i] {
			return v[i] < w[i]
		}
	}
	return false
}

// hubVersionOK applies the min_hub_version gate to the "sw" field of an INIT:
// with a gate set, a hub must send a version at least as new.
func hubVersionOK(minVersion, sw string) bool {
	if minVersion == "" {
		return true
	}
	lo, _ := ParseVersion(minVersion)
	v, ok := ParseVersion(sw)
	return ok && !v.Less(lo)
}
