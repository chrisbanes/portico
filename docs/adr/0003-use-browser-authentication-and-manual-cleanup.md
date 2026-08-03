# Use browser authentication and manual tailnet cleanup

MVP Portals are user-owned nodes authenticated in the browser. Portico does
not accept auth keys or request a Tailscale API credential; removing a Portal
deletes its local identity after a clear warning, while remote node deletion
remains a documented manual action in the Tailscale admin console.
