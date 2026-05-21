package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

type vmRepository struct {
	db *DB
}

type vmRow struct {
	ID                 string     `db:"id"`
	EnvironmentID      string     `db:"environment_id"`
	ConversationID     *string    `db:"conversation_id"`
	Provider           string     `db:"provider"`
	Status             string     `db:"status"`
	WorkspaceSizeGB    *int       `db:"workspace_size_gb"`
	RuntimeID          *string    `db:"runtime_id"`
	SocketPath         *string    `db:"socket_path"`
	VsockPath          *string    `db:"vsock_path"`
	GuestCID           *int64     `db:"guest_cid"`
	PID                *int       `db:"pid"`
	RuntimeStateSource *string    `db:"runtime_state_source"`
	LastHeartbeatAt    *time.Time `db:"last_heartbeat_at"`
	IdleDeadlineAt     *time.Time `db:"idle_deadline_at"`
	VCPU               *int       `db:"vcpu"`
	MemoryMB           *int       `db:"memory_mb"`
	DiskMB             *int       `db:"disk_mb"`
	IPAddress          *string    `db:"ip_address"`
	ChatID             *string    `db:"chat_id"`
	AssignedAt         *time.Time `db:"assigned_at"`
	LastResumedAt      *time.Time `db:"last_resumed_at"`
	DiffSnapshots      bool       `db:"diff_snapshots_enabled"`
	TerminatedAt       *time.Time `db:"terminated_at"`
	CreatedAt          time.Time  `db:"created_at"`
}

type vmLeaseRow struct {
	ID         string     `db:"id"`
	ChatID     string     `db:"chat_id"`
	VMID       string     `db:"vm_id"`
	LeasedAt   time.Time  `db:"leased_at"`
	ReleasedAt *time.Time `db:"released_at"`
}

// NewVMRepository creates a VM repository backed by PostgreSQL.
func NewVMRepository(db *DB) vmdomain.Repository {
	return &vmRepository{db: db}
}

func (r *vmRepository) Create(ctx context.Context, vm *vmdomain.VM) error {
	logger := pkglog.FromContext(ctx)
	query := `
		INSERT INTO vm_instances (
			id, environment_id, provider, status,
			runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
			vcpu, memory_mb, disk_mb,
			ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb,
			diff_snapshots_enabled
		)
		VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15,
				$16, $17, $18, $19, $20, $21, $22, $23,
				$24
		)
	`

	guestCID := toNullableInt64(vm.GuestCID)

	if _, err := r.db.ExecContext(ctx, query,
		vm.ID, vm.EnvironmentID, string(vm.Provider), string(vm.Status),
		vm.RuntimeID, vm.SocketPath, vm.VsockPath, guestCID, vm.PID, vm.RuntimeState, vm.LastHeartbeatAt, vm.IdleDeadlineAt,
		vm.VCPU, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.ChatID, vm.AssignedAt, vm.LastResumedAt, vm.TerminatedAt, vm.CreatedAt,
		toNullableString(vm.ConversationID), vm.WorkspaceSizeGB, vm.DiffSnapshotsEnabled,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: create vm failed", "vm_id", vm.ID, "error", err)
		return exception.Internal(fmt.Errorf("create vm: %w", err))
	}

	return nil
}

