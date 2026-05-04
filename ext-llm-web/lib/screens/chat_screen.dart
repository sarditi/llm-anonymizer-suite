import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';
import '../widgets/chat_list.dart';
import '../widgets/message_bubble.dart';
import '../widgets/message_input.dart';

class ChatScreen extends StatefulWidget {
  const ChatScreen({super.key});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final _scrollCtl = ScrollController();
  bool _autoCreateScheduled = false;

  @override
  void initState() {
    super.initState();
    // If the user landed on the chat screen with no sessions, kick off the
    // first one automatically.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final st = context.read<AppState>();
      if (!_autoCreateScheduled && st.sessions.isEmpty) {
        _autoCreateScheduled = true;
        st.startNewSession();
      }
    });
  }

  @override
  void dispose() {
    _scrollCtl.dispose();
    super.dispose();
  }

  void _scrollToEnd() {
    if (!_scrollCtl.hasClients) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_scrollCtl.hasClients) return;
      _scrollCtl.animateTo(
        _scrollCtl.position.maxScrollExtent,
        duration: const Duration(milliseconds: 180),
        curve: Curves.easeOut,
      );
    });
  }

  @override
  Widget build(BuildContext context) {
    final st = context.watch<AppState>();
    final theme = Theme.of(context);
    final session = st.activeSession;

    _scrollToEnd();

    return Scaffold(
      body: Row(
        children: [
          const ChatList(),
          Expanded(
            child: Column(
              children: [
                _Header(session: session),
                const Divider(height: 1),
                Expanded(
                  child: session == null
                      ? _EmptyState(theme: theme)
                      : ListView.builder(
                          controller: _scrollCtl,
                          padding: const EdgeInsets.symmetric(
                              vertical: 12, horizontal: 6),
                          itemCount: session.messages.length,
                          itemBuilder: (ctx, i) =>
                              MessageBubble(message: session.messages[i]),
                        ),
                ),
                const MessageInput(),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

class _Header extends StatelessWidget {
  const _Header({required this.session});
  final ChatSession? session;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final title = session?.title ?? "No active chat";
    final sub = session == null
        ? "Open or create a chat from the left"
        : (session!.isReady
            ? "ready • next_key=${session!.nextRequestKey!.substring(0, 8)}…"
            : "initialising…");

    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      color: theme.colorScheme.surface,
      child: Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title,
                    style: theme.textTheme.titleMedium
                        ?.copyWith(fontWeight: FontWeight.w600),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis),
                Text(sub,
                    style: theme.textTheme.bodySmall
                        ?.copyWith(color: theme.colorScheme.outline)),
              ],
            ),
          ),
          if (session != null && session!.inFlight)
            const SizedBox(
              width: 16,
              height: 16,
              child: CircularProgressIndicator(strokeWidth: 2),
            ),
        ],
      ),
    );
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.theme});
  final ThemeData theme;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(Icons.forum_outlined,
              size: 56, color: theme.colorScheme.outline),
          const SizedBox(height: 12),
          Text("No active chat",
              style: theme.textTheme.titleMedium
                  ?.copyWith(color: theme.colorScheme.outline)),
        ],
      ),
    );
  }
}
