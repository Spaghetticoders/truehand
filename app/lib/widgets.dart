import 'package:flutter/material.dart';

import 'theme.dart';

Color statusColor(String status) => switch (status) {
  'pass' => TruehandColors.pass,
  'fail' => TruehandColors.fail,
  'error' => TruehandColors.warn,
  _ => TruehandColors.skip,
};

IconData statusIcon(String status) => switch (status) {
  'pass' => Icons.check_circle,
  'fail' => Icons.cancel,
  'error' => Icons.error,
  _ => Icons.remove_circle_outline,
};

class Pill extends StatelessWidget {
  const Pill(this.label, {super.key, required this.color, this.filled = false});

  final String label;
  final Color color;
  final bool filled;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: filled ? color : color.withValues(alpha: 0.1),
        borderRadius: BorderRadius.circular(20),
      ),
      child: Text(
        label,
        style: TextStyle(
          fontSize: 11,
          fontWeight: FontWeight.w600,
          color: filled ? TruehandColors.card : color,
        ),
      ),
    );
  }
}

class StateStepper extends StatelessWidget {
  const StateStepper({super.key, required this.current, required this.states});

  final String current;
  final List<String> states;

  @override
  Widget build(BuildContext context) {
    final ci = states.indexOf(current);
    return Wrap(
      spacing: 4,
      runSpacing: 4,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        for (var i = 0; i < states.length; i++) ...[
          Pill(
            states[i],
            color: i <= ci ? TruehandColors.navy : TruehandColors.skip,
            filled: i == ci,
          ),
          if (i < states.length - 1)
            const Icon(
              Icons.chevron_right,
              size: 14,
              color: TruehandColors.skip,
            ),
        ],
      ],
    );
  }
}

class Section extends StatelessWidget {
  const Section({
    super.key,
    required this.title,
    required this.child,
    this.trailing,
  });

  final String title;
  final Widget child;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    title,
                    style: Theme.of(context).textTheme.titleMedium,
                  ),
                ),
                ?trailing,
              ],
            ),
            const SizedBox(height: 12),
            child,
          ],
        ),
      ),
    );
  }
}
