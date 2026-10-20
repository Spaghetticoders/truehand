import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import 'api.dart';
import 'board_page.dart';
import 'strings.dart';
import 'task_page.dart';
import 'theme.dart';

void main() => runApp(const TruehandApp());

class TruehandApp extends StatelessWidget {
  const TruehandApp({super.key, this.api});

  final TruehandApi? api;

  @override
  Widget build(BuildContext context) {
    final initial = api ?? _fromWebOrigin();
    return MaterialApp(
      title: S.appTitle,
      theme: truehandTheme(),
      debugShowCheckedModeBanner: false,
      home: initial == null
          ? const ConnectPage()
          : _deepLink(initial) ?? BoardPage(api: initial),
    );
  }

  /// `?repo=<name>&task=<id>` opens a task directly, e.g. from a link in a terminal.
  static Widget? _deepLink(TruehandApi api) {
    if (!kIsWeb) return null;
    final q = Uri.base.queryParameters;
    final repo = q['repo'], task = q['task'];
    if (repo == null || task == null) return null;
    return TaskPage(api: api, repo: repo, id: task);
  }

  /// Served by `truehand serve --web`, the app talks to its own origin and reads the token
  /// from the ?token= link the server printed.
  static TruehandApi? _fromWebOrigin() {
    if (!kIsWeb) return null;
    final base = Uri.base;
    if (base.scheme != 'http' && base.scheme != 'https') return null;
    return TruehandApi(
      baseUrl: base.origin,
      token: base.queryParameters['token'],
    );
  }
}

class ConnectPage extends StatefulWidget {
  const ConnectPage({super.key});

  @override
  State<ConnectPage> createState() => _ConnectPageState();
}

class _ConnectPageState extends State<ConnectPage> {
  final _url = TextEditingController(text: 'http://localhost:7878');
  final _token = TextEditingController();

  @override
  void dispose() {
    _url.dispose();
    _token.dispose();
    super.dispose();
  }

  void _connect() {
    final api = TruehandApi(
      baseUrl: _url.text.trim().replaceAll(RegExp(r'/+$'), ''),
      token: _token.text.trim(),
    );
    Navigator.of(context).pushReplacement(
      MaterialPageRoute<void>(builder: (_) => BoardPage(api: api)),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text(S.appTitle)),
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 420),
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                const Text(
                  S.connectHelp,
                  style: TextStyle(
                    fontFamily: 'monospace',
                    color: TruehandColors.textMuted,
                  ),
                ),
                const SizedBox(height: 24),
                TextField(
                  controller: _url,
                  decoration: const InputDecoration(labelText: S.serverUrl),
                ),
                const SizedBox(height: 12),
                TextField(
                  controller: _token,
                  obscureText: true,
                  decoration: const InputDecoration(
                    labelText: S.token,
                    helperText: S.tokenHint,
                  ),
                ),
                const SizedBox(height: 24),
                FilledButton(onPressed: _connect, child: const Text(S.connect)),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
