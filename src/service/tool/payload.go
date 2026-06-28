package toolsvc

import vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"

// publicDomainSuffix is the public hostname suffix cloudflared ingress
// maps each VM to. Kept in sync with the suffix passed to tunnelwriter in
// cmd/main.go.
const publicDomainSuffix = ".box.spacetrek.xyz"

// publicURL returns the user-facing URL for reaching a VM through the
// Cloudflare Tunnel, or "" when the VM is not publicly exposed (no name
// or no service port).
func publicURL(vm *vmdomain.VM) string {
	if vm == nil || vm.Name == "" || vm.ServicePort <= 0 {
		return ""
	}
	return "https://" + vm.Name + publicDomainSuffix
}

// vmBaseFields returns the identity + networking fields every VM-emitting
// tool result includes. Callers add per-tool extras (environment, restored,
// has_snapshot) to the returned map. Centralizing this keeps the LLM-facing
// payload shape consistent across vm.create / vm.start / vm.list and means
// new domain fields propagate automatically.
func vmBaseFields(vm *vmdomain.VM) map[string]any {
	return map[string]any{
		"vm_id":        vm.ID,
		"name":         vm.Name,
		"status":       string(vm.Status),
		"provider":     string(vm.Provider),
		"service_port": vm.ServicePort,
		"public_url":   publicURL(vm),
	}
}
