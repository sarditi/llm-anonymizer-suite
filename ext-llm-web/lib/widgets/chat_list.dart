import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';

class ChatList extends StatelessWidget {
  const ChatList({super.key});

  @override
  Widget build(BuildContext context) {
    final st = context.watch<AppState>();
    final theme = Theme.of(context);

    return Container(
      width: 260,
      decoration: BoxDecoration(
        color: theme.colorScheme.surface,
        border: Border(
          right: BorderSide(color: theme.colorScheme.outlineVariant),
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 14, 14, 6),
            child: Row(
              children: [
                Icon(Icons.chat_bubble_outline,
                    size: 18, color: theme.colorScheme.primary),
                const SizedBox(width: 8),
                Text(
                  "Chats",
                  style: theme.textTheme.titleSmall
                      ?.copyWith(fontWeight: FontWeight.w600),
                ),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 12),
            child: SizedBox(
              width: double.infinity,
              child: FilledButton.tonalIcon(
                onPressed: () => st.startNewSession(),
                icon: const Icon(Icons.add_rounded, size: 18),
                label: const Text("New chat"),
              ),
            ),
          ),
          const SizedBox(height: 8),
          Expanded(
            child: st.sessions.isEmpty
                ? Center(
                    child: Padding(
                      padding: const EdgeInsets.all(20),
                      child: Text(
                        "No chats yet.\nClick \"New chat\" to start.",
                        textAlign: TextAlign.center,
                        style: theme.textTheme.bodySmall
                            ?.copyWith(color: theme.colorScheme.outline),
                      ),
                    ),
                  )
                : ListView.builder(
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    itemCount: st.sessions.length,
                    itemBuilder: (ctx, i) {
                      final s = st.sessions[i];
                      final selected = s == st.activeSession;
                      return Padding(
                        padding: const EdgeInsets.symmetric(
                            horizontal: 8, vertical: 2),
                        child: Material(
                          color: selected
                              ? theme.colorScheme.primaryContainer
                              : Colors.transparent,
                          borderRadius: BorderRadius.circular(8),
                          child: InkWell(
                            borderRadius: BorderRadius.circular(8),
                            onTap: () => st.selectSession(s),
                            child: Padding(
                              padding: const EdgeInsets.symmetric(
                                  horizontal: 10, vertical: 10),
                              child: Row(
                                children: [
                                  Icon(
                                    s.inFlight
                                        ? Icons.hourglass_top_rounded
                                        : Icons.forum_outlined,
                                    size: 16,
                                    color: selected
                                        ? theme.colorScheme.onPrimaryContainer
                                        : theme.colorScheme.outline,
                                  ),
                                  const SizedBox(width: 8),
                                  Expanded(
                                    child: Text(
                                      s.title,
                                      maxLines: 1,
                                      overflow: TextOverflow.ellipsis,
                                      style: TextStyle(
                                        fontWeight: selected
                                            ? FontWeight.w600
                                            : FontWeight.w400,
                                        color: selected
                                            ? theme
                                                .colorScheme.onPrimaryContainer
                                            : null,
                                      ),
                                    ),
                                  ),
                                  IconButton(
                                    tooltip: "Close",
                                    onPressed: () => st.closeSession(s),
                                    icon: const Icon(Icons.close, size: 16),
                                    visualDensity: VisualDensity.compact,
                                    padding: EdgeInsets.zero,
                                    constraints: const BoxConstraints(
                                      minWidth: 24,
                                      minHeight: 24,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                          ),
                        ),
                      );
                    },
                  ),
          ),
          const Divider(height: 1),
          ListTile(
            leading: const Icon(Icons.account_circle_outlined),
            title: Text(st.username ?? "—",
                style: theme.textTheme.bodyMedium),
            subtitle: Text(
              "Sign out",
              style: theme.textTheme.bodySmall
                  ?.copyWith(color: theme.colorScheme.outline),
            ),
            onTap: () => st.logout(),
          ),
        ],
      ),
    );
  }
}
