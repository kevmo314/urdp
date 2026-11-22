import GameController

/// Maps HID keyboard usages (what GCKeyCode wraps) to the KeyboardEvent.code
/// names urdp's input channel expects.
enum KeyCodes {
    static func code(for key: GCKeyCode) -> String? {
        table[Int(key.rawValue)]
    }

    private static let table: [Int: String] = {
        var t: [Int: String] = [:]
        // a-z: 0x04...0x1D
        for (i, c) in "ABCDEFGHIJKLMNOPQRSTUVWXYZ".enumerated() { t[0x04 + i] = "Key\(c)" }
        // 1-9, 0: 0x1E...0x27
        for i in 0..<9 { t[0x1E + i] = "Digit\(i + 1)" }
        t[0x27] = "Digit0"
        let named: [Int: String] = [
            0x28: "Enter", 0x29: "Escape", 0x2A: "Backspace", 0x2B: "Tab", 0x2C: "Space",
            0x2D: "Minus", 0x2E: "Equal", 0x2F: "BracketLeft", 0x30: "BracketRight",
            0x31: "Backslash", 0x32: "IntlHash", 0x33: "Semicolon", 0x34: "Quote",
            0x35: "Backquote", 0x36: "Comma", 0x37: "Period", 0x38: "Slash", 0x39: "CapsLock",
            0x46: "PrintScreen", 0x47: "ScrollLock", 0x48: "Pause", 0x49: "Insert",
            0x4A: "Home", 0x4B: "PageUp", 0x4C: "Delete", 0x4D: "End", 0x4E: "PageDown",
            0x4F: "ArrowRight", 0x50: "ArrowLeft", 0x51: "ArrowDown", 0x52: "ArrowUp",
            0x53: "NumLock", 0x54: "NumpadDivide", 0x55: "NumpadMultiply", 0x56: "NumpadSubtract",
            0x57: "NumpadAdd", 0x58: "NumpadEnter", 0x63: "NumpadDecimal",
            0x64: "IntlBackslash", 0x65: "ContextMenu", 0x67: "NumpadEqual",
            0x87: "IntlRo", 0x89: "IntlYen",
            0xE0: "ControlLeft", 0xE1: "ShiftLeft", 0xE2: "AltLeft", 0xE3: "MetaLeft",
            0xE4: "ControlRight", 0xE5: "ShiftRight", 0xE6: "AltRight", 0xE7: "MetaRight",
        ]
        t.merge(named) { $1 }
        // F1-F12: 0x3A...0x45
        for i in 0..<12 { t[0x3A + i] = "F\(i + 1)" }
        // Numpad 1-9, 0: 0x59...0x62
        for i in 0..<9 { t[0x59 + i] = "Numpad\(i + 1)" }
        t[0x62] = "Numpad0"
        return t
    }()

    /// US-layout key presses for typing an ASCII character: (code, shift).
    static func type(_ ch: Character) -> (String, Bool)? {
        if ch.isLetter, ch.isASCII {
            return ("Key\(ch.uppercased())", ch.isUppercase)
        }
        if let d = ch.wholeNumberValue, ch.isASCII { return ("Digit\(d)", false) }
        let map: [Character: (String, Bool)] = [
            " ": ("Space", false), "\n": ("Enter", false), "\t": ("Tab", false),
            "-": ("Minus", false), "_": ("Minus", true), "=": ("Equal", false), "+": ("Equal", true),
            "[": ("BracketLeft", false), "{": ("BracketLeft", true),
            "]": ("BracketRight", false), "}": ("BracketRight", true),
            "\\": ("Backslash", false), "|": ("Backslash", true),
            ";": ("Semicolon", false), ":": ("Semicolon", true),
            "'": ("Quote", false), "\"": ("Quote", true),
            "`": ("Backquote", false), "~": ("Backquote", true),
            ",": ("Comma", false), "<": ("Comma", true), ".": ("Period", false), ">": ("Period", true),
            "/": ("Slash", false), "?": ("Slash", true),
            "!": ("Digit1", true), "@": ("Digit2", true), "#": ("Digit3", true), "$": ("Digit4", true),
            "%": ("Digit5", true), "^": ("Digit6", true), "&": ("Digit7", true), "*": ("Digit8", true),
            "(": ("Digit9", true), ")": ("Digit0", true),
        ]
        return map[ch]
    }
}
