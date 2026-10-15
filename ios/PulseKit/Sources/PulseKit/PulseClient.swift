import Foundation
import HTTPTypes
import OpenAPIRuntime
import OpenAPIURLSession

public enum PulseError: LocalizedError, Equatable {
    case unauthorized
    case notFound
    case badRequest(String?)
    case unexpectedStatus(Int)

    public var errorDescription: String? {
        switch self {
        case .unauthorized: "The server didn't accept this device's token. It may have been revoked."
        case .notFound: "It no longer exists."
        case .badRequest(let message): message ?? "The server rejected the request."
        case .unexpectedStatus(let code): "The server answered with HTTP \(code)."
        }
    }
}

/// Typed access to one Pulse server. Wraps the generated client, adds the
/// bearer token, and turns non-200 responses into `PulseError`.
public struct PulseClient: Sendable {
    public let enrollment: Enrollment
    private let client: Client

    public init(enrollment: Enrollment, transport: any ClientTransport = URLSessionTransport()) {
        self.enrollment = enrollment
        client = Client(
            serverURL: enrollment.serverURL,
            configuration: Configuration(dateTranscoder: LenientISO8601DateTranscoder()),
            transport: transport,
            middlewares: [BearerToken(token: enrollment.token)]
        )
    }

    public func overview() async throws -> Overview {
        switch try await client.getOverview() {
        case .ok(let r): return try r.body.json
        case .unauthorized: throw PulseError.unauthorized
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public func nodes() async throws -> [NodeSummary] {
        switch try await client.listNodes() {
        case .ok(let r): return try r.body.json.items
        case .unauthorized: throw PulseError.unauthorized
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public func node(_ name: String) async throws -> NodeDetail {
        switch try await client.getNode(path: .init(name: name)) {
        case .ok(let r): return try r.body.json
        case .unauthorized: throw PulseError.unauthorized
        case .notFound: throw PulseError.notFound
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public func workloads(namespace: String? = nil) async throws -> [Workload] {
        switch try await client.listWorkloads(query: .init(namespace: namespace)) {
        case .ok(let r): return try r.body.json.items
        case .unauthorized: throw PulseError.unauthorized
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public func pods(namespace: String? = nil, node: String? = nil) async throws -> [PodSummary] {
        switch try await client.listPods(query: .init(namespace: namespace, node: node)) {
        case .ok(let r): return try r.body.json.items
        case .unauthorized: throw PulseError.unauthorized
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public func pod(namespace: String, name: String) async throws -> PodDetail {
        switch try await client.getPod(path: .init(namespace: namespace, pod: name)) {
        case .ok(let r): return try r.body.json
        case .unauthorized: throw PulseError.unauthorized
        case .notFound: throw PulseError.notFound
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    /// The last `tail` lines, with Kubernetes timestamps.
    public func logs(
        namespace: String, pod: String, container: String?, previous: Bool = false, tail: Int = 500
    ) async throws -> [LogLine] {
        let out = try await client.getPodLogs(
            path: .init(namespace: namespace, pod: pod),
            query: .init(container: container, tail: tail, timestamps: true, previous: previous)
        )
        switch out {
        case .ok(let r):
            let text = try await String(collecting: r.body.plainText, upTo: 10 << 20)
            return text.split(separator: "\n", omittingEmptySubsequences: false)
                .enumerated()
                .compactMap { i, line in line.isEmpty ? nil : LogLine(id: i, raw: String(line)) }
        case .badRequest(let r): throw PulseError.badRequest(try? r.body.json.error)
        case .unauthorized: throw PulseError.unauthorized
        case .notFound: throw PulseError.notFound
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }

    public enum LogStreamEvent: Sendable, Equatable {
        case line(String)
        /// The container exited and the server closed the stream.
        case ended
    }

    /// Follows a container's log. Starts with the last `tail` lines.
    /// Cancelling the consuming task closes the connection.
    public func followLogs(
        namespace: String, pod: String, container: String?, tail: Int = 200
    ) -> AsyncThrowingStream<LogStreamEvent, any Error> {
        let client = client
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let out = try await client.streamPodLogs(
                        path: .init(namespace: namespace, pod: pod),
                        query: .init(container: container, tail: tail, timestamps: true)
                    )
                    switch out {
                    case .ok(let r):
                        for try await event in try r.body.textEventStream.asDecodedServerSentEvents() {
                            if event.event == "end" {
                                continuation.yield(.ended)
                                break
                            }
                            if let data = event.data { continuation.yield(.line(data)) }
                        }
                        continuation.finish()
                    case .badRequest(let r): throw PulseError.badRequest(try? r.body.json.error)
                    case .unauthorized: throw PulseError.unauthorized
                    case .notFound: throw PulseError.notFound
                    case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
                    }
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public struct EventPage: Sendable {
        public var items: [Event]
        public var nextCursor: String?
    }

    public func events(
        before: String? = nil, minSeverity: Severity? = nil, source: String? = nil, limit: Int = 50
    ) async throws -> EventPage {
        let out = try await client.listEvents(
            query: .init(before: before, minSeverity: minSeverity, source: source, limit: limit)
        )
        switch out {
        case .ok(let r):
            let page = try r.body.json
            return EventPage(items: page.items, nextCursor: page.nextCursor)
        case .badRequest(let r): throw PulseError.badRequest(try? r.body.json.error)
        case .unauthorized: throw PulseError.unauthorized
        case .undocumented(let code, _): throw PulseError.unexpectedStatus(code)
        }
    }
}

extension PulseError {
    /// Unwraps the runtime's `ClientError` so the UI shows the cause
    /// ("The Internet connection appears to be offline.") rather than the
    /// generator's wrapper text.
    public static func underlying(_ error: any Error) -> any Error {
        if let error = error as? ClientError { return error.underlyingError }
        return error
    }
}

struct BearerToken: ClientMiddleware {
    let token: String

    func intercept(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String,
        next: @Sendable (HTTPRequest, HTTPBody?, URL) async throws -> (HTTPResponse, HTTPBody?)
    ) async throws -> (HTTPResponse, HTTPBody?) {
        var request = request
        request.headerFields[.authorization] = "Bearer \(token)"
        return try await next(request, body, baseURL)
    }
}
