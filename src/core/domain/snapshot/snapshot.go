// Package snapshot defines the VM snapshot domain entity.
package snapshot

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Type represents the snapshot type.
type Type string

const (
	TypeFull        Type = "full"
	TypeIncremental Type = "incremental"
)

// SnapshotMetadata captures the VM specification at snapshot time.
// This is required to reconstruct the Firecracker machine on restore.
type SnapshotMetadata struct {
	EnvironmentID string `json:"environment_id"`
	ImagePath     string `json:"image_path"`
	VCPU          int    `json:"vcpu"`
	MemoryMB      int    `json:"memory_mb"`
	DiskMB        int    `json:"disk_mb"`
	GuestCID      uint32 `json:"guest_cid"`
	GuestPort     uint32 `json:"guest_port"`
	RootfsPath    string `json:"rootfs_path"`

	// Incremental snapshot fields (dm-snapshot CoW device)
	DiffSnapshots  bool   `json:"diff_snapshots"`
	BaseImagePath  string `json:"base_image_path"`  // Host path to environment base image
	BaseSnapshotID string `json:"base_snapshot_id"` // Full snapshot this diff is relative to
	CowDeviceName  string `json:"cow_device_name"`  // /dev/mapper/vm_{id}
}

// Snapshot represents a VM snapshot for backup/restore.
// SnapshotPath stores the directory containing "memory" and "state" files.
type Snapshot struct {
	ID               string          `db:"id"`
	VMID             string          `db:"vm_id"`
	ParentSnapshotID *string         `db:"parent_snapshot_id"` // For incremental snapshots
	Type             Type            `db:"type"`
	SnapshotPath     string          `db:"snapshot_path"` // Directory containing memory + state files
	SizeBytes        int64           `db:"size_bytes"`
	Metadata         json.RawMessage `db:"metadata"`
	CreatedAt        time.Time       `db:"created_at"`
}

// CreateParams contains the parameters for creating a new snapshot.
type CreateParams struct {
	VMID             string
	ParentSnapshotID *string
	Type             Type
	SnapshotPath     string
	SizeBytes        int64
	Metadata         json.RawMessage
}

// New creates a new Snapshot with a generated ID and timestamp.
func New(params CreateParams) *Snapshot {
	return &Snapshot{
		ID:               uuid.NewString(),
		VMID:             params.VMID,
		ParentSnapshotID: params.ParentSnapshotID,
		Type:             params.Type,
		SnapshotPath:     params.SnapshotPath,
		SizeBytes:        params.SizeBytes,
		Metadata:         params.Metadata,
		CreatedAt:        time.Now().UTC(),
	}
}

// IsFull returns true if this is a full snapshot.
func (s *Snapshot) IsFull() bool {
	return s.Type == TypeFull
}

// IsIncremental returns true if this is an incremental snapshot.
func (s *Snapshot) IsIncremental() bool {
	return s.Type == TypeIncremental
}

// HasParent returns true if this snapshot has a parent (is incremental).
func (s *Snapshot) HasParent() bool {
	return s.ParentSnapshotID != nil
}

// MemFilePath returns the key/path to the memory file within the snapshot prefix.
func (s *Snapshot) MemFilePath() string {
	return s.SnapshotPath + "/memory"
}

// StateFilePath returns the key/path to the state file within the snapshot prefix.
func (s *Snapshot) StateFilePath() string {
	return s.SnapshotPath + "/state"
}

// CowFilePath returns the key/path to the CoW image within the snapshot prefix.
func (s *Snapshot) CowFilePath() string {
	return s.SnapshotPath + "/cow"
}

// DiskFilePath returns the key/path to the full disk image within the snapshot prefix.
// Used by the new self-contained snapshot format where the entire disk state is
// captured (not just the CoW delta), making each snapshot independently restorable.
func (s *Snapshot) DiskFilePath() string {
	return s.SnapshotPath + "/disk"
}

// ManifestFilePath returns the key/path to the memory manifest within the snapshot prefix.
func (s *Snapshot) ManifestFilePath() string {
	return s.SnapshotPath + "/memory.manifest"
}

// ParseMetadata decodes the JSON metadata into SnapshotMetadata.
func (s *Snapshot) ParseMetadata() (*SnapshotMetadata, error) {
	if len(s.Metadata) == 0 {
		return nil, nil
	}
	var meta SnapshotMetadata
	if err := json.Unmarshal(s.Metadata, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
