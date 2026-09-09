package worker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type dataLock struct {
	file *os.File
}

func ResolveWorkerID(config Config) (id string, returnErr error) {
	if err := ensureSupportedPlatform(); err != nil {
		return "", err
	}
	if err := validateConfig(config); err != nil {
		return "", err
	}
	dataDirectory, err := resolveDataDirectory(config.DataDirectory)
	if err != nil {
		return "", err
	}
	lock, err := lockDataDirectory(dataDirectory)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := lock.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("unlock data directory: %w", err)
		}
	}()
	return loadOrCreateWorkerID(dataDirectory, nil)
}

func lockDataDirectory(directory string) (*dataLock, error) {
	path := filepath.Join(directory, "worker.lock")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("worker.lock must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect worker lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open worker lock: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect worker lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return &dataLock{file: file}, nil
}

func (lock *dataLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockFile(lock.file)
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}

func loadOrCreateWorkerID(directory string, random io.Reader) (string, error) {
	path := filepath.Join(directory, "worker-id")
	if info, err := os.Lstat(path); err == nil {
		return readWorkerID(path, info)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect worker ID: %w", err)
	}
	id, err := newUUID(random)
	if err != nil {
		return "", fmt.Errorf("generate worker ID: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create worker ID: %w", err)
	}
	if _, err := io.WriteString(file, id+"\n"); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write worker ID: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("sync worker ID: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close worker ID: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", fmt.Errorf("sync worker ID directory: %w", err)
	}
	return id, nil
}

func loadWorkerID(directory string) (string, error) {
	path := filepath.Join(directory, "worker-id")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("worker-id does not exist")
		}
		return "", fmt.Errorf("inspect worker ID: %w", err)
	}
	return readWorkerID(path, info)
}

func readWorkerID(path string, info os.FileInfo) (string, error) {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("worker-id must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("worker-id permissions must not allow group or other access")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read worker ID: %w", err)
	}
	id := strings.TrimSpace(string(body))
	if !uuidPattern.MatchString(id) {
		return "", errors.New("worker-id does not contain a valid UUID")
	}
	return id, nil
}

func newUUID(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
