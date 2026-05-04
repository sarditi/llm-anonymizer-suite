import 'package:flutter/material.dart';
import 'package:provider/provider.dart';

import 'api/webadapter_client.dart';
import 'screens/chat_screen.dart';
import 'screens/login_screen.dart';
import 'state/app_state.dart';

void main() {
  runApp(const ExtLlmWebApp());
}

class ExtLlmWebApp extends StatefulWidget {
  const ExtLlmWebApp({super.key});

  @override
  State<ExtLlmWebApp> createState() => _ExtLlmWebAppState();
}

class _ExtLlmWebAppState extends State<ExtLlmWebApp> {
  late final AppState _state;

  @override
  void initState() {
    super.initState();
    _state = AppState(
      client: WebadapterClient(baseUrl: RuntimeConfig.webadapterBaseUrl()),
    );
    _state.hydrateFromStorage();
  }

  @override
  Widget build(BuildContext context) {
    final lightScheme =
        ColorScheme.fromSeed(seedColor: const Color(0xFF2563EB));
    final darkScheme = ColorScheme.fromSeed(
      seedColor: const Color(0xFF60A5FA),
      brightness: Brightness.dark,
    );

    return ChangeNotifierProvider<AppState>.value(
      value: _state,
      child: MaterialApp(
        title: RuntimeConfig.appTitle(),
        debugShowCheckedModeBanner: false,
        themeMode: ThemeMode.system,
        theme: ThemeData(
          colorScheme: lightScheme,
          useMaterial3: true,
          fontFamily: "Roboto",
          inputDecorationTheme: const InputDecorationTheme(
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        darkTheme: ThemeData(
          colorScheme: darkScheme,
          useMaterial3: true,
          fontFamily: "Roboto",
          inputDecorationTheme: const InputDecorationTheme(
            border: OutlineInputBorder(),
            isDense: true,
          ),
        ),
        home: Consumer<AppState>(
          builder: (ctx, st, _) =>
              st.isAuthenticated ? const ChatScreen() : const LoginScreen(),
        ),
      ),
    );
  }
}