func (r *vmRepository) GetByID(ctx context.Context, id string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE id = $1
	`

	var row vmRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm", id)
		}
		logger.ErrorContext(ctx, "postgres: get vm by id failed", "vm_id", id, "error", err)
		return nil, exception.Internal(fmt.Errorf("get vm by id: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) Update(ctx context.Context, vm *vmdomain.VM) error {
	logger := pkglog.FromContext(ctx)
	query := `
		UPDATE vm_instances
		SET environment_id = $2,
		    provider = $3,
		    status = $4,
		    runtime_id = $5,
		    socket_path = $6,
		    vsock_path = $7,
		    guest_cid = $8,
		    pid = $9,
		    runtime_state_source = $10,
		    last_heartbeat_at = $11,
		    idle_deadline_at = $12,
		    vcpu = $13,
		    memory_mb = $14,
		    disk_mb = $15,
		    ip_address = $16,
		    chat_id = $17,
		    assigned_at = $18,
		    last_resumed_at = $19,
		    terminated_at = $20,
		    conversation_id = $21,
		    workspace_size_gb = $22
		WHERE id = $1
	`

	guestCID := toNullableInt64(vm.GuestCID)

	result, err := r.db.ExecContext(ctx, query,
		vm.ID, vm.EnvironmentID, string(vm.Provider), string(vm.Status),
		vm.RuntimeID, vm.SocketPath, vm.VsockPath, guestCID, vm.PID, vm.RuntimeState, vm.LastHeartbeatAt, vm.IdleDeadlineAt,
		vm.VCPU, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.ChatID, vm.AssignedAt, vm.LastResumedAt, vm.TerminatedAt,
		toNullableString(vm.ConversationID), vm.WorkspaceSizeGB,
	)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: update vm failed", "vm_id", vm.ID, "error", err)
		return exception.Internal(fmt.Errorf("update vm: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("update vm rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("vm", vm.ID)
	}

	return nil
}

func (r *vmRepository) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)
	query := `DELETE FROM vm_instances WHERE id = $1`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete vm failed", "vm_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete vm: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete vm rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("vm", id)
	}

	return nil
}

func (r *vmRepository) List(ctx context.Context) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		logger.ErrorContext(ctx, "postgres: list vms failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("list vms: %w", err))
	}

	out := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}

	return out, nil
}

func (r *vmRepository) GetAvailablePool(ctx context.Context, provider vmdomain.Provider, limit int) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE provider = $1
		  AND status IN ('ready', 'idle')
		  AND chat_id IS NULL
		ORDER BY created_at ASC
		LIMIT $2
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, string(provider), limit); err != nil {
		logger.ErrorContext(ctx, "postgres: get available vm pool failed", "provider", provider, "error", err)
		return nil, exception.Internal(fmt.Errorf("get available vm pool: %w", err))
	}

	out := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}

	return out, nil
}

func (r *vmRepository) GetByEnvironmentID(ctx context.Context, envID string) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE environment_id = $1
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, envID); err != nil {
		logger.ErrorContext(ctx, "postgres: get vms by environment id failed", "env_id", envID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get vms by environment id: %w", err))
	}

	out := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}

	return out, nil
}

func (r *vmRepository) GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE chat_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`

	var row vmRow
	if err := r.db.GetContext(ctx, &row, query, chatID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm chat", chatID)
		}
		logger.ErrorContext(ctx, "postgres: get vm by chat id failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get vm by chat id: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) GetActiveVMs(ctx context.Context) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE status IN ('ready', 'running', 'idle')
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		logger.ErrorContext(ctx, "postgres: get active vms failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("get active vms: %w", err))
	}

	out := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}

	return out, nil
}

func (r *vmRepository) GetActiveByUserID(ctx context.Context, userID string) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT DISTINCT ON (vm.id)
		       vm.id, vm.environment_id, vm.provider, vm.status,
		       vm.runtime_id, vm.socket_path, vm.vsock_path, vm.guest_cid, vm.pid, vm.runtime_state_source,
		       vm.last_heartbeat_at, vm.idle_deadline_at,
		       vm.vcpu, vm.memory_mb, vm.disk_mb,
		       vm.ip_address, vm.chat_id, vm.assigned_at, vm.last_resumed_at, vm.terminated_at, vm.created_at,
		       vm.conversation_id, vm.workspace_size_gb, vm.diff_snapshots_enabled
		FROM vm_instances vm
		JOIN vm_leases l ON l.vm_id = vm.id
		JOIN chats c ON c.id = l.chat_id
		WHERE c.user_id = $1
		  AND vm.status IN ('ready', 'running', 'idle')
		  AND l.released_at IS NULL
		ORDER BY vm.id, vm.created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, userID); err != nil {
		logger.ErrorContext(ctx, "postgres: get active vms by user failed", "user_id", userID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get active vms by user: %w", err))
	}

	out := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
	}

	return out, nil
}

