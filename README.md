# Portico

Portico gives local web apps stable, private HTTPS addresses on a Tailscale
tailnet. Each Portal is an independent Tailscale node and proxies its HTTPS
traffic to one HTTP service on the local Mac.

Portico is currently in the design and executable-spike phase. There is no
buildable application in the repository yet.

## Documentation

- [Domain language](./CONTEXT.md)
- [MVP specification](./docs/portico-mvp-spec.md)
- [Architectural decisions](./docs/adr/)
- [Preliminary naming check](./docs/naming.md)
- [Roadmap](./docs/roadmap.md)
