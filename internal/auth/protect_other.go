//go:build !windows

package auth

// protect is a no-op outside Windows: the file is mode 0600 in the user's private state
// directory, as other CLIs do when no keyring is involved.
func protect(data []byte) ([]byte, error) { return data, nil }

func unprotect(data []byte) ([]byte, error) { return data, nil }
