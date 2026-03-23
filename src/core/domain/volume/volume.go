// Package volume defines the VM volume domain entity.
package volume

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Type represents the volume source type.
type Type string

const (
	TypeLocalDir  Type = "local_dir"
	TypeGitHubRepo Type = "github_repo"
	TypeS3        Type = "s3"
)

// Volume represents a mounted volume for a VM.
type Volume struct {
	ID          string          `db:"id"`
	VMID        string          `db:"vm_id"`
	Type        Type            `db:"type"`
	Source      string          `db:"source"`
	MountPath   string          `db:"mount_path"`
	IsReadOnly  bool            `db:"is_readonly"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
}

// CreateParams contains the parameters for creating a new volume.
type CreateParams struct {
	VMID       string
	Type       Type
	Source     string
	MountPath  string
	IsReadOnly bool
	Metadata   json.RawMessage
}

// New creates a new Volume with a generated ID and timestamp.
func New(params CreateParams) *Volume {
	return &Volume{
		ID:         uuid.NewString(),
		VMID:       params.VMID,
		Type:       params.Type,
		Source:     params.Source,
		MountPath:  params.MountPath,
		IsReadOnly: params.IsReadOnly,
		Metadata:   params.Metadata,
		CreatedAt:  time.Now().UTC(),
	}
}

// IsWritable returns true if the volume is writable.
func (v *Volume) IsWritable() bool {
	return !v.IsReadOnly
}
