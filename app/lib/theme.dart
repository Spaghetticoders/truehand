import 'package:flutter/material.dart';

abstract final class TruehandColors {
  static const navy = Color(0xFF1C244B);
  static const cyan = Color(0xFF61D6D7);
  static const surface = Color(0xFFF6F7FB);
  static const card = Color(0xFFFFFFFF);
  static const border = Color(0xFFE2E5EF);
  static const textMuted = Color(0xFF6B7190);
  static const pass = Color(0xFF1E9E6A);
  static const fail = Color(0xFFD93A4A);
  static const skip = Color(0xFF9AA0B8);
  static const warn = Color(0xFFE09A1B);
}

ThemeData truehandTheme() {
  final scheme = ColorScheme.fromSeed(
    seedColor: TruehandColors.navy,
    primary: TruehandColors.navy,
    secondary: TruehandColors.cyan,
    surface: TruehandColors.surface,
  );
  return ThemeData(
    colorScheme: scheme,
    scaffoldBackgroundColor: TruehandColors.surface,
    appBarTheme: const AppBarTheme(
      backgroundColor: TruehandColors.navy,
      foregroundColor: Colors.white,
      elevation: 0,
    ),
    cardTheme: const CardThemeData(
      color: TruehandColors.card,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        side: BorderSide(color: TruehandColors.border),
        borderRadius: BorderRadius.all(Radius.circular(10)),
      ),
    ),
    useMaterial3: true,
  );
}
