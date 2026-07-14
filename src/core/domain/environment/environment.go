// Package environment defines the environment domain entity.
// An environment defines the base image and resource configuration for VMs.
package environment

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Type represents the environment base image type.
type Type string

const (
	TypeAlpine Type = "alpine"
	TypeBun    Type = "bun"
	TypeNode   Type = "node"
	TypePython Type = "python"
	TypeUv     Type = "uv"
	TypeUbuntu Type = "ubuntu"
)

// ResourceLimits defines the resource constraints for an environment.
type ResourceLimits struct {
	VCPU     int `json:"vcpu"`
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
}

// Environment represents a base environment configuration for VM instances.
type Environment struct {
	ID             string           `db:"id"`
	Type           Type             `db:"type"`
	ImagePath      string           `db:"image_path"`
	ResourceLimits ResourceLimits   `db:"resource_limits"`
	Description    string           `db:"description"`
	Metadata       *json.RawMessage `db:"metadata"` // Flexible metadata (nullable)
	DiffSnapshots  bool             `db:"diff_snapshots"`
	CreatedAt      time.Time        `db:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at"`
}

// CreateParams contains the parameters for creating a new environment.
type CreateParams struct {
	Type           Type
	ImagePath      string
	ResourceLimits ResourceLimits
	Description    string
	Metadata       *json.RawMessage
	DiffSnapshots  bool
}

// New creates a new Environment with a generated ID and timestamps.
func New(params CreateParams) *Environment {
	now := time.Now().UTC()
	var metadata *json.RawMessage
	if params.Metadata != nil {
		metadata = params.Metadata
	}
	return &Environment{
		ID:             uuid.NewString(),
		Type:           params.Type,
		ImagePath:      params.ImagePath,
		ResourceLimits: params.ResourceLimits,
		Description:    params.Description,
		Metadata:       metadata,
		DiffSnapshots:  params.DiffSnapshots,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// GetVCPU returns the number of vCPUs for this environment.
func (e *Environment) GetVCPU() int {
	return e.ResourceLimits.VCPU
}

// GetMemoryMB returns the memory in MB for this environment.
func (e *Environment) GetMemoryMB() int {
	return e.ResourceLimits.MemoryMB
}

// GetDiskMB returns the disk size in MB for this environment.
func (e *Environment) GetDiskMB() int {
	return e.ResourceLimits.DiskMB
}
