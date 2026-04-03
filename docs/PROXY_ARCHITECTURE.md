# Multi-Site Reverse Proxy Architecture Decision

## Requirements Summary

- Proxy multiple websites hosted behind VPN
- Use ports 443 (HTTPS) and 80 (HTTP) exclusively
- Create mDNS entries for each proxied site (`sitename.local`)
- Route based on Host header / SNI (TLS Server Name Indication)
- Fallback site: single IP/port with TLS that lists all registered routes
- Fallback should be navigable as path-based URLs from the base IP/port

## Architectural Options

### Option 1: Built-in Implementation (Go in `rip-bastion`)

**Implementation**: Expand the existing `webui` module into a full reverse proxy using Go's `net/http` and `crypto/tls` packages.

#### Pros
- ✅ Single Go binary (no additional dependencies)
- ✅ Tight integration with mDNS registration lifecycle
- ✅ Can dynamically register/deregister mDNS entries as routes are added
- ✅ Direct access to VPN state for route validation
- ✅ Minimal resource overhead (Go is efficient)
- ✅ Full control over behavior (custom fallback logic, metrics, logging)
- ✅ Easier to extend with custom features (e.g., auth, rate limiting)
- ✅ Simple configuration (can integrate with existing config system)

#### Cons
- ❌ Requires implementing TLS termination, certificate handling (or using mkcert/ACME)
- ❌ Requires implementing HTTP/HTTPS reverse proxy middleware from scratch
- ❌ More testing burden (proxying, TLS, SNI routing, fallback routing)
- ❌ Need to handle certificate lifecycle (generation, rotation, storage)
- ❌ Scaling complexity (e.g., session affinity, load balancing features)
- ❌ More application code to maintain

**Resource Profile**
- Memory: ~20-30 MB (Go base) + ~1-2 MB per route
- CPU: Minimal (event-driven, no inefficient processes)
- Disk: Minimal (certificates need storage)

**Complexity Score**: Medium-High (reverse proxy is non-trivial)

**Implementation Effort**: 4-6 weeks (including testing and certificate handling)

---

### Option 2: Caddy

**Implementation**: Run Caddy as a separate service, configure via dynamic API or JSON file.

#### Pros
- ✅ **Excellent** for this use case (SNI routing, dynamic config)
- ✅ Automatic HTTPS with self-signed or ACME certificates
- ✅ Built-in HTTP→HTTPS redirects
- ✅ Dynamic API for adding/removing routes at runtime
- ✅ Proven, battle-tested reverse proxy
- ✅ Minimal configuration overhead
- ✅ Small footprint (~30 MB binary)
- ✅ Can integrate with external service discovery (your mDNS could trigger Caddy API)
- ✅ Excellent documentation and community

#### Cons
- ❌ Adds external process/dependency (not monolithic)
- ❌ Slightly higher memory overhead (~40-60 MB)
- ❌ Need to manage service lifecycle (startup, restarts)
- ❌ Certificate storage external to main app
- ❌ IPC between `rip-bastion` and Caddy needed (HTTP API or files)
- ❌ Operational complexity (debugging requires looking at multiple processes)

**Resource Profile**
- Memory: ~40-60 MB
- CPU: Low (event-driven, production-grade)
- Disk: ~10-20 MB (binary) + certificates

**Complexity Score**: Low (Caddy handles most details)

**Integration Effort**: 2-3 weeks (mostly learning Caddy config, building mDNS→Caddy API bridge)

**Recommended Configuration**:
```caddyfile
# Dynamic TLS with SNI-based routing
:443 {
  tls internal  # or acme for Let's Encrypt

  @host1 {
    header Host site1.local
  }
  
  @host2 {
    header Host site2.local
  }

  route @host1 /* {
    reverse_proxy 192.168.1.100:8001
  }

  route @host2 /* {
    reverse_proxy 192.168.1.100:8002
  }

  # Fallback route listing
  route /* {
    respond "Available sites: site1.local, site2.local" 200
  }
}

:80 {
  # Redirect to HTTPS
  redir https://{host}{uri}
}
```

