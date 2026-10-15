import Foundation
import HTTPTypes
import OpenAPIRuntime
import Testing
@testable import PulseKit

@Suite struct DateTranscoderTests {
    let transcoder = LenientISO8601DateTranscoder()

    @Test(arguments: [
        "2026-10-16T04:03:11Z",
        "2026-10-16T04:03:11.5Z",
        "2026-10-16T04:03:11.123Z",
        "2026-10-16T04:03:11.123456789Z",
        "2026-10-16T00:03:11.123456789-04:00",
    ])
    func decodesGoTimestamps(_ string: String) throws {
        let date = try transcoder.decode(string)
        #expect(abs(date.timeIntervalSince1970 - 1_792_123_391) < 1)
    }

    @Test func keepsMilliseconds() throws {
        let date = try transcoder.decode("2026-10-16T04:03:11.123456789Z")
        #expect(abs(date.timeIntervalSince1970 - 1_792_123_391.123) < 0.0005)
    }

    @Test func rejectsGarbage() {
        #expect(throws: (any Error).self) { try transcoder.decode("yesterday") }
    }
}

@Suite struct EnrollmentTests {
    @Test func parsesTheLinkTheCLIPrints() throws {
        // url.Values.Encode() percent-encodes the URL.
        let link = URL(string: "pulse://enroll?token=pls_abc-DEF_123&url=https%3A%2F%2Fpulse.example.com")!
        let e = try Enrollment(link: link)
        #expect(e.serverURL.absoluteString == "https://pulse.example.com")
        #expect(e.token == "pls_abc-DEF_123")
    }

    @Test func rejectsOtherLinks() {
        #expect(throws: Enrollment.Invalid.notAnEnrollmentLink) {
            try Enrollment(link: URL(string: "https://pulse.example.com/enroll?url=x&token=y")!)
        }
    }

    @Test func normalisesTypedAddresses() throws {
        let e = try Enrollment(server: "  pulse.example.com/ ", token: " pls_x\n")
        #expect(e.serverURL.absoluteString == "https://pulse.example.com")
        #expect(e.token == "pls_x")
    }

    @Test func requiresHTTPSExceptLocally() throws {
        #expect(throws: Enrollment.Invalid.insecureServerURL) {
            try Enrollment(server: "http://pulse.example.com", token: "pls_x")
        }
        _ = try Enrollment(server: "http://127.0.0.1:18080", token: "pls_x")
    }

    @Test(arguments: ["", "pls_", "abc", "pls_has space", "pls_ünïcode"])
    func rejectsBadTokens(_ token: String) {
        #expect(throws: Enrollment.Invalid.badToken) {
            try Enrollment(server: "pulse.example.com", token: token)
        }
    }
}

@Suite struct LogLineTests {
    @Test func splitsTimestamp() {
        let line = LogLine(id: 0, raw: "2026-10-15T04:12:01.114567890Z Starting immich-ml")
        #expect(line.timestamp != nil)
        #expect(line.text == "Starting immich-ml")
        #expect(line.level == nil)
    }

    @Test func keepsLinesWithoutTimestamp() {
        let line = LogLine(id: 0, raw: "plain text here")
        #expect(line.timestamp == nil)
        #expect(line.text == "plain text here")
    }

    @Test(arguments: [
        ("[ERROR] worker 17 exited", LogLine.Level.error),
        ("level=error msg=\"boom\"", .error),
        (#"{"level":"error","msg":"x"}"#, .error),
        ("E1015 04:12:01.114 1 reflector.go:1] failed", .error),
        ("WARN memory 3.8 GiB of 4.0 GiB", .warning),
        ("level=warn msg=slow", .warning),
        ("W1015 04:12:01.114 1 x.go:1] slow", .warning),
    ])
    func detectsLevels(_ text: String, _ level: LogLine.Level) {
        #expect(LogLine(id: 0, raw: text).level == level)
    }

    @Test(arguments: ["errors=0 processed", "Terror Twilight", "no warnings today", "INFO ok"])
    func ignoresLookalikes(_ text: String) {
        #expect(LogLine(id: 0, raw: text).level == nil)
    }
}

struct MockTransport: ClientTransport {
    var handler: @Sendable (HTTPRequest) async throws -> (HTTPResponse, HTTPBody?)

    func send(
        _ request: HTTPRequest, body: HTTPBody?, baseURL: URL, operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        try await handler(request)
    }
}

@Suite struct ClientTests {
    let enrollment = Enrollment(serverURL: URL(string: "https://pulse.example.com")!, token: "pls_secret")

    func client(status: HTTPResponse.Status = .ok, contentType: String = "application/json", body: String)
        -> PulseClient
    {
        PulseClient(enrollment: enrollment, transport: MockTransport { request in
            #expect(request.headerFields[.authorization] == "Bearer pls_secret")
            var response = HTTPResponse(status: status)
            response.headerFields[.contentType] = contentType
            return (response, HTTPBody(body))
        })
    }

