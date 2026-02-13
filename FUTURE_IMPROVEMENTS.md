# Future Improvements

## Tailscale Integration Plan (Deferred)

Current mode is single-PC only. This section describes how to enable secure multi-device access later without exposing public ports.

### Goal
- Access dashboard/API from multiple devices securely.
- Keep zero-trust networking and avoid router port forwarding.
- Preserve local-first Docker workflow.

### Recommended Approach
- Keep `api` service as-is.
- Add optional `tailscale` sidecar service in `docker-compose.yml` using Tailscale auth key.
- Route traffic to API through tailnet hostname.

### Proposed Compose Additions (future)
- New service: `tailscale`
  - image: `tailscale/tailscale:stable`
  - capabilities: `NET_ADMIN`, `SYS_MODULE`
  - env:
    - `TS_AUTHKEY` (secret)
    - `TS_STATE_DIR` (`/var/lib/tailscale`)
    - `TS_HOSTNAME` (`cross-site-tracker`)
- Persist Tailscale state in a named volume.
- Keep API internal and optionally remove host port mapping.

### Security Notes
- Never commit `TS_AUTHKEY` to git.
- Use ephemeral/restricted auth keys from Tailscale admin.
- Keep ACLs in Tailscale to limit which devices can access app ports.

### Rollout Steps
1. Create Tailscale auth key in admin console.
2. Add sidecar service and state volume to compose.
3. Add `.env` entries for Tailscale variables.
4. Start stack and verify device appears in tailnet.
5. Access app using tailnet DNS name + port.
6. Optionally remove local/public port binding based on your preferred access model.

### Validation Checklist
- App reachable from second device over tailnet.
- API health endpoint works from tailnet URL.
- Dashboard loads and CRUD actions succeed.
- Scheduler/notifications continue to run normally.

### Nice-to-have (later)
- Add profile-based compose (`--profile remote`) to toggle Tailscale on/off.
- Add docs for MagicDNS + HTTPS reverse proxy.
- Add startup health checks for sidecar connectivity.
