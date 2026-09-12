// swift-tools-version: 5.9

import PackageDescription

let package = Package(
    name: "native_internal",
    platforms: [
        .macOS("10.15")
    ],
    products: [
        .library(name: "native-internal", type: .static, targets: ["native_internal"])
    ],
    dependencies: [
        .package(name: "FlutterFramework", path: "../FlutterFramework")
    ],
    targets: [
        .binaryTarget(
            name: "libgodash",
            path: "Frameworks/native_internal.xcframework"
        ),
        .target(
            name: "native_internal",
            dependencies: [
                "libgodash",
                .product(name: "FlutterFramework", package: "FlutterFramework")
            ],
            linkerSettings: [
                // The Go exports are looked up at runtime with dlsym
                // (DynamicLibrary.process()), so the linker must not dead-strip
                // them: force each symbol the Dart bridge resolves.
                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_InitializeDartAPI"]),
                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_RPC"]),
                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_CallSync"]),
                .unsafeFlags(["-Xlinker", "-u", "-Xlinker", "_FreeBytesContainer"])//GODASH_HOT_LINKER_FLAGS
            ]
        )
    ]
)
