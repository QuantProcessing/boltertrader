package verifiedartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

type Artifact struct {
	path    string
	file    *os.File
	info    os.FileInfo
	maxSize int64
	sha256  string
}

func ReadSnapshot(path string, maxSize int64) ([]byte, string, error) {
	return readSnapshot(path, maxSize, nil)
}

func OpenStream(path string, maxSize int64) (*Artifact, error) {
	return openStream(path, maxSize, nil)
}

func CreateStream(path string, maxSize int64) (*Artifact, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("verified artifact size limit is invalid")
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("verified artifact create rejected")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("verified artifact create rejected")
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 0 || validateMetadata(info) != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("verified artifact create rejected")
	}
	return &Artifact{path: path, file: file, info: info, maxSize: maxSize}, nil
}

func readSnapshot(path string, maxSize int64, beforeOpen func()) ([]byte, string, error) {
	artifact, err := openStream(path, maxSize, beforeOpen)
	if err != nil {
		return nil, "", err
	}
	defer artifact.Close()
	data, err := io.ReadAll(io.LimitReader(artifact.file, maxSize+1))
	if err != nil || int64(len(data)) != artifact.info.Size() || int64(len(data)) > maxSize {
		clear(data)
		return nil, "", fmt.Errorf("verified artifact changed during read")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.sha256 {
		clear(data)
		return nil, "", fmt.Errorf("verified artifact changed during read")
	}
	if err := artifact.Revalidate(); err != nil {
		clear(data)
		return nil, "", err
	}
	return data, artifact.sha256, nil
}

func openStream(path string, maxSize int64, beforeOpen func()) (*Artifact, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("verified artifact size limit is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("verified artifact is unavailable")
	}
	if err := validateInfo(pathInfo, maxSize); err != nil {
		return nil, err
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("verified artifact open rejected")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("verified artifact open rejected")
	}
	closeOnError := func(err error) (*Artifact, error) {
		_ = file.Close()
		return nil, err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("verified artifact metadata is unavailable"))
	}
	if err := validateInfo(fileInfo, maxSize); err != nil {
		return closeOnError(err)
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return closeOnError(fmt.Errorf("verified artifact changed before open"))
	}
	digest, err := hashFile(file, fileInfo.Size(), maxSize)
	if err != nil {
		return closeOnError(err)
	}
	artifact := &Artifact{path: path, file: file, info: fileInfo, maxSize: maxSize, sha256: digest}
	if err := artifact.Rewind(); err != nil {
		return closeOnError(err)
	}
	return artifact, nil
}

func validateInfo(info os.FileInfo, maxSize int64) error {
	if err := validateMetadata(info); err != nil || info.Size() <= 0 || info.Size() > maxSize {
		return fmt.Errorf("verified artifact must be a private regular file")
	}
	return nil
}

func validateMetadata(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("verified artifact must be a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("verified artifact owner mismatch")
	}
	return nil
}

func hashFile(file *os.File, expectedSize, maxSize int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("verified artifact rewind failed")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxSize+1))
	if err != nil || written != expectedSize || written > maxSize {
		return "", fmt.Errorf("verified artifact changed during hash")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (artifact *Artifact) File() *os.File {
	return artifact.file
}

func (artifact *Artifact) SHA256() string {
	return artifact.sha256
}

func (artifact *Artifact) Rewind() error {
	if _, err := artifact.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("verified artifact rewind failed")
	}
	return nil
}

func (artifact *Artifact) Revalidate() error {
	if artifact.sha256 == "" {
		return fmt.Errorf("verified artifact is not sealed")
	}
	fileInfo, err := artifact.file.Stat()
	if err != nil || validateInfo(fileInfo, artifact.maxSize) != nil || !os.SameFile(artifact.info, fileInfo) {
		return fmt.Errorf("verified artifact changed after open")
	}
	pathInfo, err := os.Lstat(artifact.path)
	if err != nil || validateInfo(pathInfo, artifact.maxSize) != nil || !os.SameFile(pathInfo, fileInfo) {
		return fmt.Errorf("verified artifact changed after open")
	}
	digest, err := hashFile(artifact.file, fileInfo.Size(), artifact.maxSize)
	if err != nil || digest != artifact.sha256 {
		return fmt.Errorf("verified artifact changed after open")
	}
	return artifact.Rewind()
}

func (artifact *Artifact) Seal() error {
	fileInfo, err := artifact.file.Stat()
	if err != nil || validateInfo(fileInfo, artifact.maxSize) != nil || !os.SameFile(artifact.info, fileInfo) {
		return fmt.Errorf("verified artifact changed before seal")
	}
	pathInfo, err := os.Lstat(artifact.path)
	if err != nil || validateInfo(pathInfo, artifact.maxSize) != nil || !os.SameFile(pathInfo, fileInfo) {
		return fmt.Errorf("verified artifact changed before seal")
	}
	digest, err := hashFile(artifact.file, fileInfo.Size(), artifact.maxSize)
	if err != nil {
		return err
	}
	artifact.info = fileInfo
	artifact.sha256 = digest
	return artifact.Rewind()
}

func (artifact *Artifact) Close() error {
	return artifact.file.Close()
}
