package managedworker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// nextIncarnation returns a value greater than every incarnation previously
// created in this worker state directory. Retaining the high-water mark keeps
// process identity monotonic even if the system clock moves backwards.
func nextIncarnation(stateDir string) (int64, error) {
	path := filepath.Join(stateDir, "incarnation")
	var previous int64
	if value, err := os.ReadFile(path); err == nil {
		previous, err = strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
		if err != nil || previous <= 0 {
			return 0, fmt.Errorf("read worker incarnation: invalid value %q", strings.TrimSpace(string(value)))
		}
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("read worker incarnation: %w", err)
	}

	incarnation := time.Now().UnixNano()
	if incarnation <= previous {
		if previous == math.MaxInt64 {
			return 0, errors.New("worker incarnation exhausted")
		}
		incarnation = previous + 1
	}
	if incarnation <= 0 {
		incarnation = 1
	}

	temporary, err := os.CreateTemp(stateDir, ".incarnation-*")
	if err != nil {
		return 0, fmt.Errorf("create worker incarnation: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return 0, fmt.Errorf("secure worker incarnation: %w", err)
	}
	if _, err := fmt.Fprintf(temporary, "%d\n", incarnation); err != nil {
		temporary.Close()
		return 0, fmt.Errorf("write worker incarnation: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return 0, fmt.Errorf("sync worker incarnation: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return 0, fmt.Errorf("close worker incarnation: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return 0, fmt.Errorf("persist worker incarnation: %w", err)
	}
	return incarnation, nil
}
