package ledger

import (
	"fmt"
	"os"
	"sync"
)

// MemStore is an append-only in-memory Store (tests, and the hot cache in front
// of the durable store). It enforces contiguous seqs so a buggy caller can't
// create a gap or overwrite.
type MemStore struct {
	mu      sync.Mutex
	records []Record
}

func NewMemStore() *MemStore { return &MemStore{} }

func (m *MemStore) Append(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Seq != uint64(len(m.records)) {
		return fmt.Errorf("ledger: append seq %d, expected %d (append-only, no gaps)", r.Seq, len(m.records))
	}
	m.records = append(m.records, r)
	return nil
}

func (m *MemStore) List() ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, len(m.records))
	copy(out, m.records)
	return out, nil
}

// FileStore persists the chain as a single JSON array file, rewritten on each
// append. It is the simple durable backend for local/dev; production uses an
// object-locked R2 bucket (one immutable object per record) behind this same
// interface. Appends are serialized by a process-level mutex.
type FileStore struct {
	mu   sync.Mutex
	path string
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (f *FileStore) load() ([]Record, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	return UnmarshalRecords(b)
}

func (f *FileStore) Append(r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	records, err := f.load()
	if err != nil {
		return err
	}
	if r.Seq != uint64(len(records)) {
		return fmt.Errorf("ledger: append seq %d, expected %d (append-only, no gaps)", r.Seq, len(records))
	}
	records = append(records, r)
	b, err := MarshalRecords(records)
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *FileStore) List() ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load()
}
