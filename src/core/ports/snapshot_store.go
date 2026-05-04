package ports

import "context"

// SnapshotFile maps a local filesystem path to an object storage key.
type SnapshotFile struct {
	Key       string
	LocalPath string
}

// SnapshotStore abstracts object storage for VM snapshot binary files.
type SnapshotStore interface {
	// ObjectExists checks whether an object exists at the provided key.
	ObjectExists(ctx context.Context, key string) (bool, error)

	// UploadFiles uploads multiple files to object storage.
	// Returns the total bytes uploaded.
	UploadFiles(ctx context.Context, files []SnapshotFile) (int64, error)

	// DownloadFiles downloads multiple files from object storage to local paths.
	// Creates parent directories as needed.
	DownloadFiles(ctx context.Context, files []SnapshotFile) error

	// DeletePrefix removes all objects under a given key prefix.
	DeletePrefix(ctx context.Context, prefix string) error

	// EnsureBucket creates the target bucket if it does not exist.
	EnsureBucket(ctx context.Context) error
}
