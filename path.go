package filesystem

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

var windowsReservedNames = map[string]struct{}{
	"CON":  {},
	"PRN":  {},
	"AUX":  {},
	"NUL":  {},
	"COM1": {},
	"COM2": {},
	"COM3": {},
	"COM4": {},
	"COM5": {},
	"COM6": {},
	"COM7": {},
	"COM8": {},
	"COM9": {},
	"LPT1": {},
	"LPT2": {},
	"LPT3": {},
	"LPT4": {},
	"LPT5": {},
	"LPT6": {},
	"LPT7": {},
	"LPT8": {},
	"LPT9": {},
}

func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: absolute paths are not allowed", ErrInvalidPath)
	}
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("%w: backslash is not allowed", ErrInvalidPath)
	}
	for _, r := range p {
		if r == 0 || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidPath)
		}
	}
	segments := strings.Split(p, "/")
	for _, segment := range segments {
		if segment == "" {
			return "", fmt.Errorf("%w: empty path segment", ErrInvalidPath)
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: dot segments are not allowed", ErrInvalidPath)
		}
		if strings.Contains(segment, ":") {
			return "", fmt.Errorf("%w: colon is not allowed in path segments", ErrInvalidPath)
		}
		if isWindowsReservedName(segment) {
			return "", fmt.Errorf("%w: reserved Windows name %q", ErrInvalidPath, segment)
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("%w: path escapes root", ErrInvalidPath)
	}
	return cleaned, nil
}

func normalizeNonEmptyPath(p string) (string, error) {
	normalized, err := NormalizePath(p)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", fmt.Errorf("%w: path must not be empty", ErrInvalidPath)
	}
	return normalized, nil
}

func isWindowsReservedName(segment string) bool {
	name := segment
	if idx := strings.IndexByte(name, '.'); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimRight(name, " ")
	name = strings.TrimRight(name, ".")
	if name == "" {
		return false
	}
	_, ok := windowsReservedNames[strings.ToUpper(name)]
	return ok
}
