// Wire-level models. Field names match the JSON tags exposed by the
// ext-llm-webadapter (see ext-llm-webadapter/internal/models/models.go and
// ext-llm-common/models/models.go).

class LoginRequest {
  final String username;
  final String password;
  LoginRequest({required this.username, required this.password});
  Map<String, dynamic> toJson() =>
      {"username": username, "password": password};
}

class LoginResponse {
  final String token;
  final String tokenType;
  final int expiresAt;
  final String username;
  LoginResponse({
    required this.token,
    required this.tokenType,
    required this.expiresAt,
    required this.username,
  });
  factory LoginResponse.fromJson(Map<String, dynamic> j) => LoginResponse(
        token: j["token"] as String,
        tokenType: (j["token_type"] as String?) ?? "Bearer",
        expiresAt: (j["expires_at"] as num).toInt(),
        username: j["username"] as String,
      );
}

class InitChatResponse {
  final String status;
  final String chatId;
  final String setNextReqKey;
  final String message;
  InitChatResponse({
    required this.status,
    required this.chatId,
    required this.setNextReqKey,
    required this.message,
  });
  factory InitChatResponse.fromJson(Map<String, dynamic> j) => InitChatResponse(
        status: (j["status"] as String?) ?? "",
        chatId: (j["chat_id"] as String?) ?? "",
        setNextReqKey: (j["set_next_req_key"] as String?) ?? "",
        message: (j["message"] as String?) ?? "",
      );
}

class Content {
  final String inputText;
  final bool thought;
  final String explanations;
  const Content(
      {this.inputText = "", this.thought = false, this.explanations = ""});

  Map<String, dynamic> toJson() {
    final m = <String, dynamic>{};
    if (inputText.isNotEmpty) m["input_text"] = inputText;
    if (thought) m["thought"] = true;
    if (explanations.isNotEmpty) m["explanations"] = explanations;
    return m;
  }

  factory Content.fromJson(Map<String, dynamic> j) => Content(
        inputText: (j["input_text"] as String?) ?? "",
        thought: (j["thought"] as bool?) ?? false,
        explanations: (j["explanations"] as String?) ?? "",
      );
}

class SendMessageRequest {
  final String chatId;
  final String requestKey;
  final Content content;
  SendMessageRequest({
    required this.chatId,
    required this.requestKey,
    required this.content,
  });
  // Field names match the webadapter's SendMessageRequest tags
  // (`internal_chat_id`, `request_key`, `content`).
  Map<String, dynamic> toJson() => {
        "internal_chat_id": chatId,
        "request_key": requestKey,
        "content": content.toJson(),
      };
}

class SendMessageResponse {
  final String status;
  final String chatId;
  final String setNextReqKey;
  final Content content;
  final String message;
  SendMessageResponse({
    required this.status,
    required this.chatId,
    required this.setNextReqKey,
    required this.content,
    required this.message,
  });
  factory SendMessageResponse.fromJson(Map<String, dynamic> j) =>
      SendMessageResponse(
        status: (j["status"] as String?) ?? "",
        chatId: (j["internal_chat_id"] as String?) ?? "",
        setNextReqKey: (j["set_next_req_key"] as String?) ?? "",
        content: j["content"] is Map<String, dynamic>
            ? Content.fromJson(j["content"] as Map<String, dynamic>)
            : const Content(),
        message: (j["message"] as String?) ?? "",
      );
}

class ApiError implements Exception {
  final int? statusCode;
  final String code;
  final String message;
  ApiError({this.statusCode, this.code = "", required this.message});
  @override
  String toString() =>
      "ApiError(status=$statusCode, code=$code, message=$message)";
}
