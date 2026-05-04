import 'package:flutter/material.dart';

import '../state/app_state.dart';

class MessageBubble extends StatelessWidget {
  const MessageBubble({super.key, required this.message});

  final ChatMessage message;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final isUser = message.role == Role.user;
    final isSystem = message.role == Role.system;

    final Color bg;
    final Color fg;
    Alignment align;
    BorderRadius radius;

    if (isSystem) {
      bg = theme.colorScheme.surfaceContainerHighest;
      fg = theme.colorScheme.onSurfaceVariant;
      align = Alignment.center;
      radius = BorderRadius.circular(6);
    } else if (isUser) {
      bg = theme.colorScheme.primary;
      fg = theme.colorScheme.onPrimary;
      align = Alignment.centerRight;
      radius = const BorderRadius.only(
        topLeft: Radius.circular(14),
        topRight: Radius.circular(14),
        bottomLeft: Radius.circular(14),
        bottomRight: Radius.circular(2),
      );
    } else {
      bg = theme.colorScheme.surfaceContainer;
      fg = theme.colorScheme.onSurface;
      align = Alignment.centerLeft;
      radius = const BorderRadius.only(
        topLeft: Radius.circular(14),
        topRight: Radius.circular(14),
        bottomRight: Radius.circular(14),
        bottomLeft: Radius.circular(2),
      );
    }

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4, horizontal: 8),
      child: Align(
        alignment: align,
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 720),
          child: Container(
            padding:
                const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
            decoration: BoxDecoration(color: bg, borderRadius: radius),
            child: SelectableText(
              message.text,
              style: TextStyle(
                color: fg,
                fontSize: isSystem ? 12 : 14,
                fontStyle: isSystem ? FontStyle.italic : FontStyle.normal,
                height: 1.35,
              ),
            ),
          ),
        ),
      ),
    );
  }
}
