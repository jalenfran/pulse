import Foundation
import PackagePlugin

/// Runs swift-openapi-generator, then rewrites its `public import` lines as
/// plain imports.
///
/// The generator splits types across several files and gives every one the
/// same `public import`s, so Swift 6.4 warns about each that a file doesn't
/// use publicly ([#UnusedImportAccess]). A plain import is implicitly public
/// in this target, so the generated API is unchanged. Swap back to the
/// upstream `OpenAPIGenerator` plugin once it emits only the imports each
/// file needs.
@main struct PulseAPIGenerator: BuildToolPlugin {
    func createBuildCommands(context: PluginContext, target: Target) throws -> [Command] {
        guard let target = target as? SwiftSourceModuleTarget else { return [] }
        func input(_ name: String) throws -> URL {
            guard let file = target.sourceFiles.first(where: { $0.url.lastPathComponent == name }) else {
                throw MissingInput(target: target.name, file: name)
            }
            return file.url
        }
        let doc = try input("openapi.yaml")
        let config = try input("openapi-generator-config.yaml")
        let generator = try context.tool(named: "swift-openapi-generator").url

        let raw = context.pluginWorkDirectoryURL.appending(component: "Raw")
        let out = context.pluginWorkDirectoryURL.appending(component: "GeneratedSources")
        // `--plugin-source build` writes every file, empty if unused, so the
        // list is fixed.
        let files = [
            "Types.swift", "Types+Components.swift", "Types+Operations.swift",
            "Types+Components+Schemas.swift", "Types+Components+Parameters.swift",
            "Types+Components+RequestBodies.swift", "Types+Components+Responses.swift",
            "Types+Components+Headers.swift", "Client.swift", "Server.swift",
        ]
        // No `${…}` or `$(…)`: the build system expands those itself.
        let script = """
            set -e
            "$0" generate "$1" --config "$2" --output-directory "$3" --plugin-source build
            mkdir -p "$4"
            cd "$3"
            for f in *.swift; do
              sed -E 's/^([[:space:]]*(@_spi\\([A-Za-z]+\\) )?)public import /\\1import /' "$f" > "$4/$f"
            done
            """
        return [
            .buildCommand(
                displayName: "Generating PulseAPI from openapi.yaml",
                executable: URL(filePath: "/bin/sh"),
                // Unencoded: under Xcode the tool's path is
                // `/${BUILD_DIR}/${CONFIGURATION}/…`, for the build to expand.
                arguments: ["-c", script] + [generator, doc, config, raw, out].map { $0.path(percentEncoded: false) },
                inputFiles: [doc, config],
                outputFiles: files.map { out.appending(component: $0) }
            ),
        ]
    }
}

struct MissingInput: Error, CustomStringConvertible {
    var target: String
    var file: String
    var description: String { "\(target) has no \(file)" }
}
