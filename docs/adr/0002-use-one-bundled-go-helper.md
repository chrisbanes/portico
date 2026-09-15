# Run tsnet in one bundled Go helper

The native Swift app launches and supervises one signed bundled Go helper as a
same-user child process. It contains all active Portal servers and communicates
with Swift through versioned JSON Lines over standard input and output. It is
not a privileged helper. Swift owns product configuration while the helper owns
runtime state and opaque tsnet identity files; this avoids a Go-to-C ABI and
the lifecycle and signing overhead of one process per Portal.
