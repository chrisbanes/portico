# Route destination connections through the Mac

Portals connect to their web-service destinations through the Mac's normal
network stack, rather than through each Portal's tsnet identity. This makes a
destination reachable exactly when the Mac can reach it and avoids coupling
destination access to the Portal's tailnet ACLs and subnet routes. A Remote App
may use any non-loopback DNS name or IPv4 or IPv6 address reachable through
that stack, including private, public, Tailscale, and routed networks.
