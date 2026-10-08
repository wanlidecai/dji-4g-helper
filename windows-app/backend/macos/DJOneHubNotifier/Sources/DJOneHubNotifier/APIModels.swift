import Foundation

struct CallRecord: Codable, Equatable, Sendable {
    let id: String
    let index: Int
    let direction: String
    let state: String
    let number: String?
    let startedAt: Date
    let updatedAt: Date
    let endedAt: Date?
    let missed: Bool

    enum CodingKeys: String, CodingKey {
        case id, index, direction, state, number, missed
        case startedAt = "started_at"
        case updatedAt = "updated_at"
        case endedAt = "ended_at"
    }
}

struct CallStatus: Codable, Sendable {
    let active: CallRecord?
    let history: [CallRecord]?
    let polling: Bool
    let pollIntervalSeconds: Int
    let lastPollError: String

    enum CodingKeys: String, CodingKey {
        case active, history, polling
        case pollIntervalSeconds = "poll_interval_s"
        case lastPollError = "last_poll_error"
    }
}

struct SMSMessage: Codable, Equatable, Sendable {
    let sender: String
    let content: String
    let code: String?
    let timestamp: Date

    var identity: String {
        "\(sender)\u{0}\(timestamp.timeIntervalSince1970)\u{0}\(content)"
    }
}

struct RejectResponse: Codable, Sendable {
    let rejected: Bool
}

enum APIError: LocalizedError {
    case invalidResponse
    case http(Int)

    var errorDescription: String? {
        switch self {
        case .invalidResponse:
            return "DJOneHub 返回了无效响应"
        case let .http(status):
            return "DJOneHub 请求失败（HTTP \(status)）"
        }
    }
}

struct DJOneHubAPI: Sendable {
    let baseURL: URL

    private static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()

    func callStatus() async throws -> CallStatus {
        try await get(path: "api/calls/status")
    }

    func messages() async throws -> [SMSMessage] {
        try await get(path: "api/sms")
    }

    func rejectCall() async throws -> RejectResponse {
        var request = URLRequest(url: baseURL.appending(path: "api/calls/reject"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode)
        }
        return try Self.decoder.decode(RejectResponse.self, from: data)
    }

    private func get<T: Decodable>(path: String) async throws -> T {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.timeoutInterval = 5
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode)
        }
        return try Self.decoder.decode(T.self, from: data)
    }
}
