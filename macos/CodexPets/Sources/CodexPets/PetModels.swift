import Foundation

enum PetSource: String, Codable {
    case app
    case petdex
    case codex

    var label: String {
        switch self {
        case .app: return "Imported"
        case .petdex: return "Petdex"
        case .codex: return "Codex"
        }
    }
}

struct PetAnimationState: Codable, Equatable {
    let id: String
    let label: String
    let row: Int
    let frames: Int
    let duration: TimeInterval

    static let defaults: [PetAnimationState] = [
        .init(id: "idle", label: "Idle", row: 0, frames: 6, duration: 0.16),
        .init(id: "running-right", label: "Run Right", row: 1, frames: 8, duration: 0.12),
        .init(id: "running-left", label: "Run Left", row: 2, frames: 8, duration: 0.12),
        .init(id: "waving", label: "Wave", row: 3, frames: 4, duration: 0.14),
        .init(id: "jumping", label: "Jump", row: 4, frames: 5, duration: 0.14),
        .init(id: "failed", label: "Failed", row: 5, frames: 8, duration: 0.14),
        .init(id: "waiting", label: "Waiting", row: 6, frames: 6, duration: 0.15),
        .init(id: "running", label: "Running", row: 7, frames: 6, duration: 0.12),
        .init(id: "review", label: "Review", row: 8, frames: 6, duration: 0.15),
    ]

    static func named(_ id: String, from states: [PetAnimationState]) -> PetAnimationState {
        states.first { $0.id == id } ?? states.first ?? defaults[0]
    }
}

struct PetPackage: Codable, Equatable {
    let slug: String
    let displayName: String
    let detail: String
    let kind: String
    let source: PetSource
    let directory: URL
    let spritesheet: URL
    let frameWidth: Int
    let frameHeight: Int
    let states: [PetAnimationState]

    var id: String {
        "\(source.rawValue):\(slug):\(directory.path)"
    }
}

enum PetLoadError: Error, LocalizedError {
    case missingManifest
    case missingSpritesheet
    case invalidManifest

    var errorDescription: String? {
        switch self {
        case .missingManifest: return "pet.json was not found."
        case .missingSpritesheet: return "spritesheet.webp or spritesheet.png was not found."
        case .invalidManifest: return "pet.json could not be parsed."
        }
    }
}

final class PetStore {
    private let fileManager: FileManager
    let appSupport: URL
    let importedPetsRoot: URL
    let runtimeRoot: URL

    init(appSupport explicitAppSupport: URL? = nil, fileManager: FileManager = .default) {
        self.fileManager = fileManager
        let base = explicitAppSupport ?? fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("CodexPets", isDirectory: true)
        self.appSupport = base
        self.importedPetsRoot = base.appendingPathComponent("Pets", isDirectory: true)
        self.runtimeRoot = base.appendingPathComponent("Runtime", isDirectory: true)
        try? fileManager.createDirectory(at: importedPetsRoot, withIntermediateDirectories: true)
        try? fileManager.createDirectory(at: runtimeRoot, withIntermediateDirectories: true)
    }

    func bundledDefaultPetDirectory(slug: String) -> URL? {
        var candidates: [URL] = []
        if let resourceURL = Bundle.main.resourceURL {
            candidates.append(
                resourceURL
                    .appendingPathComponent("PetdexBrowser", isDirectory: true)
                    .appendingPathComponent("prebundled-pets", isDirectory: true)
                    .appendingPathComponent("pets", isDirectory: true)
                    .appendingPathComponent(slug, isDirectory: true)
            )
        }
        candidates.append(
            URL(fileURLWithPath: fileManager.currentDirectoryPath)
                .appendingPathComponent("prebundled-pets", isDirectory: true)
                .appendingPathComponent("pets", isDirectory: true)
                .appendingPathComponent(slug, isDirectory: true)
        )
        return candidates.first { fileManager.fileExists(atPath: $0.appendingPathComponent("pet.json").path) }
    }
}

// petPackage maps a daemon-provided ref onto the render-side package. The
// daemon already parsed and validated pet.json; native code never does.
func petPackage(from ref: DaemonPetRef) -> PetPackage? {
    guard
        let slug = ref.slug, !slug.isEmpty,
        let path = ref.path, !path.isEmpty,
        let spriteName = ref.spritesheetPath, !spriteName.isEmpty,
        let source = PetSource(rawValue: ref.source)
    else {
        return nil
    }
    let directory = URL(fileURLWithPath: path, isDirectory: true)
    return PetPackage(
        slug: slug,
        displayName: ref.displayName,
        detail: ref.description ?? "Animated Codex-compatible pet.",
        kind: ref.kind ?? "pet",
        source: source,
        directory: directory,
        spritesheet: directory.appendingPathComponent(spriteName),
        frameWidth: ref.frameWidth ?? 192,
        frameHeight: ref.frameHeight ?? 208,
        states: PetAnimationState.defaults
    )
}