---

### Option 3: Traefik

**Implementation**: Run Traefik as a separate service with dynamic configuration via labels or files.

#### Pros
- ✅ Designed for container/microservice environments
- ✅ Automatic service discovery (could integrate with service registry)
- ✅ Excellent for complex routing scenarios
- ✅ Hot-reload configuration without restarts
- ✅ Strong plugin ecosystem
- ✅ Built-in metrics and tracing (Prometheus, Jaeger)

#### Cons
- ❌ **Higher complexity than needed for your use case**
- ❌ Larger footprint (~80 MB memory, ~50 MB binary)
- ❌ Overkill for single-machine reverse proxy
- ❌ Steeper learning curve (more configuration options)
- ❌ Resource overhead (extra features you won't use)
- ❌ Better suited for orchestrated environments (Kubernetes, Docker Swarm)
- ❌ More operational overhead

**Resource Profile**
- Memory: ~80-100 MB
- CPU: Moderate
- Disk: ~50 MB (binary)

**Complexity Score**: High (too much for this use case)

**Verdict**: **Not recommended** for a single Raspberry Pi use case. Overkill.

---

### Option 4: nginx

**Implementation**: Run nginx as a reverse proxy with SNI-based routing.

#### Pros
- ✅ Battle-tested, production-grade
- ✅ Very low resource overhead
- ✅ Small footprint (~10 MB)
- ✅ Extremely fast

#### Cons
- ❌ Static configuration (requires reloads for changes)
- ❌ Complex SNI routing configuration (nginx-stream module needed)
- ❌ No built-in HTTPS certificate automation
- ❌ No dynamic service discovery
- ❌ Difficult to integrate with runtime route registration
- ❌ Steeper learning curve than Caddy
- ❌ Reload means brief connection interruptions

**Resource Profile**
- Memory: ~10-15 MB
- CPU: Very low
- Disk: ~10 MB (binary)

**Complexity Score**: Medium (but static config is limiting)

**Verdict**: **Not ideal** due to static configuration requirements. You'd need external tooling to manage config changes.

---

## Decision Matrix

| Factor | Built-in | Caddy | Traefik | nginx |
|--------|----------|-------|---------|-------|
| **mDNS Integration** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐ |
| **Resource Efficiency** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ |
| **Operational Simplicity** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐ | ⭐⭐⭐ |
| **Implementation Speed** | ⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐ |
| **Maintainability** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐⭐ |
| **Future Extensibility** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ |
| **Failure Isolation** | ⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ |

---

## Recommendation: **Caddy** (Primary) or **Built-in** (Long-term)

### Use Caddy If:
- ✅ You want to ship quickly (2-3 weeks)
- ✅ You prioritize operational simplicity
- ✅ You're comfortable with a multi-process architecture
- ✅ You want battle-tested networking code
- ✅ You may add complex features later (caching, compression, etc.)

**Implementation Path**:
1. Caddy with Caddyfile configuration
2. Build a simple bridge: `mDNS event` → `Caddy Admin API` (add/remove routes)
3. Caddy handles all TLS, routing, and fallback logic
4. Run both services in systemd or supervisor

### Use Built-in If:
- ✅ You prefer a monolithic single binary
- ✅ You want to avoid external processes
- ✅ You have time for thorough development (6-8 weeks)
- ✅ You want tight mDNS lifecycle integration
- ✅ You anticipate custom feature requirements later

**Implementation Path**:
1. Extract proxy logic from webui into a new `proxy` module
2. Use Go's `crypto/tls` with SNI callback routing
3. Build fallback route lister HTML
4. Integrate with mDNS module for automatic registration
5. Handle certificate management (self-signed or ACME)

---

## Hybrid Approach (Recommended for MVP)

Start with **Caddy** to validate the architecture:

1. **Weeks 1-2**: Deploy Caddy, build mDNS→Caddy API bridge
2. **Weeks 3-4**: Add fallback route listing page
3. **Validate**: Test with actual proxied sites, measure resource overhead
4. **If satisfied**: Keep Caddy (low overhead, proven)
5. **If not satisfied**: Migrate to built-in implementation (you now know the requirements thoroughly)

---

## Implementation Details: Caddy Bridge Example

```go
// internal/proxy/caddy.go
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type CaddyRoute struct {
	HostPattern string // e.g., "site1.local"
	Target      string // e.g., "192.168.1.100:8001"
}

// AddRoute registers a new route with Caddy via Admin API
func AddRoute(route CaddyRoute) error {
	payload := map[string]interface{}{
		"method": "POST",
		"path":   fmt.Sprintf("/admin/config/apps/http/servers/srv0/routes"),
		"body": map[string]interface{}{
			"match": []interface{}{
				map[string]interface{}{
					"header": map[string][]string{
						"Host": {route.HostPattern},
					},
				},
			},
			"handle": []interface{}{
				map[string]interface{}{
					"handler": "reverse_proxy",
					"upstreams": []interface{}{
						map[string]string{
							"dial": route.Target,
						},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	resp, err := http.Post("http://localhost:2019/admin/config/apps/http/servers/srv0/routes",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("adding caddy route: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("caddy returned %d", resp.StatusCode)
	}
	return nil
}
```

---

## Monitoring & Observability

Whichever you choose, implement:

- **Health checks**: mDNS + proxy endpoint responsiveness
- **Logging**: Proxy errors, route changes, certificate renewals
- **Metrics**: Request count, latency, upstream health
- **Fallback tracking**: When clients fallback to IP/port

Display on the existing display or webui:
```
rip-bastion Status:
├─ VPN: Connected
├─ Proxy: Active (3 routes)
│  ├─ site1.local → 192.168.1.100:8001
│  ├─ site2.local → 192.168.1.100:8002
│  └─ dashboard.local → 192.168.1.100:9000
├─ mDNS: Running
└─ Fallback: https://[bastion-ip]:443
```

---

## Certificate Strategy

### For All Options:

1. **Self-signed**: Use `mkcert` to generate local CA, sign certificates for all mDNS names
   - Pros: Simple, no external dependencies
   - Cons: Browsers warn about the certificate

2. **ACME (Let's Encrypt)**: If `rip-bastion` is reachable from the internet
   - Pros: Browser-trusted certificates
   - Cons: Requires port forwarding, domain setup

3. **Internal CA**: Use your organization's internal certificate authority
   - Pros: Enterprise-ready, trusted within org
   - Cons: Operational overhead

**Recommendation for MVP**: Self-signed with mkcert. Upgrade to ACME if needed later.

---

## Decision Framework

**Ask yourself:**

1. **How much time do you have?**
   - < 3 weeks → **Caddy**
   - 6+ weeks → **Built-in**

2. **Do you care about single binary deployment?**
   - Yes → **Built-in**
   - No → **Caddy** (simpler)

3. **Will requirements evolve?**
   - Yes (auth, rate limiting, caching) → **Caddy** or **Built-in** (avoid nginx/traefik)
   - No (basic proxy) → **Caddy** (fastest path)

4. **Is this on limited hardware?**
   - RPi Zero → **Built-in** or **nginx** (lowest overhead)
   - RPi 4+ → **Caddy** (plenty of headroom)

---

## Next Steps

1. **Validate** your certificate strategy (self-signed vs ACME)
2. **Prototype** the mDNS registration pattern (how are routes added?)
3. **Choose**: Caddy (recommend) or Built-in (if you want monolithic)
4. **Implement**: Start with fallback route listing (validates your routing logic)
5. **Test**: With actual backend services behind the VPN
