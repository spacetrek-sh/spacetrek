package toolsvc

import vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"

// vmBaseFields returns the identity + networking fields every VM-emitting
// tool result includes. Callers add per-tool extras (environment, restored,
// has_snapshot) to the returned map. Centralizing this keeps the LLM-facing
// payload shape consistent across vm.create / vm.start / vm.list and means
// new domain fields propagate automatically.
func vmBaseFields(vm *vmdomain.VM) map[string]any {
	payload := map[string]any{
		"vm_id":        vm.ID,
		"name":         vm.Name,
		"status":       string(vm.Status),
		"provider":     string(vm.Provider),
		"service_port": vm.ServicePort,
		"public_url":   vm.PublicURL(),
	}
	if vm.IPAddress != nil && *vm.IPAddress != "" {
		payload["ip_address"] = *vm.IPAddress
	}
	return payload
}

