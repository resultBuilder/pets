import Cocoa
import Foundation

if CommandLine.arguments.contains("--validate") {
    let store = PetStore()
    print("Codex Pets native app OK")
    print("Imported root: \(store.importedPetsRoot.path)")
    print("Runtime root: \(store.runtimeRoot.path)")
    exit(0)
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
