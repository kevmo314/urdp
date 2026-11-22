import SwiftUI
import UIKit

@main
struct URDPApp: App {
    @StateObject private var store = ServerStore()

    var body: some Scene {
        WindowGroup {
            ServersView()
                .environmentObject(store)
        }
    }
}

/// Presents a stream over whatever is on screen. Streams are UIKit
/// (GCEventViewController) so controller input bypasses SwiftUI focus.
@MainActor
func presentStream(api: API, source: Source) {
    guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
          var top = scene.windows.first(where: \.isKeyWindow)?.rootViewController else { return }
    while let next = top.presentedViewController { top = next }
    top.present(StreamViewController(api: api, source: source), animated: true)
}

// MARK: - servers

struct ServersView: View {
    @EnvironmentObject private var store: ServerStore
    @State private var editing: Server?
    @State private var adding = false
    @State private var notice: String?
    @State private var path: [Server] = []

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                VStack(alignment: .leading, spacing: 40) {
                    if store.servers.isEmpty {
                        VStack(alignment: .leading, spacing: 16) {
                            Text("Stream a desktop from a computer running urdp.")
                                .font(.title3)
                            Text("Run urdp on the host (it serves port 8000), then add it here by name or address.")
                                .foregroundStyle(.secondary)
                        }
                        .padding(.bottom, 20)
                    }
                    LazyVGrid(columns: Array(repeating: GridItem(.fixed(400), spacing: 48), count: 4), alignment: .leading, spacing: 48) {
                        ForEach(store.servers) { server in
                            NavigationLink(value: server) {
                                Tile(symbol: "desktopcomputer", title: server.name, subtitle: server.url.host() ?? server.url.absoluteString)
                            }
                            .buttonStyle(.card)
                            .contextMenu {
                                Button("Edit") { editing = server }
                                Button("Delete", role: .destructive) { store.remove(server) }
                            }
                        }
                        Button { adding = true } label: {
                            Tile(symbol: "plus", title: "Add Server", subtitle: "Host name or address")
                        }
                        .buttonStyle(.card)
                    }
                    if !store.servers.isEmpty {
                        Text("Press and hold a server to edit or delete it.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.horizontal, 80)
                .padding(.vertical, 40)
            }
            .navigationTitle("urdp")
            .navigationDestination(for: Server.self) { SourcesView(server: $0) }
        }
        .sheet(isPresented: $adding) { ServerForm(server: nil) }
        .sheet(item: $editing) { ServerForm(server: $0) }
        .onReceive(NotificationCenter.default.publisher(for: .urdpNotice)) { notice = $0.object as? String }
        .alert(notice ?? "", isPresented: Binding(get: { notice != nil }, set: { if !$0 { notice = nil } })) {
            Button("OK") { notice = nil }
        }
        #if DEBUG
        .task { autoConnect() }
        #endif
    }

    #if DEBUG
    /// For testing: `-urdpServer http://host:8000 [-urdpSource display1]`.
    private func autoConnect() {
        if UserDefaults.standard.bool(forKey: "urdpBrowse"), let first = store.servers.first { path = [first] }
        guard let s = UserDefaults.standard.string(forKey: "urdpServer"), let url = Server.parse(s) else { return }
        let source = UserDefaults.standard.string(forKey: "urdpSource") ?? ""
        let api = API(server: Server(name: url.host() ?? s, url: url,
                                     password: UserDefaults.standard.string(forKey: "urdpPassword") ?? ""))
        Task {
            try? await Task.sleep(for: .seconds(1))
            presentStream(api: api, source: Source(id: source, kind: .desktop, title: source.isEmpty ? "Entire desktop" : source,
                                                   subtitle: "", width: 0, height: 0))
        }
    }
    #endif
}

struct Tile: View {
    var symbol: String?
    var image: UIImage?
    var title: String
    var subtitle: String

