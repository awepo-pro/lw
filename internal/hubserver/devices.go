package hubserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

type DeviceRecord struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Slug      string            `json:"slug"`
	State     string            `json:"state"`
	User      string            `json:"user"`
	FirstSeen string            `json:"first_seen"`
	LastSeen  string            `json:"last_seen"`
	LastAddr  string            `json:"last_addr"`
	Status    *hub.StatusReport `json:"status,omitempty"`
}

type Devices struct {
	path string
}

func OpenDevices(path string) (*Devices, error) {
	return &Devices{path: path}, nil
}

func (d *Devices) withLock(fn func() error) error {
	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	lockPath := d.path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()

	return fn()
}

func (d *Devices) readFile() ([]DeviceRecord, error) {
	data, err := os.ReadFile(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return []DeviceRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Devices []DeviceRecord `json:"devices"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Devices == nil {
		return []DeviceRecord{}, nil
	}
	return f.Devices, nil
}

func (d *Devices) writeFile(records []DeviceRecord) error {
	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	fData := struct {
		Devices []DeviceRecord `json:"devices"`
	}{
		Devices: records,
	}
	data, err := json.MarshalIndent(fData, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, filepath.Base(d.path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, d.path); err != nil {
		return err
	}
	tmpName = ""
	_ = os.Chmod(d.path, 0600)
	return nil
}

func (d *Devices) List() ([]DeviceRecord, error) {
	records, err := d.readFile()
	if err != nil {
		return nil, err
	}
	res := make([]DeviceRecord, len(records))
	copy(res, records)
	sort.Slice(res, func(i, j int) bool {
		return res[i].Slug < res[j].Slug
	})
	return res, nil
}

func (d *Devices) Approve(nameOrID string) (DeviceRecord, error) {
	var updated DeviceRecord
	err := d.withLock(func() error {
		records, err := d.readFile()
		if err != nil {
			return err
		}
		found := false
		for i := range records {
			if records[i].Slug == nameOrID || records[i].ID == nameOrID {
				records[i].State = hub.DeviceApproved
				updated = records[i]
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown device %q", nameOrID)
		}
		return d.writeFile(records)
	})
	if err != nil {
		return DeviceRecord{}, err
	}
	return updated, nil
}

func (d *Devices) Revoke(nameOrID string) (DeviceRecord, error) {
	var updated DeviceRecord
	err := d.withLock(func() error {
		records, err := d.readFile()
		if err != nil {
			return err
		}
		found := false
		for i := range records {
			if records[i].Slug == nameOrID || records[i].ID == nameOrID {
				records[i].State = hub.DeviceRevoked
				updated = records[i]
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown device %q", nameOrID)
		}
		return d.writeFile(records)
	})
	if err != nil {
		return DeviceRecord{}, err
	}
	return updated, nil
}

func (d *Devices) Get(id string) (DeviceRecord, bool, error) {
	records, err := d.readFile()
	if err != nil {
		return DeviceRecord{}, false, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, true, nil
		}
	}
	return DeviceRecord{}, false, nil
}

func (d *Devices) RegisterPending(id, name, user, remoteAddr string, now time.Time) (DeviceRecord, error) {
	var record DeviceRecord
	err := d.withLock(func() error {
		records, err := d.readFile()
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.ID == id {
				record = r
				return nil
			}
		}
		existingSlugs := make(map[string]bool, len(records))
		for _, r := range records {
			existingSlugs[r.Slug] = true
		}
		baseSlug := cleanSlug(name)
		slug := uniqueSlug(baseSlug, existingSlugs)
		nowStr := now.UTC().Format(time.RFC3339)
		record = DeviceRecord{
			ID:        id,
			Name:      name,
			Slug:      slug,
			State:     hub.DevicePending,
			User:      user,
			FirstSeen: nowStr,
			LastSeen:  nowStr,
			LastAddr:  remoteAddr,
		}
		records = append(records, record)
		return d.writeFile(records)
	})
	if err != nil {
		return DeviceRecord{}, err
	}
	return record, nil
}

func (d *Devices) TouchLastSeen(id, addr string, now time.Time) (bool, error) {
	written := false
	err := d.withLock(func() error {
		records, err := d.readFile()
		if err != nil {
			return err
		}
		idx := -1
		for i := range records {
			if records[i].ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("device not found: %s", id)
		}
		if records[idx].LastSeen != "" {
			t, err := time.Parse(time.RFC3339, records[idx].LastSeen)
			if err == nil && now.Sub(t) < time.Minute && now.Sub(t) >= 0 {
				return nil
			}
		}
		records[idx].LastSeen = now.UTC().Format(time.RFC3339)
		if addr != "" {
			records[idx].LastAddr = addr
		}
		written = true
		return d.writeFile(records)
	})
	return written, err
}

func (d *Devices) SetStatus(id string, status *hub.StatusReport) error {
	return d.withLock(func() error {
		records, err := d.readFile()
		if err != nil {
			return err
		}
		idx := -1
		for i := range records {
			if records[i].ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			return fmt.Errorf("device not found: %s", id)
		}
		records[idx].Status = status
		return d.writeFile(records)
	})
}

func cleanSlug(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 24 {
		s = s[:24]
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "device"
	}
	return s
}

func uniqueSlug(base string, existingSlugs map[string]bool) string {
	if !existingSlugs[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !existingSlugs[cand] {
			return cand
		}
	}
}