func (r *vmRepository) GetByEnvironmentAndChatID(ctx context.Context, envID, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT vm.id, vm.environment_id, vm.provider, vm.status,
		       vm.runtime_id, vm.socket_path, vm.vsock_path, vm.guest_cid, vm.pid, vm.runtime_state_source, vm.last_heartbeat_at, vm.idle_deadline_at,
		       vm.vcpu, vm.memory_mb, vm.disk_mb,
		       vm.ip_address, vm.chat_id, vm.assigned_at, vm.last_resumed_at, vm.terminated_at, vm.created_at, vm.conversation_id, vm.workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances vm
		JOIN vm_leases l ON l.vm_id = vm.id
		WHERE vm.environment_id = $1
		  AND l.chat_id = $2
		  AND vm.status != 'terminated'
		ORDER BY l.leased_at DESC
		LIMIT 1
	`

	var row vmRow
	if err := r.db.GetContext(ctx, &row, query, envID, chatID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm by environment and chat", envID)
		}
		logger.ErrorContext(ctx, "postgres: get vm by environment and chat id failed", "env_id", envID, "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get vm by environment and chat id: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) AssignToChatIfAvailable(ctx context.Context, vmID, chatID string, idleDeadlineAt *time.Time) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: begin vm assignment tx failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("begin vm assignment tx: %w", err))
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	lockQuery := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE id = $1
		FOR UPDATE
	`

	var row vmRow
	if err := tx.GetContext(ctx, &row, lockQuery, vmID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm", vmID)
		}
		logger.ErrorContext(ctx, "postgres: lock vm for assignment failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("lock vm for assignment: %w", err))
	}

	v, err := mapVMRow(row)
	if err != nil {
		return nil, err
	}

	if !v.IsAvailable() {
		return nil, exception.BadRequest("VM is not available")
	}

	updateQuery := `
		UPDATE vm_instances
		SET status = $2,
		    chat_id = $3,
		    assigned_at = NOW(),
		    idle_deadline_at = $4
		WHERE id = $1
	`

	if _, err := tx.ExecContext(ctx, updateQuery, vmID, string(vmdomain.StatusRunning), chatID, idleDeadlineAt); err != nil {
		logger.ErrorContext(ctx, "postgres: update vm assignment failed", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("update vm assignment: %w", err))
	}

	insertLeaseQuery := `
		INSERT INTO vm_leases (chat_id, vm_id, leased_at)
		VALUES ($1, $2, NOW())
	`
	if _, err := tx.ExecContext(ctx, insertLeaseQuery, chatID, vmID); err != nil {
		logger.ErrorContext(ctx, "postgres: create vm lease failed", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("create vm lease: %w", err))
	}

	readQuery := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, last_resumed_at, terminated_at, created_at, conversation_id, workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances
		WHERE id = $1
	`

	if err := tx.GetContext(ctx, &row, readQuery, vmID); err != nil {
		logger.ErrorContext(ctx, "postgres: read assigned vm failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("read assigned vm: %w", err))
	}

	if err := tx.Commit(); err != nil {
		logger.ErrorContext(ctx, "postgres: commit vm assignment tx failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("commit vm assignment tx: %w", err))
	}
	tx = nil

	return mapVMRow(row)
}

func (r *vmRepository) ReleaseActiveLeaseByVM(ctx context.Context, vmID string) error {
	logger := pkglog.FromContext(ctx)
	query := `
		UPDATE vm_leases
		SET released_at = NOW()
		WHERE vm_id = $1
		  AND released_at IS NULL
	`

	if _, err := r.db.ExecContext(ctx, query, vmID); err != nil {
		logger.ErrorContext(ctx, "postgres: release vm lease failed", "vm_id", vmID, "error", err)
		return exception.Internal(fmt.Errorf("release vm lease: %w", err))
	}

	return nil
}

func (r *vmRepository) ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, chat_id, vm_id, leased_at, released_at
		FROM vm_leases
		WHERE chat_id = $1
		  AND released_at IS NULL
		ORDER BY leased_at DESC
	`

	rows := make([]vmLeaseRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, chatID); err != nil {
		logger.ErrorContext(ctx, "postgres: list active vm leases failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list active vm leases: %w", err))
	}

	out := make([]vmdomain.Lease, 0, len(rows))
	for _, row := range rows {
		out = append(out, vmdomain.Lease{
			ID:         row.ID,
			ChatID:     row.ChatID,
			VMID:       row.VMID,
			LeasedAt:   row.LeasedAt.UTC(),
			ReleasedAt: row.ReleasedAt,
		})
	}

	return out, nil
}

