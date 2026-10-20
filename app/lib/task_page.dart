import 'dart:async';

import 'package:flutter/material.dart';

import 'api.dart';
import 'models.dart';
import 'strings.dart';
import 'theme.dart';
import 'widgets.dart';

class TaskPage extends StatefulWidget {
  const TaskPage({
    super.key,
    required this.api,
    required this.repo,
    required this.id,
  });

  final TruehandApi api;
  final String repo;
  final String id;

  @override
  State<TaskPage> createState() => _TaskPageState();
}

class _TaskPageState extends State<TaskPage> {
  TaskDetail? _detail;
  // _plan is what the user is reading; a newer version waits in _newPlan until they choose
  // to see it, so an approval always covers the text that was on screen.
  PlanDoc? _plan;
  PlanDoc? _newPlan;
  Object? _error;
  bool _approving = false;
  StreamSubscription<String>? _sub;

  @override
  void initState() {
    super.initState();
    _load();
    _sub = widget.api.changes().listen((repo) {
      if (repo == '*' || repo == widget.repo) {
        _load();
      }
    });
  }

  @override
  void dispose() {
    _sub?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final d = await widget.api.task(widget.repo, widget.id);
      final p = await widget.api.plan(widget.repo, widget.id);
      if (!mounted) return;
      setState(() {
        _detail = d;
        if (_plan == null || _plan!.sha256 == p.sha256) {
          _plan = p;
          _newPlan = null;
        } else {
          _newPlan = p;
        }
        _error = null;
      });
    } catch (e) {
      if (mounted) setState(() => _error = e);
    }
  }

  @visibleForTesting
  Future<void> reloadForTest() => _load();

  bool get _awaitingApproval {
    final g = _detail?.nextGate;
    return _detail?.task.state == 'planned' &&
        g != null &&
        g.items.any((i) => i.check == 'human_approval' && i.status == 'fail') &&
        (_plan?.valid ?? false) &&
        _newPlan == null;
  }

  Future<void> _approve() async {
    final plan = _plan!;
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text(S.approve),
        content: const Text(S.approveConfirm),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text(S.cancel),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text(S.approve),
          ),
        ],
      ),
    );
    if (ok != true || !mounted) return;
    setState(() => _approving = true);
    final messenger = ScaffoldMessenger.of(context);
    try {
      await widget.api.approve(widget.repo, widget.id, plan.sha256);
      messenger.showSnackBar(const SnackBar(content: Text(S.approved)));
    } catch (e) {
      messenger.showSnackBar(SnackBar(content: Text('$e')));
    } finally {
      if (mounted) setState(() => _approving = false);
      _load();
    }
  }

  @override
  Widget build(BuildContext context) {
    final d = _detail;
    return Scaffold(
      appBar: AppBar(
        title: Text(d == null ? widget.id : '${d.task.id} — ${d.task.title}'),
      ),
      body: d == null
          ? Center(
              child: _error == null
                  ? const CircularProgressIndicator()
                  : Text('$_error'),
            )
          : ListView(
              padding: const EdgeInsets.all(16),
              children: [
                StateStepper(current: d.task.state, states: states),
                const SizedBox(height: 16),
                _gate(d),
                const SizedBox(height: 12),
                _planSection(),
                const SizedBox(height: 12),
                _evidence(d),
                const SizedBox(height: 12),
                _history(d),
              ],
            ),
    );
  }

  Widget _gate(TaskDetail d) {
    final g = d.nextGate;
    if (g == null) {
      return const Section(title: S.nextGate, child: Text(S.lastState));
    }
    return Section(
      title: '${S.nextGate}: ${g.target}',
      trailing: Pill(
        g.pass ? S.gateOpen : S.gateClosed,
        color: g.pass ? TruehandColors.pass : TruehandColors.fail,
        filled: true,
      ),
      child: Column(
        children: [
          if (g.items.isEmpty)
            const Align(
              alignment: Alignment.centerLeft,
              child: Text(
                S.noRules,
                style: TextStyle(color: TruehandColors.textMuted),
              ),
            ),
          for (final it in g.items)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(
                    statusIcon(it.status),
                    size: 18,
                    color: statusColor(it.status),
                  ),
                  const SizedBox(width: 8),
                  SizedBox(
                    width: 170,
                    child: Text(
                      it.check,
                      style: const TextStyle(fontWeight: FontWeight.w600),
                    ),
                  ),
                  Expanded(
                    child: Text(
                      it.message,
                      style: const TextStyle(color: TruehandColors.textMuted),
                    ),
                  ),
                ],
              ),
            ),
        ],
      ),
    );
  }

  Widget _planSection() {
    final p = _plan;
    final canApprove = _awaitingApproval;
    final hasToken = (widget.api.token ?? '').isNotEmpty;
    return Section(
      title: S.plan,
      trailing: canApprove
          ? FilledButton.icon(
              onPressed: hasToken && !_approving ? _approve : null,
              icon: const Icon(Icons.verified_user, size: 18),
              label: const Text(S.approve),
            )
          : null,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (_newPlan != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Row(
                children: [
                  const Expanded(
                    child: Text(
                      S.planChanged,
                      style: TextStyle(color: TruehandColors.warn),
                    ),
                  ),
                  TextButton(
                    onPressed: () => setState(() {
                      _plan = _newPlan;
                      _newPlan = null;
                    }),
                    child: const Text(S.showNewPlan),
                  ),
                ],
              ),
            ),
          if (canApprove && !hasToken)
            const Padding(
              padding: EdgeInsets.only(bottom: 8),
              child: Text(
                S.approveNeedsToken,
                style: TextStyle(color: TruehandColors.warn),
              ),
            ),
          if (p == null || !p.exists)
            const Text(S.noPlan)
          else ...[
            if (p.problems.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(bottom: 8),
                child: Text(
                  '${S.planIncomplete}: ${p.problems.join('; ')}',
                  style: const TextStyle(color: TruehandColors.fail),
                ),
              ),
            SelectableText(
              p.markdown,
              style: const TextStyle(
                fontFamily: 'monospace',
                fontSize: 13,
                height: 1.4,
              ),
            ),
          ],
        ],
      ),
    );
  }

  Widget _evidence(TaskDetail d) {
    return Section(
      title: S.evidence,
      child: d.evidence.isEmpty
          ? const Text(S.noEvidence)
          : Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [for (final e in d.evidence.reversed) _evidenceTile(e)],
            ),
    );
  }

  Widget _evidenceTile(Evidence e) {
    final images = e.files.where((f) => f.isImage).toList();
    final state = !e.intact ? S.tampered : (e.fresh ? S.fresh : S.stale);
    final stateColor = !e.intact
        ? TruehandColors.fail
        : (e.fresh ? TruehandColors.pass : TruehandColors.warn);
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                statusIcon(e.verdict),
                size: 18,
                color: statusColor(e.verdict),
              ),
              const SizedBox(width: 8),
              Text(e.id, style: const TextStyle(fontWeight: FontWeight.w700)),
              const SizedBox(width: 8),
              Pill(state, color: stateColor),
              const Spacer(),
              Text(
                e.actor,
                style: const TextStyle(
                  fontSize: 12,
                  color: TruehandColors.textMuted,
                ),
              ),
            ],
          ),
          const SizedBox(height: 4),
          SelectableText(
            e.summary,
            style: const TextStyle(color: TruehandColors.textMuted),
          ),
          if (images.isNotEmpty) ...[
            const SizedBox(height: 8),
            SizedBox(
              height: 320,
              child: ListView(
                scrollDirection: Axis.horizontal,
                children: [
                  for (final f in images)
                    Padding(
                      padding: const EdgeInsets.only(right: 8),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            f.path.split('/').last,
                            style: const TextStyle(
                              fontSize: 11,
                              color: TruehandColors.textMuted,
                            ),
                          ),
                          const SizedBox(height: 4),
                          Expanded(
                            child: ClipRRect(
                              borderRadius: BorderRadius.circular(6),
                              child: GestureDetector(
                                onTap: () => _zoom(
                                  widget.api.fileUrl(
                                    widget.repo,
                                    widget.id,
                                    f.path,
                                  ),
                                ),
                                child: Image.network(
                                  widget.api.fileUrl(
                                    widget.repo,
                                    widget.id,
                                    f.path,
                                  ),
                                  fit: BoxFit.contain,
                                ),
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                ],
              ),
            ),
          ],
        ],
      ),
    );
  }

  void _zoom(String url) {
    showDialog<void>(
      context: context,
      builder: (c) => Dialog(
        insetPadding: const EdgeInsets.all(24),
        child: InteractiveViewer(
          maxScale: 8,
          child: Image.network(url, fit: BoxFit.contain),
        ),
      ),
    );
  }

  Widget _history(TaskDetail d) {
    return Section(
      title: S.history,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          for (final ev in d.events.reversed.take(30))
            Padding(
              padding: const EdgeInsets.only(bottom: 6),
              child: Text(
                '${_time(ev.time)}  ${_describe(ev)}',
                style: const TextStyle(fontSize: 12, fontFamily: 'monospace'),
              ),
            ),
        ],
      ),
    );
  }

  String _time(DateTime t) {
    final l = t.toLocal();
    String two(int v) => v.toString().padLeft(2, '0');
    return '${two(l.day)}/${two(l.month)} ${two(l.hour)}:${two(l.minute)}';
  }

  String _describe(TaskEvent e) => switch (e.type) {
    'moved' => '${e.from} → ${e.to} by ${e.actor}',
    'reworked' => '${e.from} ↩ ${e.to} by ${e.actor}',
    'gate_closed' => 'gate ${e.to} closed for ${e.actor}',
    'hand' => '${e.detail['record']} ${e.detail['verdict']} by ${e.actor}',
    _ => '${e.type} by ${e.actor}',
  };
}
