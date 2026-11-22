import Foundation

/// A saved urdp host.
struct Server: Codable, Identifiable, Hashable {
    var id = UUID()
    var name: String
    /// Base URL, e.g. http://desk.local:8000
    var url: URL
    var password: String = ""

    /// Builds a base URL from what was typed: "desk", "desk:9000" or a full URL.
    static func parse(_ text: String) -> URL? {
        var s = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty else { return nil }
        if !s.contains("://") { s = "http://" + s }
        guard var c = URLComponents(string: s), let host = c.host, !host.isEmpty else { return nil }
        if c.port == nil && c.scheme == "http" { c.port = 8000 }
        c.path = ""
        c.query = nil
        c.fragment = nil
        return c.url
    }
}

/// One streamable source: the whole desktop, a display or a window.
struct Source: Identifiable, Hashable {
    enum Kind: Hashable { case desktop, display, window }
    /// The stream path without the leading slash: "", "display1", "0x1a00007".
    var id: String
    var kind: Kind
    var title: String
    var subtitle: String
    var width: Int
    var height: Int
    var icon: URL?
}

struct Info: Decodable {
    var hostname: String
    var os: String
    var codecs: [String]?
}

private struct DisplayJSON: Decodable {
    var index: Int
    var name: String
    var width: Int
    var height: Int
    var primary: Bool?
    var path: String
}

private struct WindowJSON: Decodable {
    var title: String
    var app: String?
    var width: Int
    var height: Int
    var path: String
}

struct APIError: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

/// Talks to a urdp server's HTTP API.
struct API {
    var server: Server

    private func request(_ path: String, query: [URLQueryItem] = []) -> URLRequest {
        var c = URLComponents(url: server.url, resolvingAgainstBaseURL: false)!
        c.path = path
        if !query.isEmpty { c.queryItems = query }
        var r = URLRequest(url: c.url!, timeoutInterval: 10)
        if !server.password.isEmpty {
            // urdp accepts any username with its password.
            let token = Data("urdp:\(server.password)".utf8).base64EncodedString()
            r.setValue("Basic \(token)", forHTTPHeaderField: "Authorization")
        }
        return r
    }

    private func check(_ data: Data, _ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { return }
        switch http.statusCode {
        case 200..<300: return
        case 401: throw APIError(message: "Wrong password")
        case 404: throw APIError(message: "Not found")
        default:
            let body = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            throw APIError(message: "HTTP \(http.statusCode)\(body.isEmpty ? "" : ": \(body)")")
        }
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        let (data, resp) = try await URLSession.shared.data(for: request(path))
        try check(data, resp)
        return try JSONDecoder().decode(T.self, from: data)
    }

    func info() async throws -> Info { try await get("/api/info") }

    /// Every source, in the web page's order: desktop, displays, windows by app.
    func sources() async throws -> [Source] {
        let displays: [DisplayJSON] = try await get("/api/displays")
        let windows: [WindowJSON] = (try? await get("/api/windows")) ?? []
        var out: [Source] = []
        let totalW = displays.map(\.width).reduce(0, +)
        let maxH = displays.map(\.height).max() ?? 0
        out.append(Source(id: "", kind: .desktop, title: "Entire desktop",
                          subtitle: displays.count > 1 ? "\(displays.count) displays" : "\(totalW)×\(maxH)",
                          width: totalW, height: maxH))
        if displays.count > 1 {
            for d in displays {
                out.append(Source(id: trim(d.path), kind: .display, title: d.name.isEmpty ? "Display \(d.index)" : d.name,
                                  subtitle: "\(d.width)×\(d.height)\(d.primary == true ? " · primary" : "")",
                                  width: d.width, height: d.height))
            }
        }
        for w in windows.sorted(by: { ($0.app ?? "", $0.title) < ($1.app ?? "", $1.title) }) {
            var icon = URLComponents(url: server.url, resolvingAgainstBaseURL: false)!
            icon.path = "/api/windows\(w.path)/icon"
            icon.queryItems = [URLQueryItem(name: "size", value: "128")]
            out.append(Source(id: trim(w.path), kind: .window, title: w.title.isEmpty ? "(untitled)" : w.title,
                              subtitle: [w.app, "\(w.width)×\(w.height)"].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · "),
                              width: w.width, height: w.height, icon: icon.url))
        }
        return out
    }

    func icon(_ url: URL) async -> Data? {
        var r = request(url.path, query: [URLQueryItem(name: "size", value: "128")])
        r.timeoutInterval = 5
        guard let (data, resp) = try? await URLSession.shared.data(for: r), (try? check(data, resp)) != nil else { return nil }
        return data
    }

    /// Posts an SDP offer for a source and returns the answer SDP.
    /// Throws SourceGone if the source no longer exists.
    func offer(source: String, sdp: String, codec: String = "") async throws -> String {
        var r = request("/api/offer", query: [URLQueryItem(name: "source", value: source),
                                               URLQueryItem(name: "codec", value: codec)])
        r.httpMethod = "POST"
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: ["type": "offer", "sdp": sdp])
        let (data, resp) = try await URLSession.shared.data(for: r)
        if (resp as? HTTPURLResponse)?.statusCode == 404 { throw SourceGone() }
        try check(data, resp)
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let answer = obj["sdp"] as? String else {
            throw APIError(message: "Bad answer from server")
        }
        return answer
    }

    private func trim(_ path: String) -> String {
        path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    }
}

struct SourceGone: LocalizedError {
    var errorDescription: String? { "The source was closed" }
}

/// Saved servers, kept in UserDefaults (passwords in the keychain).
@MainActor
final class ServerStore: ObservableObject {
    @Published private(set) var servers: [Server] = []
    private let key = "servers"

    init() {
        if let data = UserDefaults.standard.data(forKey: key),
           let saved = try? JSONDecoder().decode([Server].self, from: data) {
            servers = saved.map { var s = $0; s.password = Keychain.get(s.id.uuidString) ?? ""; return s }
        }
    }

    func save(_ server: Server) {
        if let i = servers.firstIndex(where: { $0.id == server.id }) {
            servers[i] = server
        } else {
            servers.append(server)
        }
        Keychain.set(server.id.uuidString, server.password)
        persist()
    }

    func remove(_ server: Server) {
        servers.removeAll { $0.id == server.id }
        Keychain.set(server.id.uuidString, "")
        persist()
    }

    private func persist() {
        let stripped = servers.map { var s = $0; s.password = ""; return s }
        UserDefaults.standard.set(try? JSONEncoder().encode(stripped), forKey: key)
    }
}

enum Keychain {
    private static func query(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: "urdp",
         kSecAttrAccount as String: account]
    }

    static func get(_ account: String) -> String? {
        var q = query(account)
        q[kSecReturnData as String] = true
        var out: AnyObject?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func set(_ account: String, _ value: String) {
        SecItemDelete(query(account) as CFDictionary)
        guard !value.isEmpty else { return }
        var q = query(account)
        q[kSecValueData as String] = Data(value.utf8)
        SecItemAdd(q as CFDictionary, nil)
    }
}
