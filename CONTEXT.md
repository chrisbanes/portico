# Portico

Portico gives a web service reachable from a Mac a durable private doorway on
a Tailscale tailnet. This glossary defines the product language used in the
app, documentation, and code.

## Language

**Portal**:
A durable private doorway from an HTTPS tailnet address to one Portal
Destination. A Portal retains its identity until it is removed, even when it
is repointed to a different Portal Destination.
_Avoid_: Mapping, endpoint, tunnel

**Portal Name**:
The immutable name a user requests before a Portal joins a tailnet.
_Avoid_: Hostname, label

**Assigned Name**:
The unique machine name Tailscale assigns to a Portal. It can differ from the
Portal Name when Tailscale resolves a name collision.
_Avoid_: Portal Name, requested name

**Portal URL**:
The HTTPS address issued for a Portal's Assigned Name on its tailnet.
_Avoid_: Generated URL, requested URL

**Portal Destination**:
The web service a Portal makes available to its tailnet. It is either a Local
App or a Remote App.
_Avoid_: Backend, upstream, target

**Local App**:
An HTTP service on the same Mac that a Portal makes available to its tailnet.
_Avoid_: Localhost app, on-device service

**Remote App**:
An HTTP or HTTPS service running on another host and reachable through the
Mac's normal network. It may be on any non-loopback network the Mac can reach.
_Avoid_: External App, Network App, remote target

**Online Portal**:
A Portal whose Tailscale node is connected to its tailnet. It does not imply
that the Portal is accepting tailnet requests or that the Portal Destination
is reachable or healthy.
_Avoid_: Healthy Portal, available app

**Portal Destination**:
The one configured Local App or Remote App to which a Portal forwards requests.

**Remote App**:
An HTTP or HTTPS service reached through the Mac network that a Portal makes
available to its tailnet. A Remote App is configured by scheme, host, and port.

**Enabled Portal**:
A Portal whose desired state is to remain connected whenever Portico is
running.
_Avoid_: Active Portal, automatic Portal

**Stopped Portal**:
A Portal that remains disconnected until the user explicitly starts it.
_Avoid_: Disabled Portal, paused Portal

**Removing Portal**:
A Portal whose user-confirmed local removal has not yet finished. It cannot
run and remains recorded only so Portico can complete crash-safe cleanup.
_Avoid_: Deleted Portal, pending deletion
