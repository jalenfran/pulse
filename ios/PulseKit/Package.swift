// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "PulseKit",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "PulseKit", targets: ["PulseKit"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-openapi-generator", from: "1.6.0"),
        .package(url: "https://github.com/apple/swift-openapi-runtime", from: "1.7.0"),
        .package(url: "https://github.com/apple/swift-openapi-urlsession", from: "1.0.2"),
        .package(url: "https://github.com/apple/swift-http-types", from: "1.0.0"),
    ],
    targets: [
        // Client and types generated from api/openapi.yaml (symlinked in).
        .target(
            name: "PulseAPI",
            dependencies: [
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
            ],
            plugins: ["PulseAPIGenerator"]
        ),
        // Wraps swift-openapi-generator; see the plugin for why.
        .plugin(
            name: "PulseAPIGenerator",
            capability: .buildTool(),
            dependencies: [
                .product(name: "swift-openapi-generator", package: "swift-openapi-generator"),
            ]
        ),
        .target(
            name: "PulseKit",
            dependencies: [
                "PulseAPI",
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
                .product(name: "OpenAPIURLSession", package: "swift-openapi-urlsession"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
            ]
        ),
        .testTarget(
            name: "PulseKitTests",
            dependencies: [
                "PulseKit",
                "PulseAPI",
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
            ]
        ),
    ]
)
