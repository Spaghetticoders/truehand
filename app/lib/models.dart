const states = [
  'planned',
  'approved',
  'building',
  'verified',
  'reviewed',
  'delivered',
];

class Repo {
  Repo({
    required this.name,
    required this.root,
    required this.counts,
    this.error,
  });

  factory Repo.fromJson(Map<String, dynamic> j) => Repo(
    name: j['name'] as String,
    root: j['root'] as String,
    error: j['error'] as String?,
    counts: (j['counts'] as Map<String, dynamic>? ?? {}).map(
      (k, v) => MapEntry(k, v as int),
    ),
  );

  final String name;
  final String root;
  final String? error;
  final Map<String, int> counts;
}

class TaskSummary {
  TaskSummary({
    required this.id,
    required this.title,
    required this.state,
    required this.active,
    required this.missing,
    this.source,
    required this.updated,
  });

  factory TaskSummary.fromJson(Map<String, dynamic> j) => TaskSummary(
    id: j['id'] as String,
    title: j['title'] as String? ?? '',
    state: j['state'] as String,
    source: j['source'] as String?,
    active: j['active'] as bool? ?? false,
    missing: (j['missing'] as List? ?? []).cast<String>(),
    updated: DateTime.tryParse(j['updated'] as String? ?? '') ?? DateTime(2000),
  );

  final String id;
  final String title;
  final String state;
  final String? source;
  final bool active;
  final List<String> missing;
  final DateTime updated;
}

class GateItem {
  GateItem(this.check, this.status, this.message);

  factory GateItem.fromJson(Map<String, dynamic> j) => GateItem(
    j['check'] as String,
    j['status'] as String,
    j['message'] as String? ?? '',
  );

  final String check;
  final String status;
  final String message;
}

class Gate {
  Gate(this.target, this.pass, this.items);

  factory Gate.fromJson(Map<String, dynamic> j) => Gate(
    j['target'] as String,
    j['pass'] as bool,
    (j['items'] as List? ?? [])
        .map((e) => GateItem.fromJson(e as Map<String, dynamic>))
        .toList(),
  );

  final String target;
  final bool pass;
  final List<GateItem> items;
}

class EvidenceFile {
  EvidenceFile(this.path);
  final String path;
  bool get isImage => path.endsWith('.png') || path.endsWith('.jpg');
}

class Evidence {
  Evidence({
    required this.id,
    required this.hand,
    required this.key,
    required this.verdict,
    required this.summary,
    required this.actor,
    required this.fresh,
    required this.intact,
    required this.created,
    required this.files,
  });

  factory Evidence.fromJson(Map<String, dynamic> j) => Evidence(
    id: j['id'] as String,
    hand: j['hand'] as String,
    key: j['key'] as String? ?? '',
    verdict: j['verdict'] as String,
    summary: j['summary'] as String? ?? '',
    actor: j['actor'] as String? ?? '',
    fresh: j['fresh'] as bool? ?? false,
    intact: j['intact'] as bool? ?? false,
    created: DateTime.tryParse(j['created'] as String? ?? '') ?? DateTime(2000),
    files: (j['files'] as List? ?? [])
        .map((f) => EvidenceFile((f as Map<String, dynamic>)['path'] as String))
        .toList(),
  );

  final String id;
  final String hand;
  final String key;
  final String verdict;
  final String summary;
  final String actor;
  final bool fresh;
  final bool intact;
  final DateTime created;
  final List<EvidenceFile> files;
}

class TaskEvent {
  TaskEvent(this.time, this.type, this.actor, this.from, this.to, this.detail);

  factory TaskEvent.fromJson(Map<String, dynamic> j) => TaskEvent(
    DateTime.tryParse(j['time'] as String? ?? '') ?? DateTime(2000),
    j['type'] as String,
    j['actor'] as String? ?? '',
    j['from'] as String? ?? '',
    j['to'] as String? ?? '',
    (j['detail'] as Map<String, dynamic>?) ?? {},
  );

  final DateTime time;
  final String type;
  final String actor;
  final String from;
  final String to;
  final Map<String, dynamic> detail;
}

class TaskDetail {
  TaskDetail({
    required this.task,
    required this.roles,
    this.nextGate,
    required this.evidence,
    required this.events,
  });

  factory TaskDetail.fromJson(Map<String, dynamic> j) {
    final view = j['view'] as Map<String, dynamic>;
    final task = view['task'] as Map<String, dynamic>;
    return TaskDetail(
      task: TaskSummary.fromJson({...task, 'active': view['active']}),
      roles: (task['roles'] as Map<String, dynamic>? ?? {}).map(
        (k, v) => MapEntry(k, v as String),
      ),
      nextGate: view['next_gate'] == null
          ? null
          : Gate.fromJson(view['next_gate'] as Map<String, dynamic>),
      evidence: (view['evidence'] as List? ?? [])
          .map((e) => Evidence.fromJson(e as Map<String, dynamic>))
          .toList(),
      events: (j['events'] as List? ?? [])
          .map((e) => TaskEvent.fromJson(e as Map<String, dynamic>))
          .toList(),
    );
  }

  final TaskSummary task;
  final Map<String, String> roles;
  final Gate? nextGate;
  final List<Evidence> evidence;
  final List<TaskEvent> events;
}

class PlanDoc {
  PlanDoc(this.exists, this.markdown, this.sha256, this.problems);

  factory PlanDoc.fromJson(Map<String, dynamic> j) => PlanDoc(
    j['exists'] as bool? ?? false,
    j['markdown'] as String? ?? '',
    j['sha256'] as String? ?? '',
    (j['problems'] as List? ?? [])
        .map(
          (p) =>
              '${(p as Map<String, dynamic>)['section'] ?? ''}: ${p['message']}',
        )
        .toList(),
  );

  final bool exists;
  final String markdown;
  final String sha256;
  final List<String> problems;
  bool get valid => exists && problems.isEmpty;
}
