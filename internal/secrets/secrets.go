// Package secrets resolves ${secret:NAME} references from the OS keyring.
//
// The config file never holds secret values; it holds references, and the values live in the keyring
// (macOS Keychain, Secret Service on Linux) under the service name "skilltri".
package secrets

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/zalando/go-keyring"
)

// Service is the keyring service name.
const Service = "skilltri"

// ErrNotFound means no secret is stored under the requested name.
var ErrNotFound = errors.New("secret not found")

var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var refPattern = regexp.MustCompile(`\$\{secret:([A-Za-z0-9._-]+)\}`)

// ValidName reports whether name can be used in a ${secret:NAME} reference.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Ref returns the config reference for a secret name: ${secret:NAME}.
func Ref(name string) string { return "${secret:" + name + "}" }

// Get returns the stored secret, or an error wrapping ErrNotFound.
func Get(name string) (string, error) {
	v, err := keyring.Get(Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("secret %q: %w", name, ErrNotFound)
	}
	return v, err
}

// Set stores a secret, replacing any existing value.
func Set(name, value string) error { return keyring.Set(Service, name, value) }

// Delete removes a secret. Deleting a missing secret is not an error.
func Delete(name string) error {
	err := keyring.Delete(Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Expand replaces every ${secret:NAME} in s with its keyring value.
// The error names the first missing secret.
func Expand(s string) (string, error) {
	var firstErr error
	out := refPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := refPattern.FindStringSubmatch(m)[1]
		v, err := Get(name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}