func (r *vmRepository) FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT vm.id, vm.environment_id, vm.provider, vm.status,
		       vm.runtime_id, vm.socket_path, vm.vsock_path, vm.guest_cid, vm.pid, vm.runtime_state_source, vm.last_heartbeat_at, vm.idle_deadline_at,
		       vm.vcpu, vm.memory_mb, vm.disk_mb,
		       vm.ip_address, vm.chat_id, vm.assigned_at, vm.last_resumed_at, vm.terminated_at, vm.created_at, vm.conversation_id, vm.workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances vm
		JOIN vm_leases l ON l.vm_id = vm.id
		WHERE l.chat_id = $1
		  AND vm.status IN ('idle', 'ready', 'terminated')
		  AND vm.chat_id IS NULL
		ORDER BY l.leased_at DESC
		LIMIT 1
	`

	var row vmRow
	if err := r.db.GetContext(ctx, &row, query, chatID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("previous vm for chat", chatID)
		}
		logger.ErrorContext(ctx, "postgres: find previous lease for chat failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("find previous lease for chat: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) ListPreviousLeasesForChat(ctx context.Context, chatID string) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT vm.id, vm.environment_id, vm.provider, vm.status,
		       vm.runtime_id, vm.socket_path, vm.vsock_path, vm.guest_cid, vm.pid, vm.runtime_state_source, vm.last_heartbeat_at, vm.idle_deadline_at,
		       vm.vcpu, vm.memory_mb, vm.disk_mb,
		       vm.ip_address, vm.chat_id, vm.assigned_at, vm.last_resumed_at, vm.terminated_at, vm.created_at, vm.conversation_id, vm.workspace_size_gb, diff_snapshots_enabled
		FROM vm_instances vm
		JOIN vm_leases l ON l.vm_id = vm.id
		WHERE l.chat_id = $1
		  AND vm.status IN ('idle', 'ready', 'terminated')
		  AND vm.chat_id IS NULL
		ORDER BY l.leased_at DESC
	`

	var rows []vmRow
	if err := r.db.SelectContext(ctx, &rows, query, chatID); err != nil {
		logger.ErrorContext(ctx, "postgres: list previous leases for chat failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list previous leases for chat: %w", err))
	}

	vms := make([]*vmdomain.VM, 0, len(rows))
	for _, row := range rows {
		vm, err := mapVMRow(row)
		if err != nil {
			continue
		}
		vms = append(vms, vm)
	}

	return vms, nil
}

