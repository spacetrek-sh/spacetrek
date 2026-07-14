package toolsvc

import (
	"context"
	"reflect"
	"testing"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// TestVMBaseFields_AllFieldsPresent is the regression net: if a field
// silently disappears from vmBaseFields, the LLM's prior conversation
// context breaks because the tool result shape changes. Every base field
// must be present and correctly typed.
func TestVMBaseFields_AllFieldsPresent(t *testing.T) {
	ip := "10.200.0.5"
	vm := &vmdomain.VM{
		ID:          "vm-1",
		Name:        "admiring-turing",
		Status:      vmdomain.StatusRunning,
		Provider:    vmdomain.ProviderFirecracker,
		IPAddress:   &ip,
		ServicePort: 8000,
	}

	got := vmBaseFields(vm)

	want := map[string]any{
		"vm_id":        "vm-1",
		"name":         "admiring-turing",
		"status":       "running",
		"provider":     "firecracker",
		"service_port": 8000,
		"public_url":   "https://admiring-turing.box.spacetrek.xyz",
		"ip_address":   "10.200.0.5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vmBaseFields mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestVMBaseFields_PublicURLDerivedFromNameAndPort asserts the
// public_url/empty contract the system prompt and tunnelwriter depend on.
func TestVMBaseFields_PublicURLDerivedFromNameAndPort(t *testing.T) {
	cases := []struct {
		name    string
		vmName  string
		port    int
		wantURL string
	}{
		{"both present", "admiring-turing", 80, "https://admiring-turing.box.spacetrek.xyz"},
		{"port zero", "admiring-turing", 0, ""},
		{"name empty", "", 8000, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vm := &vmdomain.VM{
				ID:          "vm-1",
				Name:        c.vmName,
				Status:      vmdomain.StatusRunning,
				Provider:    vmdomain.ProviderFirecracker,
				ServicePort: c.port,
			}
			if got := vmBaseFields(vm)["public_url"]; got != c.wantURL {
				t.Errorf("public_url = %v, want %q", got, c.wantURL)
			}
		})
	}
}

// TestEnrichedPayload_IncludesBaseAndExtras verifies vm.start's payload
// rendering: base fields from vmBaseFields plus the optional environment
// hint and restored flag.
func TestEnrichedPayload_IncludesBaseAndExtras(t *testing.T) {
	ip := "10.200.0.5"
	vm := &vmdomain.VM{
		ID:          "vm-1",
		Name:        "nervous-lovelace",
		Status:      vmdomain.StatusRunning,
		Provider:    vmdomain.ProviderFirecracker,
		IPAddress:   &ip,
		ServicePort: 3000,
	}

	// Minimal stub: only ResolveEnvironmentHint is exercised by enrichedPayload.
	r := &minRestarter{envHint: "bun"}

	got := enrichedPayload(vm, r, context.Background(), true)

	want := map[string]any{
		"vm_id":        "vm-1",
		"name":         "nervous-lovelace",
		"status":       "running",
		"provider":     "firecracker",
		"service_port": 3000,
		"public_url":   "https://nervous-lovelace.box.spacetrek.xyz",
		"ip_address":   "10.200.0.5",
		"restored":     true,
		"environment":  "bun",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enrichedPayload mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnrichedPayload_OmitsAbsentExtras verifies that when the environment
// hint is empty or the restored flag is false, those keys are absent —
// not present-with-zero-value. The LLM treats missing keys differently
// from empty values.
func TestEnrichedPayload_OmitsAbsentExtras(t *testing.T) {
	vm := &vmdomain.VM{
		ID:          "vm-1",
		Name:        "x",
		Status:      vmdomain.StatusReady,
		Provider:    vmdomain.ProviderFirecracker,
		ServicePort: 80,
	}
	r := &minRestarter{envHint: ""} // no env hint

	got := enrichedPayload(vm, r, context.Background(), false)

	if _, ok := got["restored"]; ok {
		t.Errorf("restored should be absent when restore=false, got %v", got["restored"])
	}
	if _, ok := got["environment"]; ok {
		t.Errorf("environment should be absent when hint is empty, got %v", got["environment"])
	}
	// Base fields still present.
	if got["vm_id"] != "vm-1" {
		t.Errorf("vm_id missing or wrong: %v", got["vm_id"])
	}
}

// minRestarter implements only the VMRestarter methods enrichedPayload
// exercises. Every other method panics so we notice drift.
type minRestarter struct {
	envHint string
}

func (m *minRestarter) ResolveEnvironmentHint(context.Context, string) (string, error) {
	return m.envHint, nil
}
func (m *minRestarter) FindPreviousLeaseForChat(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (m *minRestarter) Get(context.Context, string) (*vmdomain.VM, error) { panic("not used") }
func (m *minRestarter) AssignToChat(context.Context, string, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (m *minRestarter) HasSnapshot(context.Context, string) bool { panic("not used") }
func (m *minRestarter) ResumeVM(context.Context, string, string) (*vmdomain.VM, error) {
	panic("not used")
}