    // Shaped like the Go handler's output: nanosecond timestamps, omitempty
    // fields absent, empty arrays present.
    static let overviewJSON = """
    {"generatedAt":"2026-10-15T16:20:00.123456789Z",
     "nodes":[{"name":"node1","ready":true,"schedulable":true,"roles":["control-plane"],
               "kubeletVersion":"v1.33.4+k3s1","kernelVersion":"6.12.4","osImage":"NixOS 25.05",
               "internalIP":"10.0.0.11","pressure":[],"podCount":24,
               "cpu":{"used":1500,"capacity":4000,"percent":37.5},
               "createdAt":"2026-09-01T10:00:00Z"}],
     "workloads":{"total":25,"healthy":24},
     "pods":{"total":61,"running":60,"problems":1},
     "problems":[{"severity":"warning","kind":"pod","namespace":"media","name":"immich-ml-7c9d",
                  "title":"CrashLoopBackOff","detail":"14 restarts"}],
     "hosts":[{"host":"node2","lastUpgrade":{"id":"01JABC","ts":"2026-10-15T04:03:11.5Z",
               "source":"host:node2","kind":"nixos-upgrade","severity":"info",
               "title":"node2 upgraded to generation 412","key":"nixos-upgrade:node2"}}],
     "recentEvents":[]}
    """

    @Test func decodesOverview() async throws {
        let overview = try await client(body: Self.overviewJSON).overview()
        #expect(overview.nodes.first?.cpu?.percent == 37.5)
        #expect(overview.nodes.first?.memory == nil)
        #expect(overview.problems.first?.severity == .warning)
        #expect(overview.hosts.first?.lastUpgrade?.hostName == "node2")
        #expect(overview.workloads.healthy == 24)
    }

    @Test func mapsUnauthorized() async throws {
        let c = client(status: .unauthorized, body: #"{"error":"unauthorized"}"#)
        await #expect(throws: PulseError.unauthorized) { try await c.overview() }
    }

    @Test func surfacesBadRequestMessage() async throws {
        let c = client(status: .badRequest, body: #"{"error":"container is required"}"#)
        await #expect(throws: PulseError.badRequest("container is required")) {
            try await c.logs(namespace: "media", pod: "x", container: nil)
        }
    }

    @Test func parsesLogs() async throws {
        let text = "2026-10-15T04:12:01.1Z one\n2026-10-15T04:12:02.2Z [ERROR] two\n"
        let lines = try await client(contentType: "text/plain", body: text)
            .logs(namespace: "media", pod: "x", container: "c")
        #expect(lines.map(\.text) == ["one", "[ERROR] two"])
        #expect(lines.last?.level == .error)
    }

    @Test func followsServerSentEvents() async throws {
        let sse = "data: first\n\n: ping\n\ndata: second\n\nevent: end\ndata: \n\n"
        let stream = client(contentType: "text/event-stream", body: sse)
            .followLogs(namespace: "media", pod: "x", container: "c")
        var got: [PulseClient.LogStreamEvent] = []
        for try await event in stream { got.append(event) }
        #expect(got == [.line("first"), .line("second"), .ended])
    }

    @Test func pagesEvents() async throws {
        let body = """
        {"items":[{"id":"01JB","ts":"2026-10-15T04:03:11Z","source":"k8s","kind":"k8s-event",
                   "severity":"critical","title":"x","key":"k"}],"nextCursor":"01JB"}
        """
        let page = try await client(body: body).events()
        #expect(page.items.count == 1)
        #expect(page.nextCursor == "01JB")
        #expect(page.items[0].severity > .warning)
    }
}

@Suite struct PulseLinkTests {
    @Test func parsesClickActions() {
        #expect(PulseLink(URL(string: "pulse://events")!) == .timeline)
        #expect(PulseLink(URL(string: "pulse://events/")!) == .timeline)
        #expect(PulseLink(URL(string: "pulse://events/01JABCDEFGHJKMNPQRSTVWXYZ0")!) == .event(id: "01JABCDEFGHJKMNPQRSTVWXYZ0"))
        #expect(PulseLink(URL(string: "pulse://enroll?url=x&token=y")!) == .enroll)
    }

    @Test(arguments: [
        "https://pulse.example.com/events", "pulse://nodes/node1", "pulse://events/a/b", "pulse://events/..%2Fx",
    ])
    func rejectsOthers(_ link: String) {
        #expect(PulseLink(URL(string: link)!) == nil)
    }
}

@Suite struct EventLookupTests {
    let enrollment = Enrollment(serverURL: URL(string: "https://pulse.example.com")!, token: "pls_secret")

    func client(status: HTTPResponse.Status = .ok, body: String) -> PulseClient {
        PulseClient(enrollment: enrollment, transport: MockTransport { request in
            #expect(request.path == "/v1/events/01JB")
            var response = HTTPResponse(status: status)
            response.headerFields[.contentType] = "application/json"
            return (response, HTTPBody(body))
        })
    }

    @Test func returnsTheCurrentVersion() async throws {
        // A recurring mirrored event resolves to its newest id.
        let body = #"{"id":"01JC","ts":"2026-10-15T04:03:11Z","source":"k8s","kind":"pod-crashloop","severity":"warning","title":"t","key":"k"}"#
        #expect(try await client(body: body).event(id: "01JB").id == "01JC")
    }

    @Test func mapsNotFound() async throws {
        await #expect(throws: PulseError.notFound) {
            try await client(status: .notFound, body: #"{"error":"not found"}"#).event(id: "01JB")
        }
    }
}
