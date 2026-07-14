package vm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// nameLookupStub is a minimal Repository that only knows GetByName — the
// single method resolveUniqueName calls. Every other method panics so we
// notice if resolveUniqueName grows new dependencies.
type nameLookupStub struct {
	byName map[string]*vmdomain.VM
}

func (s *nameLookupStub) GetByName(_ context.Context, name string) (*vmdomain.VM, error) {
	if v, ok := s.byName[name]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, exception.NotFound("vm name", name)
}

// Stub out everything else so nameLookupStub satisfies vmdomain.Repository.
func (s *nameLookupStub) Create(context.Context, *vmdomain.VM) error { panic("not used") }
func (s *nameLookupStub) GetByID(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) Update(context.Context, *vmdomain.VM) error { panic("not used") }
func (s *nameLookupStub) Delete(context.Context, string) error       { panic("not used") }
func (s *nameLookupStub) List(context.Context) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetAvailablePool(context.Context, vmdomain.Provider, int) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetByEnvironmentID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetByChatID(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetActiveVMs(context.Context) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetActiveByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetAllByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetByEnvironmentAndChatID(context.Context, string, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) AssignToChatIfAvailable(context.Context, string, string, *time.Time) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) ReleaseActiveLeaseByVM(context.Context, string) error {
	panic("not used")
}
func (s *nameLookupStub) ListActiveLeasesByChat(context.Context, string) ([]vmdomain.Lease, error) {
	panic("not used")
}
func (s *nameLookupStub) FindPreviousLeaseForChat(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) ListPreviousLeasesForChat(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *nameLookupStub) GetAllocatedIPs(context.Context) ([]string, error) {
	panic("not used")
}
func (s *nameLookupStub) GetAllocatedIPsExclude(context.Context, string) ([]string, error) {
	panic("not used")
}
func (s *nameLookupStub) SetIPAddress(context.Context, string, string) error {
	panic("not used")
}
func (s *nameLookupStub) ReleaseIPAddress(context.Context, string) error { panic("not used") }

func TestResolveUniqueName_ExplicitCollision(t *testing.T) {
	stub := &nameLookupStub{byName: map[string]*vmdomain.VM{
		"nervous-einstein": {ID: "existing", Name: "nervous-einstein"},
	}}
	s := &Service{repo: stub}
	ctx := context.Background()

	candidate := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env"})
	candidate.Name = "nervous-einstein"
	err := s.resolveUniqueName(ctx, candidate, true)
	if err == nil {
		t.Fatal("expected Conflict error, got nil")
	}
	appErr, ok := err.(*exception.AppError)
	if !ok {
		t.Fatalf("expected *exception.AppError, got %T", err)
	}
	if appErr.StatusCode != 409 {
		t.Errorf("expected 409, got %d", appErr.StatusCode)
	}
}

func TestResolveUniqueName_ExplicitAvailable(t *testing.T) {
	stub := &nameLookupStub{byName: map[string]*vmdomain.VM{
		"nervous-einstein": {ID: "existing", Name: "nervous-einstein"},
	}}
	s := &Service{repo: stub}

	candidate := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env"})
	candidate.Name = "admiring-turing"
	if err := s.resolveUniqueName(context.Background(), candidate, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if candidate.Name != "admiring-turing" {
		t.Errorf("name changed unexpectedly: %q", candidate.Name)
	}
}

func TestResolveUniqueName_GeneratedRetriesOnCollision(t *testing.T) {
	// Seed the candidate's initial name into the stub so the first lookup
	// collides. resolveUniqueName must regenerate and the final name must
	// differ from the seeded one.
	candidate := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env"})
	original := candidate.Name

	stub := &nameLookupStub{byName: map[string]*vmdomain.VM{
		original: {ID: "blocker", Name: original},
	}}
	s := &Service{repo: stub}

	if err := s.resolveUniqueName(context.Background(), candidate, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if candidate.Name == original {
		t.Errorf("expected name to change after collision, still %q", candidate.Name)
	}
	if _, ok := stub.byName[candidate.Name]; ok {
		t.Errorf("final name %q still collides with a seeded entry", candidate.Name)
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("oops"), false},
		{"pq 23505", &pq.Error{Code: "23505"}, true},
		{"pq 23503 (FK)", &pq.Error{Code: "23503"}, false},
		{"wrapped 23505", wrapPqErr(&pq.Error{Code: "23505"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUniqueViolation(tt.err); got != tt.want {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func wrapPqErr(err error) error { return errors.Join(errors.New("outer"), err) }
