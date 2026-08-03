# Run tsnet in one bundled Go helper

The native Swift app supervises one signed Go helper containing all active
Portal servers and communicates with it through versioned JSON Lines over
standard input and output. Swift owns product configuration while the helper
owns runtime state and opaque tsnet identity files; this avoids a Go-to-C ABI
and the lifecycle and signing overhead of one process per Portal.
