package monitor

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const maxStateBytes = 64 << 20

// One process owns a state path. An OS lock survives atomic checkpoint renames
// and is released on process death; the lock file itself must not be removed.
type stateFile struct {
	path string
	lock *os.File
}

func openState(path string, config Config, now time.Time) (*stateFile, *engine, error) {
	e, err := newEngine(config, now)
	if err != nil {
		return nil, nil, err
	}
	if path == "" {
		return nil, nil, errors.New("monitor state path is required")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, errors.New("open monitor state lock failed; parent directory must exist")
	}
	f := &stateFile{path: path, lock: lock}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, errors.New("monitor state is already locked")
	}
	if err := f.load(e); err != nil {
		f.close()
		return nil, nil, err
	}
	return f, e, nil
}

func (f *stateFile) close() { _ = f.lock.Close() }

func (f *stateFile) load(e *engine) error {
	file, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("read monitor state failed")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return errors.New("invalid monitor state file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxStateBytes+1))
	decoder.DisallowUnknownFields()
	var saved checkpoint
	var trailing any
	if decoder.Decode(&saved) != nil || decoder.Decode(&trailing) != io.EOF || !validCheckpoint(e, saved) {
		return errors.New("invalid monitor state or changed rule; use a different state path for a new rule")
	}
	e.checkpoint = saved
	return nil
}

func validCheckpoint(e *engine, c checkpoint) bool {
	instance, err := hex.DecodeString(c.InstanceID)
	if err != nil || len(instance) != 16 || c.Version != 1 || c.ConfigID != e.ConfigID || c.SavedAt.IsZero() || c.SavedAt.After(e.now) || len(c.Instruments) != len(e.config.Instruments) || len(c.Pending) > 128 {
		return false
	}
	for _, id := range e.config.Instruments {
		s := c.Instruments[id]
		if s == nil || s.StartedAt.IsZero() || s.StartedAt.After(c.SavedAt) || s.LastObserved.After(c.SavedAt) || s.LastAlert.After(c.SavedAt) || len(s.Samples) > int(e.config.Window/time.Second)+1 {
			return false
		}
		switch s.State {
		case Insufficient, Normal, Triggered:
		case Interrupted:
			if len(s.Samples) != 0 {
				return false
			}
		default:
			return false
		}
		var previous time.Time
		for _, point := range s.Samples {
			// Validate exponent before rendering a decimal loaded from disk.
			if point.Price.Exponent() < -101 || point.Price.Exponent() > 100 || !point.Price.IsPositive() || point.Price.NumDigits() > 512 || point.At.IsZero() || point.At.After(s.LastObserved) || (!previous.IsZero() && point.At.Unix() <= previous.Unix()) {
				return false
			}
			previous = point.At
		}
	}
	for _, event := range c.Pending {
		if c.Instruments[event.InstrumentID] == nil || event.ID == "" || event.At.IsZero() || event.At.After(c.SavedAt) || event.PriceBasis != PriceBasis {
			return false
		}
	}
	return true
}

func (f *stateFile) save(e *engine) error {
	e.SavedAt = e.now
	data, err := json.Marshal(e.checkpoint)
	if err != nil || len(data) > maxStateBytes {
		return errors.New("encode monitor state failed")
	}
	dir := filepath.Dir(f.path)
	temp, err := os.CreateTemp(dir, ".monitor-*.tmp")
	if err != nil {
		return errors.New("create monitor checkpoint failed")
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if err == nil {
		err = temp.Close()
	}
	if err == nil {
		err = os.Rename(temp.Name(), f.path)
	}
	if err != nil {
		return errors.New("persist monitor checkpoint failed")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return errors.New("open monitor checkpoint directory failed")
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return errors.New("sync monitor checkpoint directory failed")
	}
	return nil
}

// flush durably records a bounded pending batch BEFORE writing JSON lines. A
// crash after output but before acknowledgement can repeat the same IDs. This
// is local replay, not an exactly-once external notification promise.
func (f *stateFile) flush(e *engine, output io.Writer) error {
	if err := f.save(e); err != nil {
		return err
	}
	if len(e.Pending) == 0 {
		return nil
	}
	for _, event := range e.Pending {
		if err := json.NewEncoder(output).Encode(event); err != nil {
			return errors.New("write monitor event failed; pending IDs retained for restart")
		}
	}
	e.Pending = nil
	return f.save(e)
}