func mapVMRow(row vmRow) (*vmdomain.VM, error) {
	v := &vmdomain.VM{
		ID:              row.ID,
		EnvironmentID:   row.EnvironmentID,
		ConversationID:  nullableStringValue(row.ConversationID),
		Provider:        vmdomain.Provider(row.Provider),
		Status:          vmdomain.Status(row.Status),
		WorkspaceSizeGB: nullableIntValue(row.WorkspaceSizeGB, 2),
		RuntimeID:       row.RuntimeID,
		SocketPath:      row.SocketPath,
		VsockPath:       row.VsockPath,
		PID:             row.PID,
		RuntimeState:    row.RuntimeStateSource,
		VCPU:            row.VCPU,
		MemoryMB:        row.MemoryMB,
		DiskMB:          row.DiskMB,
		IPAddress:       row.IPAddress,
		ChatID:          row.ChatID,
		CreatedAt:       row.CreatedAt,
	}

	v.AssignedAt = row.AssignedAt
	v.TerminatedAt = row.TerminatedAt
	v.LastResumedAt = row.LastResumedAt
	v.LastHeartbeatAt = row.LastHeartbeatAt
	v.IdleDeadlineAt = row.IdleDeadlineAt
	v.DiffSnapshotsEnabled = row.DiffSnapshots

	if row.GuestCID != nil {
		if *row.GuestCID < 0 || *row.GuestCID > 4294967295 {
			return nil, exception.Internal(fmt.Errorf("invalid guest cid value %d for vm %s", *row.GuestCID, row.ID))
		}
		cid := uint32(*row.GuestCID)
		v.GuestCID = &cid
	}

	return v, nil
}

func toNullableInt64(v *uint32) *int64 {
	if v == nil {
		return nil
	}
	r := int64(*v)
	return &r
}

func toNullableString(v string) *string {
	if v == "" {
		return nil
	}
	value := v
	return &value
}

func nullableStringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func nullableIntValue(v *int, fallback int) int {
	if v == nil || *v <= 0 {
		return fallback
	}
	return *v
}

func (r *vmRepository) GetAllocatedIPs(ctx context.Context) ([]string, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT ip_address
		FROM vm_instances
		WHERE ip_address IS NOT NULL
		  AND status != 'terminated'
	`

	var ips []string
	if err := r.db.SelectContext(ctx, &ips, query); err != nil {
		logger.ErrorContext(ctx, "postgres: get allocated ips failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("get allocated ips: %w", err))
	}
	if ips == nil {
		ips = []string{}
	}
	return ips, nil
}

func (r *vmRepository) GetAllocatedIPsExclude(ctx context.Context, excludeVMID string) ([]string, error) {
	query := `
		SELECT ip_address
		FROM vm_instances
		WHERE ip_address IS NOT NULL
		  AND status != 'terminated'
		  AND id != $1
	`
	var ips []string
	if err := r.db.SelectContext(ctx, &ips, query, excludeVMID); err != nil {
		return nil, exception.Internal(fmt.Errorf("get allocated ips: %w", err))
	}
	if ips == nil {
		ips = []string{}
	}
	return ips, nil
}

func (r *vmRepository) SetIPAddress(ctx context.Context, vmID string, ip string) error {
	logger := pkglog.FromContext(ctx)
	query := `UPDATE vm_instances SET ip_address = $1 WHERE id = $2`

	result, err := r.db.ExecContext(ctx, query, ip, vmID)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: set ip address failed", "vm_id", vmID, "ip", ip, "error", err)
		return exception.Internal(fmt.Errorf("set ip address: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("set ip address rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("vm", vmID)
	}

	return nil
}

func (r *vmRepository) ReleaseIPAddress(ctx context.Context, vmID string) error {
	logger := pkglog.FromContext(ctx)
	query := `UPDATE vm_instances SET ip_address = NULL WHERE id = $1`

	if _, err := r.db.ExecContext(ctx, query, vmID); err != nil {
		logger.ErrorContext(ctx, "postgres: release ip address failed", "vm_id", vmID, "error", err)
		return exception.Internal(fmt.Errorf("release ip address: %w", err))
	}

	return nil
}