    var body: some View {
        VStack(spacing: 14) {
            Group {
                if let image {
                    Image(uiImage: image).resizable().interpolation(.high).scaledToFit()
                } else if let symbol {
                    Image(systemName: symbol).resizable().scaledToFit().padding(8)
                }
            }
            .frame(width: 96, height: 96)
            Text(title)
                .font(.headline)
                .lineLimit(2)
                .multilineTextAlignment(.center)
            Text(subtitle)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .padding(24)
        .frame(width: 400, height: 280)
    }
}

struct ServerForm: View {
    @EnvironmentObject private var store: ServerStore
    @Environment(\.dismiss) private var dismiss
    let server: Server?

    @State private var address = ""
    @State private var name = ""
    @State private var password = ""
    @State private var checking = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Address (e.g. desk.local or 192.168.1.20:8000)", text: $address)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    TextField("Name (optional)", text: $name)
                    SecureField("Password (if the host uses -password)", text: $password)
                } footer: {
                    Text("Without a port, urdp's default 8000 is used. Start urdp with -hosts <name> if the host rejects the address you use here.")
                }
                if let error {
                    Section { Text(error).foregroundStyle(.red) }
                }
                Section {
                    Button(checking ? "Connecting…" : "Save") { Task { await save() } }
                        .disabled(checking || address.isEmpty)
                    if server != nil {
                        Button("Delete", role: .destructive) {
                            if let server { store.remove(server) }
                            dismiss()
                        }
                    }
                }
            }
            .navigationTitle(server == nil ? "Add Server" : "Edit Server")
        }
        .onAppear {
            if let server {
                address = server.url.absoluteString
                name = server.name
                password = server.password
            }
        }
    }

    private func save() async {
        guard let url = Server.parse(address) else {
            error = "That isn't a valid address."
            return
        }
        checking = true
        defer { checking = false }
        var s = server ?? Server(name: "", url: url)
        s.url = url
        s.password = password
        do {
            let info = try await API(server: s).info()
            s.name = name.isEmpty ? info.hostname : name
        } catch {
            self.error = "Couldn't reach urdp at \(url.absoluteString): \(error.localizedDescription)"
            return
        }
        store.save(s)
        dismiss()
    }
}

// MARK: - sources

struct SourcesView: View {
    let server: Server
    @State private var sources: [Source] = []
    @State private var icons: [String: UIImage] = [:]
    @State private var error: String?
    @State private var loading = true

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 40) {
                if let error {
                    VStack(alignment: .leading, spacing: 20) {
                        Text(error).foregroundStyle(.secondary)
                        Button("Retry") { Task { await load() } }
                    }
                } else if loading && sources.isEmpty {
                    ProgressView()
                }
                section("Desktop", sources.filter { $0.kind != .window })
                section("Windows", sources.filter { $0.kind == .window })
            }
            .padding(.horizontal, 80)
            .padding(.vertical, 40)
        }
        .navigationTitle(server.name)
        .task { await load() }
        .refreshable { await load() }
    }

    @ViewBuilder
    private func section(_ title: String, _ items: [Source]) -> some View {
        if !items.isEmpty {
            Text(title).font(.title3).foregroundStyle(.secondary)
            LazyVGrid(columns: Array(repeating: GridItem(.fixed(400), spacing: 48), count: 4), alignment: .leading, spacing: 48) {
                ForEach(items) { s in
                    Button {
                        presentStream(api: API(server: server), source: s)
                    } label: {
                        Tile(symbol: s.kind == .window ? "macwindow" : s.kind == .display ? "display" : "rectangle.on.rectangle",
                             image: icons[s.id], title: s.title, subtitle: s.subtitle)
                    }
                    .buttonStyle(.card)
                }
            }
        }
    }

    private func load() async {
        loading = true
        defer { loading = false }
        let api = API(server: server)
        do {
            sources = try await api.sources()
            error = nil
        } catch {
            self.error = "Couldn't load sources: \(error.localizedDescription)"
            return
        }
        await withTaskGroup(of: (String, UIImage?).self) { group in
            for s in sources {
                guard let url = s.icon, icons[s.id] == nil else { continue }
                group.addTask { (s.id, await api.icon(url).flatMap(UIImage.init(data:))) }
            }
            for await (id, img) in group {
                if let img { icons[id] = img }
            }
        }
    }
}
