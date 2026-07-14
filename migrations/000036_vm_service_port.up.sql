-- Per-VM backend port for cloudflared ingress. The tunnelwriter renders
-- one ingress entry per non-terminated VM mapping <vm-name>.spacetrek.xyz
-- to http://<vm-ip>:<service_port>. Default 80 covers the common case
-- (user runs an HTTP server on the standard port inside the VM).
ALTER TABLE vm_instances
    ADD COLUMN service_port INT NOT NULL DEFAULT 80;

COMMENT ON COLUMN vm_instances.service_port IS
    'Backend port cloudflared forwards to on this VM. Default 80.';
