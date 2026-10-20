import 'dart:async';

import 'package:flutter/material.dart';

import 'api.dart';
import 'models.dart';
import 'strings.dart';
import 'task_page.dart';
import 'theme.dart';
import 'widgets.dart';

class BoardPage extends StatefulWidget {
  const BoardPage({super.key, required this.api});

  final TruehandApi api;

  @override
  State<BoardPage> createState() => _BoardPageState();
}

class _BoardPageState extends State<BoardPage> {
  List<Repo> _repos = [];
  String? _repo;
  List<TaskSummary> _tasks = [];
  Object? _error;
  StreamSubscription<String>? _sub;

  @override
  void initState() {
    super.initState();
    _load();
    _sub = widget.api.changes().listen((repo) {
      if (repo == '*' || repo == _repo) _load();
    });
  }

  @override
  void dispose() {
    _sub?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final repos = await widget.api.repos();
      final repo = _repo ?? (repos.isEmpty ? null : repos.first.name);
      final tasks = repo == null
          ? <TaskSummary>[]
          : await widget.api.tasks(repo);
      if (!mounted) return;
      setState(() {
        _repos = repos;
        _repo = repo;
        _tasks = tasks;
        _error = null;
      });
    } catch (e) {
      if (mounted) setState(() => _error = e);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text(S.appTitle),
        actions: [
          if (_repos.length > 1)
            Padding(
              padding: const EdgeInsets.only(right: 12),
              child: DropdownButton<String>(
                value: _repo,
                dropdownColor: TruehandColors.navy,
                underline: const SizedBox.shrink(),
                style: const TextStyle(color: TruehandColors.card),
                iconEnabledColor: TruehandColors.card,
                items: [
                  for (final r in _repos)
                    DropdownMenuItem(value: r.name, child: Text(r.name)),
                ],
                onChanged: (v) {
                  setState(() => _repo = v);
                  _load();
                },
              ),
            )
          else if (_repo != null)
            Padding(
              padding: const EdgeInsets.only(right: 16),
              child: Center(child: Text(_repo!)),
            ),
        ],
      ),
      body: _body(),
    );
  }

  Widget _body() {
    if (_error != null && _tasks.isEmpty) {
      return Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              S.disconnected,
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 8),
            Text(
              '$_error',
              style: const TextStyle(color: TruehandColors.textMuted),
            ),
            const SizedBox(height: 16),
            FilledButton(onPressed: _load, child: const Text(S.retry)),
          ],
        ),
      );
    }
    if (_repo == null) return const Center(child: Text(S.noRepos));
    if (_tasks.isEmpty) return const Center(child: Text(S.noTasks));
    return LayoutBuilder(
      builder: (context, c) {
        if (c.maxWidth < 700) {
          return ListView(
            padding: const EdgeInsets.all(16),
            children: [
              for (final s in states)
                if (_tasks.any((t) => t.state == s)) ...[
                  _ColumnHeader(
                    state: s,
                    count: _tasks.where((t) => t.state == s).length,
                  ),
                  for (final t in _tasks.where((t) => t.state == s))
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: _TaskCard(task: t, onTap: () => _open(t)),
                    ),
                  const SizedBox(height: 8),
                ],
            ],
          );
        }
        return SingleChildScrollView(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.all(16),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              for (final s in states)
                Container(
                  width: 260,
                  margin: const EdgeInsets.only(right: 12),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      _ColumnHeader(
                        state: s,
                        count: _tasks.where((t) => t.state == s).length,
                      ),
                      for (final t in _tasks.where((t) => t.state == s))
                        Padding(
                          padding: const EdgeInsets.only(bottom: 8),
                          child: _TaskCard(task: t, onTap: () => _open(t)),
                        ),
                    ],
                  ),
                ),
            ],
          ),
        );
      },
    );
  }

  Future<void> _open(TaskSummary t) async {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => TaskPage(api: widget.api, repo: _repo!, id: t.id),
      ),
    );
    _load();
  }
}

class _ColumnHeader extends StatelessWidget {
  const _ColumnHeader({required this.state, required this.count});

  final String state;
  final int count;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8, left: 2),
      child: Text(
        '${state.toUpperCase()}  $count',
        style: const TextStyle(
          fontSize: 12,
          fontWeight: FontWeight.w700,
          letterSpacing: 0.8,
          color: TruehandColors.textMuted,
        ),
      ),
    );
  }
}

class _TaskCard extends StatelessWidget {
  const _TaskCard({required this.task, required this.onTap});

  final TaskSummary task;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Text(
                    task.id,
                    style: const TextStyle(
                      fontWeight: FontWeight.w700,
                      color: TruehandColors.navy,
                    ),
                  ),
                  const Spacer(),
                  if (task.active)
                    const Pill(
                      S.active,
                      color: TruehandColors.cyan,
                      filled: true,
                    ),
                ],
              ),
              const SizedBox(height: 4),
              Text(task.title, maxLines: 2, overflow: TextOverflow.ellipsis),
              if (task.missing.isNotEmpty) ...[
                const SizedBox(height: 8),
                Wrap(
                  spacing: 4,
                  runSpacing: 4,
                  children: [
                    for (final m in task.missing)
                      Pill(m, color: TruehandColors.fail),
                  ],
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}
