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
