import 'dart:async';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';

import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:uuid/uuid.dart';

import '../api/webadapter_client.dart';
import '../models/chat_models.dart';

// Minimal interop bindings to read window.EXT_LLM_WEB_CONFIG, which is set by
// web/config.js (and overridden by the static server at runtime).
@JS('EXT_LLM_WEB_CONFIG')
external JSObject? get _runtimeConfig;

/// Roles for messages in the chat history view.
enum Role { user, assistant, system }

class ChatMessage {
  ChatMessage({
    required this.role,
    required this.text,
    DateTime? at,
  }) : at = at ?? DateTime.now();
  final Role role;
  final String text;
  final DateTime at;
}

/// One chat session lives entirely in memory: chat_id, the next request key
/// the webadapter expects, the running message log, and a per-session
/// in-flight flag so the input can be locked until the round-trip completes.
class ChatSession {
  ChatSession({
    required this.id,
    required this.title,
  });

  final String id; // local UUID, not the server chat_id
  String title;

  /// Server-issued chat id (from /chat/init). Null until the first init
  /// completes for this session.
  String? remoteChatId;

  /// Next request key the webadapter expects on /chat/message.
  String? nextRequestKey;

  /// True while a /chat/message request is in flight. The UI uses this to
  /// disable the composer for this session only.
  bool inFlight = false;

  /// Last error message (cleared on next successful turn). Surfaced as a
  /// banner above the input so the user can see what went wrong.
  String? lastError;

  final List<ChatMessage> messages = [];

  bool get isReady => remoteChatId != null && nextRequestKey != null;
}

/// AppState owns the auth token, the currently configured webadapter base URL,
/// and the list of chat sessions.
class AppState extends ChangeNotifier {
  AppState({required this.client});

  WebadapterClient client;

  String? _token;
  String? _username;
  DateTime? _expiresAt;

  String? get token => _token;
  String? get username => _username;
  bool get isAuthenticated =>
      _token != null &&
      _expiresAt != null &&
      _expiresAt!.isAfter(DateTime.now());

  final List<ChatSession> sessions = [];
  ChatSession? _active;
  ChatSession? get activeSession => _active;

  static const _kToken = "ext_llm_web.token";
  static const _kUser = "ext_llm_web.user";
  static const _kExp = "ext_llm_web.exp";

  Future<void> hydrateFromStorage() async {
    final sp = await SharedPreferences.getInstance();
    final t = sp.getString(_kToken);
    final u = sp.getString(_kUser);
    final e = sp.getInt(_kExp);
    if (t != null && u != null && e != null) {
      _token = t;
      _username = u;
      _expiresAt = DateTime.fromMillisecondsSinceEpoch(e * 1000);
    }
    notifyListeners();
  }

  Future<void> login(String username, String password) async {
    final r = await client.login(username, password);
    _token = r.token;
    _username = r.username;
    _expiresAt = DateTime.fromMillisecondsSinceEpoch(r.expiresAt * 1000);
    final sp = await SharedPreferences.getInstance();
    await sp.setString(_kToken, r.token);
    await sp.setString(_kUser, r.username);
    await sp.setInt(_kExp, r.expiresAt);
    notifyListeners();
  }

  Future<void> logout() async {
    _token = null;
    _username = null;
    _expiresAt = null;
    sessions.clear();
    _active = null;
    final sp = await SharedPreferences.getInstance();
    await sp.remove(_kToken);
    await sp.remove(_kUser);
    await sp.remove(_kExp);
    notifyListeners();
  }

  /// Creates a new local chat session and immediately calls /chat/init so the
  /// session is ready to send a message.
  Future<ChatSession> startNewSession() async {
    if (!isAuthenticated) {
      throw StateError("not authenticated");
    }
    final s = ChatSession(
      id: const Uuid().v4(),
      title: "Chat ${sessions.length + 1}",
    );
    sessions.add(s);
    _active = s;
    notifyListeners();

    try {
      final init = await client.initChat(_token!);
      s.remoteChatId = init.chatId;
      s.nextRequestKey = init.setNextReqKey;
      s.messages.add(ChatMessage(
        role: Role.system,
        text: "New chat ready (chat_id=${init.chatId.substring(0, 8)}…)",
      ));
    } on ApiError catch (e) {
      s.lastError = e.message;
      if (e.statusCode == 401) {
        await logout();
      }
    } catch (e) {
      s.lastError = e.toString();
    }
    notifyListeners();
    return s;
  }

  void selectSession(ChatSession s) {
    _active = s;
    notifyListeners();
  }

  void closeSession(ChatSession s) {
    sessions.remove(s);
    if (_active == s) {
      _active = sessions.isNotEmpty ? sessions.last : null;
    }
    notifyListeners();
  }

  /// Sends a user message on the active session and waits for the webadapter
  /// to return the synchronous reply. The session is locked (`inFlight=true`)
  /// for the duration of the call.
  Future<void> sendOnActive(String text) async {
    final s = _active;
    if (s == null) throw StateError("no active session");
    if (!s.isReady) throw StateError("session is not ready");
    if (s.inFlight) return;
    if (text.trim().isEmpty) return;

    s.inFlight = true;
    s.lastError = null;
    s.messages.add(ChatMessage(role: Role.user, text: text));
    if (s.title.startsWith("Chat ") && s.messages.length == 2) {
      // First user turn — adopt a short prefix as a friendlier title.
      final t = text.trim();
      s.title = t.length <= 40 ? t : "${t.substring(0, 40)}…";
    }
    notifyListeners();

    try {
      final resp = await client.sendMessage(
        _token!,
        SendMessageRequest(
          chatId: s.remoteChatId!,
          requestKey: s.nextRequestKey!,
          content: Content(inputText: text),
        ),
      );
      s.nextRequestKey = resp.setNextReqKey;
      s.messages.add(ChatMessage(
        role: Role.assistant,
        text: resp.content.inputText.isNotEmpty
            ? resp.content.inputText
            : "(empty reply)",
      ));
    } on ApiError catch (e) {
      s.lastError = e.message;
      s.messages.add(ChatMessage(
        role: Role.system,
        text: "error: ${e.message}",
      ));
      if (e.statusCode == 401) {
        await logout();
      }
    } catch (e) {
      s.lastError = e.toString();
      s.messages.add(ChatMessage(role: Role.system, text: "error: $e"));
    } finally {
      s.inFlight = false;
      notifyListeners();
    }
  }
}

/// Reads the runtime configuration injected via web/config.js.
class RuntimeConfig {
  static String _readString(String key, String fallback) {
    try {
      final cfg = _runtimeConfig;
      if (cfg == null) return fallback;
      final v = cfg.getProperty(key.toJS);
      if (v == null) return fallback;
      return (v as JSString).toDart;
    } catch (_) {
      return fallback;
    }
  }

  static String webadapterBaseUrl() =>
      _readString("webadapterBaseUrl", "http://localhost:9555");

  static String appTitle() => _readString("appTitle", "ext-llm-web");
}
