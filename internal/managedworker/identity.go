package managedworker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateInstanceID returns the stable UUID for a worker state directory.
func LoadOrCreateInstanceID(stateDir string) (string, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("create worker state directory: %w", err)
	}
	path := filepath.Join(stateDir, "instance_id")
	if value, err := os.ReadFile(path); err == nil {
		return parseInstanceID(value)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read worker instance ID: %w", err)
	}

	id, err := newUUIDv4()
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(stateDir, ".instance_id-*")
	if err != nil {
		return "", fmt.Errorf("create worker instance ID: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("secure worker instance ID: %w", err)
	}
	if _, err := temporary.WriteString(id + "\n"); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write worker instance ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync worker instance ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close worker instance ID: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf("persist worker instance ID: %w", err)
		}
		value, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read concurrently created worker instance ID: %w", readErr)
		}
		return parseInstanceID(value)
	}
	return id, nil
}

func newUUIDv4() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate worker instance ID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func parseInstanceID(value []byte) (string, error) {
	id := strings.TrimSpace(string(value))
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' {
		return "", fmt.Errorf("worker instance ID %q is not a UUIDv4", id)
	}
	compact := strings.ReplaceAll(id, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 || decoded[8]&0xc0 != 0x80 {
		return "", fmt.Errorf("worker instance ID %q is not a UUIDv4", id)
	}
	return id, nil
}
