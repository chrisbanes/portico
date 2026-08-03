# Give each Portal an independent tsnet node

Each Portal is a durable, user-owned `tsnet.Server` with its own UUID-keyed
persistent state directory. This gives every Portal an independent Tailscale
node identity, IP address, MagicDNS name, and certificate without renaming,
retagging, or otherwise reusing the Mac's existing Tailscale node.
