import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:flutter/services.dart';

import 'main.dart' show AppUpdate, Prefs, appLog, kServerBase;

// ---------------------------------------------------------------------------
// Debug chute: one-way health reports from this install to our own
// server (POST https://arcade.itak.live/debug/report), so a stick
// can be diagnosed without Android wireless debugging, which turns
// itself off whenever it likes. The app only ever sends; nothing
// here listens or accepts commands back.
//
// Identity is an install code: 16 random bytes as hex, generated
// once and kept in the 'install_code' pref for the life of the
// install. The server keys everything by a hash of it and a human
// labels sticks there, so the app never carries names or accounts.
//
// Two senders share this payload: this Dart side (launch send,
// throttled to one success per 6 h, and the manual Settings row)
// and the native WorkManager check-in (DebugChute.kt), which adds
// a tiny 15-minute ping and uploads this same full report when the
// server answers want_full_report.
// ---------------------------------------------------------------------------

class DebugChute {
  static const _channel = MethodChannel('net.itak/emu');

  static const String _codePref = 'install_code';
  static const String _togglePref = 'debug_chute';
  static const String _lastPref = 'debug_chute_last';

  /// One full report at most per this window on the launch path.
  static const int launchThrottleSeconds = 6 * 3600;

  /// The install code, generated on first ask and then stable.
  /// The native check-in worker uses the same pref and format, so
  /// whichever side runs first establishes the same identity.
  static Future<String> installCode() async {
    final existing = await Prefs.get(_codePref);
    if (existing != null && _isHex(existing, 32)) return existing;
    final rnd = Random.secure();
    final bytes = List<int>.generate(16, (_) => rnd.nextInt(256));
    final code =
        bytes.map((b) => b.toRadixString(16).padLeft(2, '0')).join();
    await Prefs.set(_codePref, code);
    return code;
  }

  static bool _isHex(String s, int len) {
    if (s.length != len) return false;
    for (final c in s.codeUnits) {
      final ok = (c >= 0x30 && c <= 0x39) || // 0-9
          (c >= 0x61 && c <= 0x66) || // a-f
          (c >= 0x41 && c <= 0x46); // A-F
      if (!ok) return false;
    }
    return true;
  }

  /// Settings -> Debug reports toggle; default on.
  static Future<bool> enabled() async {
    return (await Prefs.get(_togglePref)) != '0';
  }

  /// The app's log text (the native side streams logcat to a
  /// file; readLog hands back its tail, up to about 200 KB).
  static Future<String> _logText() async {
    try {
      final data =
          await _channel.invokeMethod<Map<dynamic, dynamic>>('readLog');
      return (data?['text'] ?? '').toString();
    } catch (_) {
      return '';
    }
  }

  /// The last [max] log lines that carry a core frame timing.
  static List<String> timingLines(String logText, int max) {
    final hits = logText
        .split('\n')
        .where((l) => l.contains('native timing'))
        .toList();
    if (hits.length <= max) return hits;
    return hits.sublist(hits.length - max);
  }

  /// The log tail, capped at the last 120 KB of UTF-8 so the
  /// report stays small no matter how chatty the session was.
  static String logTail(String logText) {
    const cap = 120 * 1024;
    final bytes = utf8.encode(logText);
    if (bytes.length <= cap) return logText;
    var tail = utf8.decode(bytes.sublist(bytes.length - cap),
        allowMalformed: true);
    final nl = tail.indexOf('\n');
    if (nl >= 0) tail = tail.substring(nl + 1); // drop the partial line
    return tail;
  }

  /// The full report payload. [reason] is launch, manual, or pull
  /// (the native side sends pull itself; Dart sends launch and
  /// manual). Model and SDK have no cheap Dart source here, so
  /// they ride as unknown/0; the native check-in reports the real
  /// values in its pings.
  static Future<Map<String, dynamic>> buildReport(String reason) async {
    final info = await AppUpdate.installed();
    final log = await _logText();
    return <String, dynamic>{
      'install_code': await installCode(),
      'versionName': info?['versionName']?.toString() ?? '',
      'versionCode': (info?['versionCode'] as num?)?.toInt() ?? 0,
      'model': 'unknown',
      'sdk': 0,
      'timingLines': timingLines(log, 40),
      'logTail': logTail(log),
      'reason': reason,
      'timestamp': DateTime.now().toUtc().toIso8601String(),
    };
  }

  /// POST one full report. True on a 200 from the server; stamps
  /// the last-success pref either way it goes only on success, so
  /// a dead network never throttles the next launch's retry.
  static Future<bool> sendReport(String reason) async {
    final payload = await buildReport(reason);
    final client = HttpClient();
    client.connectionTimeout = const Duration(seconds: 15);
    try {
      final req =
          await client.postUrl(Uri.parse('$kServerBase/debug/report'));
      req.headers.contentType = ContentType.json;
      req.headers.set(HttpHeaders.userAgentHeader, 'iTaK-Gaming/1.0');
      req.write(jsonEncode(payload));
      final resp = await req.close().timeout(const Duration(seconds: 15));
      await resp.drain<void>();
      if (resp.statusCode == 200) {
        await Prefs.set(_lastPref,
            (DateTime.now().millisecondsSinceEpoch ~/ 1000).toString());
        appLog('debug chute: report sent ($reason)');
        return true;
      }
      appLog(
          'debug chute: report refused (${resp.statusCode}, $reason)');
      return false;
    } catch (e) {
      appLog('debug chute: report failed ($reason): $e');
      return false;
    } finally {
      client.close(force: true);
    }
  }

  /// Launch path: fire and forget after the home screen paints.
  /// Silent apart from appLog lines; the toggle gates it and the
  /// 6 h throttle keeps a TV that relaunches all day quiet.
  static Future<void> maybeSendOnLaunch() async {
    try {
      if (!await enabled()) return;
      final last = int.tryParse(await Prefs.get(_lastPref) ?? '') ?? 0;
      final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
      if (now - last < launchThrottleSeconds) return;
      await sendReport('launch');
    } catch (_) {
      // Never let the chute disturb a launch.
    }
  }
}
