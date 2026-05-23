// Package validate contains pure-function security validators that need no
// I/O.  They are tested independently of the rest of the stack.
package validate

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// ErrCode wraps a machine-readable error code and a human message.
type ErrCode struct {
	Code    string
	Message string
}

func (e *ErrCode) Error() string { return e.Message }

// Filename checks that a client-supplied filename is a single safe path
// component.  It rejects absolute paths, paths with any separator, hidden
// files (leading dot), and names that are too long.
//
// Security hole 1: path traversal via filename.
func Filename(name string) error {
	if name == "" {
		return &ErrCode{"invalid_filename", "filename must not be empty"}
	}
	if len(name) > 128 {
		return &ErrCode{"invalid_filename", "filename exceeds 128-character limit"}
	}
	// filepath.Base returns "." for empty string and strips leading slashes;
	// we also check filepath.IsAbs and the presence of any separator.
	if filepath.IsAbs(name) {
		return &ErrCode{"invalid_filename", "source_filename must be a single path component"}
	}
	if strings.ContainsAny(name, `/\`) {
		return &ErrCode{"invalid_filename", "source_filename must be a single path component"}
	}
	if strings.HasPrefix(name, ".") {
		return &ErrCode{"invalid_filename", "source_filename must not start with a dot"}
	}
	// Disallow NUL bytes or control characters that could confuse shells.
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return &ErrCode{"invalid_filename", "source_filename contains invalid characters"}
		}
	}
	// A cleaned path must equal the original to catch things like "a//b".
	if filepath.Clean(name) != name {
		return &ErrCode{"invalid_filename", "source_filename is not a clean path component"}
	}
	return nil
}

// Flags filters extra flags submitted by the caller against the per-language
// allow-list.  Any flag not on the list causes an immediate 400.
//
// Security hole 3: compiler-flag injection.
//
// The allow-list supports a trailing glob "*" (e.g. "-std=*") which matches
// any suffix.  No other glob syntax is supported.
func Flags(requested []string, allowlist []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(requested))
	for _, f := range requested {
		if !flagAllowed(f, allowlist) {
			return nil, &ErrCode{
				"disallowed_flag",
				fmt.Sprintf("flag %q is not in the language allow-list", f),
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func flagAllowed(f string, allowlist []string) bool {
	for _, a := range allowlist {
		if strings.HasSuffix(a, "*") {
			prefix := strings.TrimSuffix(a, "*")
			if strings.HasPrefix(f, prefix) {
				return true
			}
		} else if f == a {
			return true
		}
	}
	return false
}
