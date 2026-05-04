import 'dart:async';
import 'dart:convert';
import 'package:http/http.dart' as http;

import '../models/chat_models.dart';

/// Thin HTTP client for the ext-llm-webadapter (port 9555).
class WebadapterClient {
  WebadapterClient({required this.baseUrl, http.Client? httpClient})
      : _http = httpClient ?? http.Client();

  /// Trailing slashes are stripped so callers can pass the base URL with or
  /// without one.
  final String baseUrl;
  final http.Client _http;

  String get _root =>
      baseUrl.endsWith("/") ? baseUrl.substring(0, baseUrl.length - 1) : baseUrl;

  Map<String, String> _headers(String? token) {
    final h = {
      "Content-Type": "application/json",
      "Accept": "application/json",
    };
    if (token != null && token.isNotEmpty) {
      h["Authorization"] = "Bearer $token";
    }
    return h;
  }

  // SendMessage waits for the gateway pipeline to finish, which can take a
  // long time. The generic timeout therefore needs to be much larger than for
  // plain login/init calls.
  static const _shortTimeout = Duration(seconds: 30);
  static const _longTimeout = Duration(seconds: 300);

  Future<LoginResponse> login(String username, String password) async {
    final req = LoginRequest(username: username, password: password);
    final res = await _http
        .post(
          Uri.parse("$_root/api/v1/auth/login"),
          headers: _headers(null),
          body: jsonEncode(req.toJson()),
        )
        .timeout(_shortTimeout);
    final body = _decode(res);
    if (res.statusCode != 200) {
      throw ApiError(
        statusCode: res.statusCode,
        code: (body["code"] as String?) ?? "",
        message: (body["message"] as String?) ?? "login failed",
      );
    }
    return LoginResponse.fromJson(body);
  }

  Future<InitChatResponse> initChat(String token) async {
    final res = await _http
        .post(
          Uri.parse("$_root/api/v1/chat/init"),
          headers: _headers(token),
          body: jsonEncode(<String, dynamic>{}),
        )
        .timeout(_shortTimeout);
    final body = _decode(res);
    if (res.statusCode != 200) {
      throw ApiError(
        statusCode: res.statusCode,
        code: (body["code"] as String?) ?? "",
        message: (body["message"] as String?) ?? "init_chat failed",
      );
    }
    return InitChatResponse.fromJson(body);
  }

  Future<SendMessageResponse> sendMessage(
    String token,
    SendMessageRequest req,
  ) async {
    final res = await _http
        .post(
          Uri.parse("$_root/api/v1/chat/message"),
          headers: _headers(token),
          body: jsonEncode(req.toJson()),
        )
        .timeout(_longTimeout);
    final body = _decode(res);
    if (res.statusCode != 200) {
      throw ApiError(
        statusCode: res.statusCode,
        code: (body["code"] as String?) ?? "",
        message: (body["message"] as String?) ?? "send_message failed",
      );
    }
    return SendMessageResponse.fromJson(body);
  }

  Map<String, dynamic> _decode(http.Response res) {
    if (res.body.isEmpty) return <String, dynamic>{};
    try {
      final decoded = jsonDecode(res.body);
      if (decoded is Map<String, dynamic>) return decoded;
      return <String, dynamic>{"_raw": decoded};
    } catch (_) {
      return <String, dynamic>{"message": res.body};
    }
  }

  void close() => _http.close();
}
