package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

type vmRepository struct {
	db *DB
}

type vmRow struct {
	ID                 string     `db:"id"`
	EnvironmentID      string     `db:"environment_id"`
	Provider           string     `db:"provider"`
	Status             string     `db:"status"`
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
	query := `
		INSERT INTO vm_instances (
			id, environment_id, provider, status,
			runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
			vcpu, memory_mb, disk_mb,
			ip_address, chat_id, assigned_at, terminated_at, created_at
		)
		VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15,
			$16, $17, $18, $19, $20
		)
	`

	guestCID := toNullableInt64(vm.GuestCID)

	if _, err := r.db.ExecContext(ctx, query,
		vm.ID, vm.EnvironmentID, string(vm.Provider), string(vm.Status),
		vm.RuntimeID, vm.SocketPath, vm.VsockPath, guestCID, vm.PID, vm.RuntimeState, vm.LastHeartbeatAt, vm.IdleDeadlineAt,
		vm.VCPU, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.ChatID, vm.AssignedAt, vm.TerminatedAt, vm.CreatedAt,
	); err != nil {
		return exception.Internal(fmt.Errorf("create vm: %w", err))
	}

	return nil
}

func (r *vmRepository) GetByID(ctx context.Context, id string) (*vmdomain.VM, error) {
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE id = $1
	`

	var row vmRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm", id)
		}
		return nil, exception.Internal(fmt.Errorf("get vm by id: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) Update(ctx context.Context, vm *vmdomain.VM) error {
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
		    terminated_at = $19
		WHERE id = $1
	`

	guestCID := toNullableInt64(vm.GuestCID)

	result, err := r.db.ExecContext(ctx, query,
		vm.ID, vm.EnvironmentID, string(vm.Provider), string(vm.Status),
		vm.RuntimeID, vm.SocketPath, vm.VsockPath, guestCID, vm.PID, vm.RuntimeState, vm.LastHeartbeatAt, vm.IdleDeadlineAt,
		vm.VCPU, vm.MemoryMB, vm.DiskMB,
		vm.IPAddress, vm.ChatID, vm.AssignedAt, vm.TerminatedAt,
	)
	if err != nil {
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
	query := `DELETE FROM vm_instances WHERE id = $1`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
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
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
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
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE provider = $1
		  AND status IN ('ready', 'idle')
		  AND chat_id IS NULL
		ORDER BY created_at ASC
		LIMIT $2
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, string(provider), limit); err != nil {
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
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE environment_id = $1
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, envID); err != nil {
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
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
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
		return nil, exception.Internal(fmt.Errorf("get vm by chat id: %w", err))
	}

	return mapVMRow(row)
}

func (r *vmRepository) GetActiveVMs(ctx context.Context) ([]*vmdomain.VM, error) {
	query := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE status IN ('ready', 'running', 'idle')
		ORDER BY created_at DESC
	`

	rows := make([]vmRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
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

func (r *vmRepository) AssignToChatIfAvailable(ctx context.Context, vmID, chatID string, idleDeadlineAt *time.Time) (*vmdomain.VM, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
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
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE id = $1
		FOR UPDATE
	`

	var row vmRow
	if err := tx.GetContext(ctx, &row, lockQuery, vmID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("vm", vmID)
		}
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
		return nil, exception.Internal(fmt.Errorf("update vm assignment: %w", err))
	}

	insertLeaseQuery := `
		INSERT INTO vm_leases (chat_id, vm_id, leased_at)
		VALUES ($1, $2, NOW())
	`
	if _, err := tx.ExecContext(ctx, insertLeaseQuery, chatID, vmID); err != nil {
		return nil, exception.Internal(fmt.Errorf("create vm lease: %w", err))
	}

	readQuery := `
		SELECT id, environment_id, provider, status,
		       runtime_id, socket_path, vsock_path, guest_cid, pid, runtime_state_source, last_heartbeat_at, idle_deadline_at,
		       vcpu, memory_mb, disk_mb,
		       ip_address, chat_id, assigned_at, terminated_at, created_at
		FROM vm_instances
		WHERE id = $1
	`

	if err := tx.GetContext(ctx, &row, readQuery, vmID); err != nil {
		return nil, exception.Internal(fmt.Errorf("read assigned vm: %w", err))
	}

	if err := tx.Commit(); err != nil {
		return nil, exception.Internal(fmt.Errorf("commit vm assignment tx: %w", err))
	}
	tx = nil

	return mapVMRow(row)
}

func (r *vmRepository) ReleaseActiveLeaseByVM(ctx context.Context, vmID string) error {
	query := `
		UPDATE vm_leases
		SET released_at = NOW()
		WHERE vm_id = $1
		  AND released_at IS NULL
	`

	if _, err := r.db.ExecContext(ctx, query, vmID); err != nil {
		return exception.Internal(fmt.Errorf("release vm lease: %w", err))
	}

	return nil
}

func (r *vmRepository) ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error) {
	query := `
		SELECT id, chat_id, vm_id, leased_at, released_at
		FROM vm_leases
		WHERE chat_id = $1
		  AND released_at IS NULL
		ORDER BY leased_at DESC
	`

	rows := make([]vmLeaseRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, chatID); err != nil {
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

func mapVMRow(row vmRow) (*vmdomain.VM, error) {
	v := &vmdomain.VM{
		ID:            row.ID,
		EnvironmentID: row.EnvironmentID,
		Provider:      vmdomain.Provider(row.Provider),
		Status:        vmdomain.Status(row.Status),
		RuntimeID:     row.RuntimeID,
		SocketPath:    row.SocketPath,
		VsockPath:     row.VsockPath,
		PID:           row.PID,
		RuntimeState:  row.RuntimeStateSource,
		VCPU:          row.VCPU,
		MemoryMB:      row.MemoryMB,
		DiskMB:        row.DiskMB,
		IPAddress:     row.IPAddress,
		ChatID:        row.ChatID,
		CreatedAt:     row.CreatedAt,
	}

	v.AssignedAt = row.AssignedAt
	v.TerminatedAt = row.TerminatedAt
	v.LastHeartbeatAt = row.LastHeartbeatAt
	v.IdleDeadlineAt = row.IdleDeadlineAt

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
