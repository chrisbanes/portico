# Portico

Portico gives a local web app a durable private doorway on a Tailscale
tailnet. This glossary defines the product language used in the app,
documentation, and code.

## Language

**Portal**:
A durable private doorway from an HTTPS tailnet address to one Local App. A
Portal retains its identity until it is removed.
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

**Local App**:
An HTTP service on the same Mac that a Portal makes available to its tailnet.
_Avoid_: Backend, upstream, target

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
