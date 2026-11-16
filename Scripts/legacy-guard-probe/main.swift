// The legacy-guard probe: compiled with one release's PortalStore.swift and
// PortalConfiguration.swift by Scripts/verify-legacy-guard.sh, it drives that
// reader against the state root in its argument and prints one line per call:
// "<call> ok" or "<call> error <domain> <code>".
import Foundation

let store = PortalStore(rootURL: URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true))

func report(_ call: String, _ body: () throws -> Void) {
    do {
        try body()
        print("\(call) ok")
    } catch {
        let error = error as NSError
        print("\(call) error \(error.domain) \(error.code)")
    }
}

report("prepareForStartup") { _ = try store.prepareForStartup() }
report("loadInstallation") { _ = try store.loadInstallation() }
report("save") { try store.save(InstallationRecord()) }
