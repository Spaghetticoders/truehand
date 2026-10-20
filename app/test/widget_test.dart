import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:truehand_app/api.dart';
import 'package:truehand_app/board_page.dart';
import 'package:truehand_app/strings.dart';
import 'package:truehand_app/task_page.dart';
import 'package:truehand_app/theme.dart';

const _plan = '# T-1\n\n## Goal\ng\n';

http.Client fakeServer({
  required List<String> approvals,
  List<String>? planShas,
}) => MockClient((r) async {
  final p = r.url.path;
  Object body;
  if (p == '/api/repos') {
    body = [
      {
        'name': 'demo',
        'root': '/x/demo',
        'counts': {'planned': 1, 'building': 1},
      },
    ];
  } else if (p == '/api/repos/demo/tasks') {
    body = [
      {
        'id': 'T-1',
        'title': 'Login screen',
        'state': 'planned',
        'active': true,
        'missing': ['human_approval'],
        'updated': '2026-10-20T10:00:00Z',
      },
      {
        'id': 'T-2',
        'title': 'Dark mode',
        'state': 'building',
        'active': false,
        'missing': ['test_proof', 'eyes'],
        'updated': '2026-10-20T10:00:00Z',
      },
    ];
  } else if (p == '/api/repos/demo/tasks/T-1') {
    body = {
      'view': {
        'task': {
          'id': 'T-1',
          'title': 'Login screen',
          'state': 'planned',
          'updated': '2026-10-20T10:00:00Z',
        },
        'active': true,
        'next_gate': {
          'target': 'approved',
          'pass': false,
          'items': [
            {
              'check': 'plan_valid',
              'status': 'pass',
              'message': 'plan.md has all required sections',
            },
            {
              'check': 'human_approval',
              'status': 'fail',
              'message': 'waiting for a human',
            },
          ],
        },
        'evidence': [
          {
            'id': 'check-1',
            'hand': 'check',
            'verdict': 'fail',
            'summary': 'exited 1',
            'actor': 'claude:abc',
            'fresh': false,
            'intact': true,
            'created': '2026-10-20T10:00:00Z',
            'files': [],
          },
        ],
        'approvals': [],
      },
      'events': [
        {
          'time': '2026-10-20T10:00:00Z',
          'type': 'created',
          'actor': 'claude:abc',
          'to': 'planned',
        },
      ],
    };
  } else if (p == '/api/repos/demo/tasks/T-1/plan') {
    final sha = planShas == null || planShas.isEmpty
        ? 'abc123'
        : planShas.removeAt(0);
    body = {
      'exists': true,
      'markdown': '$_plan<!-- $sha -->',
      'problems': [],
      'sha256': sha,
    };
  } else if (p == '/api/repos/demo/tasks/T-1/approve' && r.method == 'POST') {
    if (r.headers['Authorization'] != 'Bearer tok') {
      return http.Response('{"error":"bad token"}', 401);
    }
    approvals.add((jsonDecode(r.body) as Map)['plan_sha256'] as String);
    body = {'approved': true};
  } else {
    return http.Response('{"error":"not found"}', 404);
  }
  return http.Response(
    jsonEncode(body),
    200,
    headers: {'content-type': 'application/json'},
  );
});

Widget wrap(Widget child) => MaterialApp(theme: truehandTheme(), home: child);

void main() {
  testWidgets('board groups tasks by state and shows missing checks', (
    tester,
  ) async {
    final api = TruehandApi(
      baseUrl: 'http://x',
      client: fakeServer(approvals: []),
    );
    await tester.binding.setSurfaceSize(const Size(1600, 900));
    await tester.pumpWidget(wrap(BoardPage(api: api)));
    await tester.pumpAndSettle();
    expect(find.text('Login screen'), findsOneWidget);
    expect(find.text('Dark mode'), findsOneWidget);
    expect(find.text('PLANNED  1'), findsOneWidget);
    expect(find.text('BUILDING  1'), findsOneWidget);
    expect(find.text('test_proof'), findsOneWidget);
    expect(find.text(S.active), findsOneWidget);
  });

  testWidgets('approving sends the sha of the plan that was shown', (
    tester,
  ) async {
    final approvals = <String>[];
    final api = TruehandApi(
      baseUrl: 'http://x',
      token: 'tok',
      client: fakeServer(approvals: approvals),
    );
    await tester.binding.setSurfaceSize(const Size(1200, 2400));
    await tester.pumpWidget(wrap(TaskPage(api: api, repo: 'demo', id: 'T-1')));
    await tester.pumpAndSettle();
    expect(find.text('waiting for a human'), findsOneWidget);
    expect(find.text(S.stale), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, S.approve));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, S.approve).last);
    await tester.pumpAndSettle();
    expect(approvals, ['abc123']);
  });

  testWidgets('without a token the approve button is disabled', (tester) async {
    final api = TruehandApi(
      baseUrl: 'http://x',
      client: fakeServer(approvals: []),
    );
    await tester.binding.setSurfaceSize(const Size(1200, 2400));
    await tester.pumpWidget(wrap(TaskPage(api: api, repo: 'demo', id: 'T-1')));
    await tester.pumpAndSettle();
    final btn = tester.widget<FilledButton>(
      find.widgetWithText(FilledButton, S.approve),
    );
    expect(btn.onPressed, isNull);
    expect(find.text(S.approveNeedsToken), findsOneWidget);
  });

  testWidgets('a plan edited while reading is held back until shown', (
    tester,
  ) async {
    final approvals = <String>[];
    final shas = ['v1', 'v2', 'v2'];
    final api = TruehandApi(
      baseUrl: 'http://x',
      token: 'tok',
      client: fakeServer(approvals: approvals, planShas: shas),
    );
    await tester.binding.setSurfaceSize(const Size(1200, 2400));
    final key = GlobalKey();
    await tester.pumpWidget(
      wrap(TaskPage(key: key, api: api, repo: 'demo', id: 'T-1')),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('v1'), findsOneWidget);
    // Simulate a change notification: the page reloads and sees a different plan.
    await (key.currentState as dynamic).reloadForTest();
    await tester.pumpAndSettle();
    expect(find.text(S.planChanged), findsOneWidget);
    expect(find.textContaining('v1'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, S.approve), findsNothing);
    await tester.tap(find.text(S.showNewPlan));
    await tester.pumpAndSettle();
    expect(find.textContaining('v2'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, S.approve));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, S.approve).last);
    await tester.pumpAndSettle();
    expect(approvals, ['v2']);
  });
}
