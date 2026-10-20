import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;

import 'models.dart';

class ApiException implements Exception {
  ApiException(this.status, this.message);
  final int status;
  final String message;

  @override
  String toString() => message;
}

class TruehandApi {
  TruehandApi({required this.baseUrl, this.token, http.Client? client})
    : _client = client ?? http.Client();

  final String baseUrl;
  final String? token;
  final http.Client _client;

  Uri _u(String path) => Uri.parse('$baseUrl$path');

  Map<String, String> get _auth => {'Authorization': 'Bearer ${token ?? ''}'};

  Future<dynamic> _get(String path) async {
    final r = await _client.get(_u(path), headers: _auth);
    return _decode(r);
  }

  dynamic _decode(http.Response r) {
    final body = r.body.isEmpty ? null : jsonDecode(utf8.decode(r.bodyBytes));
    if (r.statusCode >= 400) {
      final msg = body is Map && body['error'] is String
          ? body['error'] as String
          : 'HTTP ${r.statusCode}';
      throw ApiException(r.statusCode, msg);
    }
    return body;
  }

  Future<List<Repo>> repos() async => (await _get('/api/repos') as List)
      .map((e) => Repo.fromJson(e as Map<String, dynamic>))
      .toList();

  Future<List<TaskSummary>> tasks(String repo) async =>
      (await _get('/api/repos/${Uri.encodeComponent(repo)}/tasks') as List)
          .map((e) => TaskSummary.fromJson(e as Map<String, dynamic>))
          .toList();

  Future<TaskDetail> task(String repo, String id) async => TaskDetail.fromJson(
    await _get(
          '/api/repos/${Uri.encodeComponent(repo)}/tasks/${Uri.encodeComponent(id)}',
        )
        as Map<String, dynamic>,
  );

  Future<PlanDoc> plan(String repo, String id) async => PlanDoc.fromJson(
    await _get(
          '/api/repos/${Uri.encodeComponent(repo)}/tasks/${Uri.encodeComponent(id)}/plan',
        )
        as Map<String, dynamic>,
  );

  /// Images cannot send headers, so the token travels in the query.
  String fileUrl(String repo, String id, String path) =>
      '$baseUrl/api/repos/${Uri.encodeComponent(repo)}/tasks/${Uri.encodeComponent(id)}/files/$path?token=${Uri.encodeQueryComponent(token ?? '')}';

  Future<void> approve(String repo, String id, String planSha) async {
    final r = await _client.post(
      _u(
        '/api/repos/${Uri.encodeComponent(repo)}/tasks/${Uri.encodeComponent(id)}/approve',
      ),
      headers: {..._auth, 'Content-Type': 'application/json'},
      body: jsonEncode({'plan_sha256': planSha}),
    );
    _decode(r);
  }

  /// Emits a repo name whenever its board changes. Browsers buffer streamed responses in
  /// package:http, so the web build polls instead of reading the event stream.
  Stream<String> changes() {
    if (kIsWeb) {
      return Stream.periodic(const Duration(seconds: 3), (_) => '*');
    }
    late StreamController<String> ctrl;
    http.Client? client;
    Timer? retry;
    var closed = false;
    Future<void> connect() async {
      while (!closed) {
        client = http.Client();
        try {
          final resp = await client!.send(
            http.Request('GET', _u('/api/events'))..headers.addAll(_auth),
          );
          await for (final line
              in resp.stream
                  .transform(utf8.decoder)
                  .transform(const LineSplitter())) {
            if (line.startsWith('data: ')) ctrl.add(line.substring(6));
          }
        } catch (_) {
          // Reconnect below.
        } finally {
          client?.close();
        }
        if (closed) return;
        final wait = Completer<void>();
        retry = Timer(const Duration(seconds: 2), wait.complete);
        await wait.future;
      }
    }

    ctrl = StreamController<String>(
      onListen: connect,
      onCancel: () async {
        closed = true;
        retry?.cancel();
        client?.close();
        await ctrl.close();
      },
    );
    return ctrl.stream;
  }
}
