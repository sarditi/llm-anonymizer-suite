import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:provider/provider.dart';

import '../state/app_state.dart';

/// Composer for the active session. The input is disabled while a request is
/// in flight on this session, satisfying the "cannot type until response is
/// received" requirement.
class MessageInput extends StatefulWidget {
  const MessageInput({super.key});

  @override
  State<MessageInput> createState() => _MessageInputState();
}

class _MessageInputState extends State<MessageInput> {
  final _ctl = TextEditingController();
  final _focus = FocusNode();

  @override
  void dispose() {
    _ctl.dispose();
    _focus.dispose();
    super.dispose();
  }

  Future<void> _send(AppState st, ChatSession s) async {
    final text = _ctl.text;
    if (text.trim().isEmpty) return;
    _ctl.clear();
    await st.sendOnActive(text);
    if (mounted) _focus.requestFocus();
  }

  @override
  Widget build(BuildContext context) {
    final st = context.watch<AppState>();
    final s = st.activeSession;
    final theme = Theme.of(context);

    final disabled = s == null || !s.isReady || s.inFlight;
    String placeholder;
    if (s == null) {
      placeholder = "Open a chat to start";
    } else if (!s.isReady) {
      placeholder = "Waiting for the chat to initialise…";
    } else if (s.inFlight) {
      placeholder = "Waiting for the assistant…";
    } else {
      placeholder = "Type a message and press Enter";
    }

    return Container(
      decoration: BoxDecoration(
        color: theme.colorScheme.surface,
        border: Border(
          top: BorderSide(color: theme.colorScheme.outlineVariant),
        ),
      ),
      padding: const EdgeInsets.fromLTRB(12, 8, 12, 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        mainAxisSize: MainAxisSize.min,
        children: [
          if (s?.lastError != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 6),
              child: Text(
                "error: ${s!.lastError}",
                style: TextStyle(color: theme.colorScheme.error, fontSize: 12),
              ),
            ),
          Row(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Expanded(
                child: Shortcuts(
                  shortcuts: <ShortcutActivator, Intent>{
                    LogicalKeySet(LogicalKeyboardKey.enter):
                        const _SubmitIntent(),
                    LogicalKeySet(
                            LogicalKeyboardKey.shift, LogicalKeyboardKey.enter):
                        const _NewlineIntent(),
                  },
                  child: Actions(
                    actions: <Type, Action<Intent>>{
                      _SubmitIntent: CallbackAction<_SubmitIntent>(
                        onInvoke: (_) {
                          if (!disabled && s != null) {
                            _send(st, s);
                          }
                          return null;
                        },
                      ),
                      _NewlineIntent: CallbackAction<_NewlineIntent>(
                        onInvoke: (_) {
                          final sel = _ctl.selection;
                          final t = _ctl.text;
                          final left = sel.start < 0 ? t : t.substring(0, sel.start);
                          final right =
                              sel.end < 0 ? "" : t.substring(sel.end);
                          _ctl.text = "$left\n$right";
                          _ctl.selection = TextSelection.collapsed(
                            offset: left.length + 1,
                          );
                          return null;
                        },
                      ),
                    },
                    child: TextField(
                      controller: _ctl,
                      focusNode: _focus,
                      enabled: !disabled,
                      minLines: 1,
                      maxLines: 6,
                      textInputAction: TextInputAction.newline,
                      decoration: InputDecoration(
                        hintText: placeholder,
                        filled: true,
                        fillColor: theme.colorScheme.surfaceContainerHighest,
                        border: OutlineInputBorder(
                          borderRadius: BorderRadius.circular(10),
                          borderSide: BorderSide.none,
                        ),
                        contentPadding: const EdgeInsets.symmetric(
                          horizontal: 14,
                          vertical: 12,
                        ),
                      ),
                    ),
                  ),
                ),
              ),
              const SizedBox(width: 8),
              SizedBox(
                width: 44,
                height: 44,
                child: FilledButton(
                  onPressed: disabled || s == null ? null : () => _send(st, s),
                  style: FilledButton.styleFrom(
                    padding: EdgeInsets.zero,
                    shape: RoundedRectangleBorder(
                      borderRadius: BorderRadius.circular(10),
                    ),
                  ),
                  child: s != null && s.inFlight
                      ? const SizedBox(
                          width: 18,
                          height: 18,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.send_rounded, size: 18),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _SubmitIntent extends Intent {
  const _SubmitIntent();
}

class _NewlineIntent extends Intent {
  const _NewlineIntent();
}
