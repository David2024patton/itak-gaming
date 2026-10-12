import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

void main() {
  runApp(const ITaKGamingApp());
}

class ITaKGamingApp extends StatelessWidget {
  const ITaKGamingApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'iTaK Gaming',
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark().copyWith(
        scaffoldBackgroundColor: const Color(0xFF0A0A14),
        primaryColor: const Color(0xFF00E676),
      ),
      home: const SystemSelectScreen(),
    );
  }
}

/// Key/value store backed by Android SharedPreferences (itak_prefs),
/// shared with the emulator screen. String and int flavors: the Kotlin
/// side reads filter/display modes as ints, so those go through the
/// int methods or the read crashes.
class Prefs {
  static const _channel = MethodChannel('net.itak/emu');

  static Future<String?> get(String key) async {
    try {
      return await _channel.invokeMethod<String>('getPref', {'key': key});
    } catch (_) {
      return null;
    }
  }

  static Future<void> set(String key, String value) async {
    try {
      await _channel.invokeMethod('setPref', {'key': key, 'value': value});
    } catch (_) {}
  }

  static Future<int?> getInt(String key) async {
    try {
      return await _channel.invokeMethod<int>('getPrefInt', {'key': key});
    } catch (_) {
      return null;
    }
  }

  static Future<void> setInt(String key, int value) async {
    try {
      await _channel
          .invokeMethod('setPrefInt', {'key': key, 'value': value});
    } catch (_) {}
  }
}

// ---------------------------------------------------------------------------
// Systems. Filtered by build (32-bit vs 64-bit done at compile time).
// ---------------------------------------------------------------------------

class SystemInfo {
  final String id;
  final String name;
  final String tagline;
  final Color color;
  final String core;
  final String dir;
  final List<String> exts;
  final String thumbRepo;
  final String imageUrl;

  /// True when the system's core only exists for 64-bit builds. On a
  /// 32-bit device the card shows faded with an upgrade message.
  final bool requires64;

  const SystemInfo({
    required this.id,
    required this.name,
    required this.tagline,
    required this.color,
    required this.core,
    required this.dir,
    required this.exts,
    required this.thumbRepo,
    required this.imageUrl,
    this.requires64 = false,
  });
}

/// Set from the device at startup: false on 32-bit-only hardware.
bool gIs64Bit = true;

const List<SystemInfo> kSystems = [
  SystemInfo(
    id: 'nes',
    name: 'Nintendo',
    tagline: 'NES · 1985',
    color: Color(0xFFE53935),
    core: 'libfceumm.so',
    dir: 'NES',
    exts: ['.nes'],
    thumbRepo: 'Nintendo_-_Nintendo_Entertainment_System',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/NES-Console-Set.png?width=900',
  ),
  SystemInfo(
    id: 'snes',
    name: 'Super Nintendo',
    tagline: 'SNES · 1991',
    color: Color(0xFF7B1FA2),
    core: 'libsnes9x.so',
    dir: 'SNES',
    exts: ['.sfc', '.smc'],
    thumbRepo: 'Nintendo_-_Super_Nintendo_Entertainment_System',
    imageUrl:
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/3/36/SNES-Mod1-Console-Set.png/1280px-SNES-Mod1-Console-Set.png',
  ),
  SystemInfo(
    id: 'genesis',
    name: 'Sega Genesis',
    tagline: 'Genesis · 1989',
    color: Color(0xFF1565C0),
    core: 'libgenesis_plus_gx.so',
    dir: 'Genesis',
    exts: ['.md', '.gen', '.bin'],
    thumbRepo: 'Sega_-_Mega_Drive_-_Genesis',
    imageUrl:
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/1/19/Sega-Genesis-NA-Mk2-Console-Set.png/1280px-Sega-Genesis-NA-Mk2-Console-Set.png',
  ),
  SystemInfo(
    id: 'gameboy',
    name: 'Game Boy',
    tagline: 'Game Boy · 1989',
    color: Color(0xFF9E9E9E),
    core: 'libgambatte.so',
    dir: 'GameBoy',
    exts: ['.gb', '.gbc'],
    thumbRepo: 'Nintendo_-_Game_Boy',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Game-Boy-Original.png?width=900',
  ),
  SystemInfo(
    id: 'gba',
    name: 'Game Boy Advance',
    tagline: 'GBA · 2001',
    color: Color(0xFF5C6BC0),
    core: 'libmgba.so',
    dir: 'GBA',
    exts: ['.gba'],
    thumbRepo: 'Nintendo_-_Game_Boy_Advance',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Nintendo-Game-Boy-Advance-Purple-FL.png?width=900',
  ),
  SystemInfo(
    id: 'ps1',
    name: 'PlayStation',
    tagline: 'PS1 · 1995',
    color: Color(0xFF616161),
    core: 'libpcsx_rearmed.so',
    dir: 'PS1',
    exts: ['.cue', '.iso'],
    thumbRepo: 'Sony_-_PlayStation',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/PSX-Console-wController.png?width=900',
  ),
  SystemInfo(
    id: 'n64',
    name: 'Nintendo 64',
    tagline: 'N64 · 1996',
    color: Color(0xFFEF6C00),
    core: 'libmupen64plus.so',
    dir: 'N64',
    exts: ['.n64', '.z64', '.v64'],
    thumbRepo: 'Nintendo_-_Nintendo_64',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/N64-Console-Set.png?width=900',
    requires64: true,
  ),
  SystemInfo(
    id: 'dc',
    name: 'Dreamcast',
    tagline: 'Dreamcast · 1999',
    color: Color(0xFF00838F),
    core: 'libflycast.so',
    dir: 'Dreamcast',
    exts: ['.cdi', '.gdi', '.chd', '.cue'],
    thumbRepo: 'Sega_-_Dreamcast',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Dreamcast-Console-Set.png?width=900',
    requires64: true,
  ),
  SystemInfo(
    id: 'psp',
    name: 'PSP',
    tagline: 'PlayStation Portable · 2004',
    color: Color(0xFF37474F),
    core: 'libppsspp.so',
    dir: 'PSP',
    exts: ['.iso', '.cso', '.pbp'],
    thumbRepo: 'Sony_-_PlayStation_Portable',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Sony-PSP-1000-Body.png?width=900',
    requires64: true,
  ),
  SystemInfo(
    id: 'ds',
    name: 'Nintendo DS',
    tagline: 'DS · 2004',
    color: Color(0xFF00897B),
    core: 'libmelonds.so',
    dir: 'NDS',
    exts: ['.nds'],
    thumbRepo: 'Nintendo_-_Nintendo_DS',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Nintendo-DS-Fat-Blue.png?width=900',
    requires64: true,
  ),
  SystemInfo(
    id: 'saturn',
    name: 'Sega Saturn',
    tagline: 'Saturn · 1994',
    color: Color(0xFF6A1B9A),
    core: 'libyabasanshiro.so',
    dir: 'Saturn',
    exts: ['.cue', '.chd', '.iso'],
    thumbRepo: 'Sega_-_Saturn',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/Sega-Saturn-Console-Set-Mk1.png?width=900',
    requires64: true,
  ),
  SystemInfo(
    id: 'gc',
    name: 'GameCube',
    tagline: 'GameCube · 2001',
    color: Color(0xFF4527A0),
    core: 'libdolphin.so',
    dir: 'GameCube',
    exts: ['.iso', '.gcm', '.ciso'],
    thumbRepo: 'Nintendo_-_GameCube',
    imageUrl:
        'https://commons.wikimedia.org/wiki/Special:FilePath/GameCube-Console-Set.png?width=900',
    requires64: true,
  ),
];

SystemInfo systemById(String id) => kSystems.firstWhere((s) => s.id == id);

// ---------------------------------------------------------------------------
// Artwork: box art and console photos download automatically and cache
// on the device next to the games.
// ---------------------------------------------------------------------------

class Artwork {
  static final HttpClient _http = HttpClient()
    ..connectionTimeout = const Duration(seconds: 10);

  static Future<List<int>?> _download(String url) async {
    try {
      final req = await _http.getUrl(Uri.parse(url));
      req.headers.set('User-Agent', 'iTaK-Gaming/1.0');
      final resp = await req.close();
      if (resp.statusCode != 200) return null;
      final bytes = <int>[];
      await for (final chunk in resp) {
        bytes.addAll(chunk);
        if (bytes.length > 8 * 1024 * 1024) return null;
      }
      return bytes;
    } catch (_) {
      return null;
    }
  }

  static bool _looksLikeImage(List<int> b) =>
      b.length > 2000 &&
      ((b[0] == 0x89 && b[1] == 0x50) || // PNG
          (b[0] == 0xFF && b[1] == 0xD8)); // JPEG

  static String _boxartUrl(String repo, String gameName) =>
      'https://raw.githubusercontent.com/libretro-thumbnails/$repo/master/Named_Boxarts/${Uri.encodeComponent(gameName)}.png';

  /// Returns the cached box art file for a game, downloading it from
  /// the libretro thumbnails project when missing. Null when no art
  /// exists for the game.
  static Future<File?> boxart(SystemInfo sys, String gameName, String romDirPath,
      {bool fetch = true}) async {
    final art = File('$romDirPath/boxart/$gameName.png');
    if (art.existsSync()) return art;
    if (!fetch) return null;
    // The thumbnail repo replaces '&' with '_' in file names.
    for (final candidate in {gameName, gameName.replaceAll('&', '_')}) {
      final bytes = await _download(_boxartUrl(sys.thumbRepo, candidate));
      if (bytes != null && _looksLikeImage(bytes)) {
        try {
          await art.parent.create(recursive: true);
          await art.writeAsBytes(bytes, flush: true);
          return art;
        } catch (_) {
          return null;
        }
      }
    }
    return null;
  }

  static File? _systemImageFile(SystemInfo sys) {
    // Transparent PNGs first: they are the processed console art.
    for (final ext in ['.png', '.jpg']) {
      final f = File('/sdcard/iTaK/systems/${sys.id}$ext');
      if (f.existsSync()) return f;
    }
    return null;
  }

  static Future<File?> systemImage(SystemInfo sys) async {
    final existing = _systemImageFile(sys);
    if (existing != null) return existing;
    final bytes = await _download(sys.imageUrl);
    if (bytes == null || !_looksLikeImage(bytes)) return null;
    final ext = sys.imageUrl.endsWith('.png') ? '.png' : '.jpg';
    final out = File('/sdcard/iTaK/systems/${sys.id}$ext');
    try {
      await out.parent.create(recursive: true);
      await out.writeAsBytes(bytes, flush: true);
      return out;
    } catch (_) {
      return null;
    }
  }
}

// ---------------------------------------------------------------------------
// Play statistics: powers the Most Played and Recent sorts.
// ---------------------------------------------------------------------------

class PlayStats {
  static Map<String, dynamic> _data = {};
  static bool _loaded = false;

  static Future<void> load() async {
    if (_loaded) return;
    _loaded = true;
    final raw = await Prefs.get('play_stats');
    if (raw != null && raw.isNotEmpty) {
      try {
        _data = jsonDecode(raw) as Map<String, dynamic>;
      } catch (_) {
        _data = {};
      }
    }
  }

  static int count(String path) =>
      ((_data[path] as Map?)?['c'] as num?)?.toInt() ?? 0;

  static int lastPlayed(String path) =>
      ((_data[path] as Map?)?['t'] as num?)?.toInt() ?? 0;

  static Future<void> record(String path) async {
    await load();
    final entry = (_data[path] as Map?) ?? {};
    _data[path] = {
      'c': ((entry['c'] as num?)?.toInt() ?? 0) + 1,
      't': DateTime.now().millisecondsSinceEpoch,
    };
    await Prefs.set('play_stats', jsonEncode(_data));
  }
}

const List<Color> kPlayerColors = [
  Color(0xFF00E676),
  Color(0xFF29B6F6),
  Color(0xFFFF7043),
  Color(0xFFAB47BC),
  Color(0xFFFFCA28),
  Color(0xFF26A69A),
  Color(0xFFEF5350),
  Color(0xFF8D6E63),
];

// Zones on the home screen, top to bottom.
const int _zoneSettings = 0;
const int _zonePlayers = 1;
const int _zoneSystems = 2;

class SystemSelectScreen extends StatefulWidget {
  const SystemSelectScreen({super.key});

  @override
  State<SystemSelectScreen> createState() => _SystemSelectScreenState();
}

class _SystemSelectScreenState extends State<SystemSelectScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  int _systemIndex = 1; // SNES default
  int _topIndex = 1; // top bar: 0 = Leaderboard, 1 = Settings
  int _zone = _zoneSystems;
  List<String> _players = [];
  String _currentPlayer = '';
  String? _banner;
  Timer? _bannerTimer;
  final Map<String, File?> _systemImages = {};
  final Map<String, int> _gameCounts = {};
  final FocusNode _focusNode = FocusNode();
  final ScrollController _gridScroll = ScrollController();

  bool _locked(SystemInfo sys) => sys.requires64 && !gIs64Bit;

  Future<void> _loadDeviceInfo() async {
    try {
      final info = await _channel.invokeMethod<Map>('deviceInfo');
      final is64 = info?['is64'] == true;
      if (!mounted) return;
      setState(() => gIs64Bit = is64);
    } catch (_) {
      // Channel unavailable (desktop dev): stay optimistic.
    }
  }

  @override
  void initState() {
    super.initState();
    _loadDeviceInfo();
    Avatars.load();
    _loadPlayers();
    _loadControllerMap();
    _loadSystemImages();
    _countGames();
    // Native side calls this when a gamepad connects: ask who picked
    // it up by jumping to the players row with a banner.
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'controllerCaptured') {
        final args = call.arguments as Map?;
        final w = _captureWaiter;
        if (w != null && !w.isCompleted) {
          w.complete(args?.map((k, v) => MapEntry(k.toString(), v)));
        }
        // Close the "press any button" dialog so the picker updates.
        _closeCaptureDialog?.call();
      }
      if (call.method == 'controllerConnected' && mounted) {
        final name = (call.arguments as Map?)?['name']?.toString() ?? 'Controller';
        setState(() {
          _zone = _zonePlayers;
          _banner = '$name connected. Open Who\'s playing to pick your name.';
        });
        _bannerTimer?.cancel();
        _bannerTimer = Timer(const Duration(seconds: 7), () {
          if (mounted) setState(() => _banner = null);
        });
      }
      return null;
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  Future<void> _loadPlayers() async {
    final stored = await Prefs.get('players');
    final current = await Prefs.get('current_player');
    var players = (stored ?? '')
        .split('\n')
        .map((s) => s.trim())
        .where((s) => s.isNotEmpty)
        .toList();
    if (players.isEmpty) {
      players = ['David'];
      await Prefs.set('players', players.join('\n'));
    }
    var cur = current ?? '';
    if (!players.contains(cur)) {
      cur = players.first;
      await Prefs.set('current_player', cur);
    }
    if (!mounted) return;
    setState(() {
      _players = players;
      _currentPlayer = cur;
    });
  }

  Future<void> _loadSystemImages() async {
    for (final sys in kSystems) {
      final f = await Artwork.systemImage(sys);
      if (!mounted) return;
      setState(() => _systemImages[sys.id] = f);
    }
  }

  Future<void> _countGames() async {
    for (final sys in kSystems) {
      var n = 0;
      try {
        final dir = Directory('/sdcard/ROMs/${sys.dir}');
        if (await dir.exists()) {
          await for (final e in dir.list()) {
            if (e is File &&
                sys.exts.any((x) => e.path.toLowerCase().endsWith(x))) {
              n++;
            }
          }
        }
      } catch (_) {}
      if (!mounted) return;
      setState(() => _gameCounts[sys.id] = n);
    }
  }

  Future<void> _selectPlayer(int index) async {
    setState(() {
      _currentPlayer = _players[index];
      _banner = null;
    });
    await Prefs.set('current_player', _currentPlayer);
  }

  // Controller assignments: player name -> {id, name} of their gamepad.
  Map<String, dynamic> _controllerMap = {};
  Completer<Map<String, dynamic>?>? _captureWaiter;

  Future<void> _loadControllerMap() async {
    final raw = await Prefs.get('controller_map');
    if (raw == null || raw.isEmpty) return;
    try {
      final decoded = jsonDecode(raw);
      if (decoded is Map<String, dynamic> && mounted) {
        setState(() => _controllerMap = decoded);
      }
    } catch (_) {}
  }

  Future<void> _saveControllerMap() =>
      Prefs.set('controller_map', jsonEncode(_controllerMap));

  String controllerLabelFor(String player) {
    final c = _controllerMap[player];
    if (c is Map && c['name'] != null) return c['name'].toString();
    return 'No controller';
  }

  // Return keyboard focus to the home screen ONLY when nothing is
  // stacked on top of it. Dialogs freeze the background: while any
  // dialog or screen is open, the home screen must not take focus or
  // answer the remote.
  void _refocusHome() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted && (ModalRoute.of(context)?.isCurrent ?? true)) {
        _focusNode.requestFocus();
      }
    });
  }

  Future<void> _removePlayer(String name) async {
    if (_players.length <= 1) return;
    final players = _players.where((p) => p != name).toList();
    await Prefs.set('players', players.join('\n'));
    if (_controllerMap.remove(name) != null) await _saveControllerMap();
    var cur = _currentPlayer;
    if (cur == name) {
      cur = players.first;
      await Prefs.set('current_player', cur);
    }
    if (!mounted) return;
    setState(() {
      _players = players;
      _currentPlayer = cur;
    });
  }

  static const Color _dialogBg = Color(0xFF262640);

  Future<void> _infoDialog(String title, String body) async {
    await showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: _dialogBg,
        title: Text(title),
        content: Text(body, style: const TextStyle(fontSize: 17)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('OK'),
          ),
        ],
      ),
    );
    _refocusHome();
  }

  Future<void> _confirmRemove(String name, VoidCallback refresh) async {
    if (_players.length <= 1) {
      await _infoDialog(
        'One player needed',
        'The arcade always needs at least one player. Add another '
        'player first, then you can remove $name.',
      );
      return;
    }
    final ok = await showDialog<bool>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: _dialogBg,
        title: Text('Remove $name?'),
        content: const Text(
          'Their saved games stay on this box, in case they come back.',
          style: TextStyle(fontSize: 17),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text(
              'Remove',
              style: TextStyle(color: Colors.redAccent),
            ),
          ),
        ],
      ),
    );
    if (ok == true) {
      await _removePlayer(name);
      refresh();
    }
    _refocusHome();
  }

  Future<void> _renamePlayer(String oldName, VoidCallback refresh) async {
    final name = await showTvKeyboard(
      context,
      title: 'Rename player',
      initial: oldName,
    );
    final clean = (name ?? '').trim().replaceAll('\n', ' ');
    if (clean.isEmpty || clean == oldName) return;
    if (_players.contains(clean)) {
      await _infoDialog('Name taken', 'There is already a player named $clean.');
      return;
    }
    final players = _players.map((p) => p == oldName ? clean : p).toList();
    await Prefs.set('players', players.join('\n'));
    if (_currentPlayer == oldName) {
      await Prefs.set('current_player', clean);
    }
    await Avatars.rename(oldName, clean);
    // A rename keeps the player's controller assignment.
    final assigned = _controllerMap.remove(oldName);
    if (assigned != null) {
      _controllerMap[clean] = assigned;
      await _saveControllerMap();
    }
    // Move the player's save folder and stats file to the new name.
    try {
      await _channel.invokeMethod('renamePlayerFiles', {
        'old': oldName,
        'new': clean,
      });
    } catch (_) {}
    if (!mounted) return;
    setState(() {
      _players = players;
      if (_currentPlayer == oldName) _currentPlayer = clean;
    });
    refresh();
    _refocusHome();
  }

  Future<void> _managePlayers() async {
    // Play-time totals power the stats column of the table.
    final totals = <String, List<int>>{}; // name -> [seconds, plays]
    try {
      final raw =
          await _channel.invokeMethod<String>('getLeaderboard') ?? '[]';
      for (final e in jsonDecode(raw) as List) {
        final m = e as Map<String, dynamic>;
        var secs = 0;
        var plays = 0;
        final games = m['games'];
        if (games is Map) {
          for (final g in games.values) {
            if (g is Map) {
              secs += (g['seconds'] as num? ?? 0).toInt();
              plays += (g['plays'] as num? ?? 0).toInt();
            }
          }
        }
        totals[m['name'].toString()] = [secs, plays];
      }
    } catch (_) {}
    if (!mounted) return;
    await showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setLocal) {
          void refresh() {
            setLocal(() {});
            setState(() {});
          }

          Widget headerCell(String text, {bool right = false}) => Text(
                text,
                textAlign: right ? TextAlign.right : TextAlign.left,
                style: const TextStyle(
                  fontSize: 13,
                  letterSpacing: 1.2,
                  color: Colors.white54,
                  fontWeight: FontWeight.bold,
                ),
              );

          return AlertDialog(
            backgroundColor: _dialogBg,
            title: const Text('Manage players'),
            content: SizedBox(
              width: 720,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  // Table header
                  Padding(
                    padding: const EdgeInsets.fromLTRB(14, 2, 14, 8),
                    child: Row(
                      children: [
                        const SizedBox(width: 60),
                        Expanded(child: headerCell('PLAYER')),
                        SizedBox(
                          width: 130,
                          child: headerCell('PLAY TIME', right: true),
                        ),
                        const SizedBox(width: 150),
                      ],
                    ),
                  ),
                  const Divider(color: Colors.white24, height: 1),
                  Flexible(
                    child: ListView(
                      shrinkWrap: true,
                      children: [
                        for (final p in _players)
                          Container(
                            margin: const EdgeInsets.only(top: 8),
                            decoration: BoxDecoration(
                              borderRadius: BorderRadius.circular(12),
                              border: Border.all(
                                color: p == _currentPlayer
                                    ? const Color(0xFF00E676)
                                    : Colors.white12,
                                width: p == _currentPlayer ? 1.6 : 1,
                              ),
                            ),
                            child: ClipRRect(
                              borderRadius: BorderRadius.circular(12),
                              child: Material(
                                color: const Color(0xFF1B1B28),
                                child: InkWell(
                                  onTap: () async {
                                    Navigator.pop(ctx);
                                    await _selectPlayer(
                                        _players.indexOf(p));
                                  },
                                  focusColor:
                                      Colors.white.withOpacity(0.14),
                                  child: Padding(
                                padding:
                                    const EdgeInsets.fromLTRB(14, 10, 6, 10),
                                child: Row(
                                  children: [
                                    PlayerAvatar(name: p, size: 46),
                                    const SizedBox(width: 14),
                                    Expanded(
                                      child: Column(
                                        crossAxisAlignment:
                                            CrossAxisAlignment.start,
                                        children: [
                                          Text(
                                            p,
                                            style: const TextStyle(
                                              fontSize: 20,
                                              fontWeight: FontWeight.bold,
                                            ),
                                          ),
                                          Text(
                                            p == _currentPlayer
                                                ? 'Playing now'
                                                : 'Tap row to switch',
                                            style: const TextStyle(
                                              fontSize: 13.5,
                                              color: Colors.white54,
                                            ),
                                          ),
                                        ],
                                      ),
                                    ),
                                    SizedBox(
                                      width: 130,
                                      child: Column(
                                        crossAxisAlignment:
                                            CrossAxisAlignment.end,
                                        children: [
                                          Text(
                                            _fmtDuration(
                                                totals[p]?[0] ?? 0),
                                            style: const TextStyle(
                                                fontSize: 17),
                                          ),
                                          Text(
                                            '${totals[p]?[1] ?? 0} plays',
                                            style: const TextStyle(
                                              fontSize: 13,
                                              color: Colors.white54,
                                            ),
                                          ),
                                        ],
                                      ),
                                    ),
                                    const SizedBox(width: 4),
                                    IconButton(
                                      icon: const Icon(Icons.edit),
                                      tooltip: 'Rename',
                                      onPressed: () =>
                                          _renamePlayer(p, refresh),
                                    ),
                                    IconButton(
                                      icon: const Icon(Icons.face),
                                      tooltip: 'Change picture',
                                      onPressed: () async {
                                        await showAvatarPicker(context, p);
                                        refresh();
                                      },
                                    ),
                                    IconButton(
                                      icon: const Icon(Icons.delete,
                                          color: Colors.redAccent),
                                      tooltip: 'Remove',
                                      onPressed: () =>
                                          _confirmRemove(p, refresh),
                                    ),
                                  ],
                                ),
                              ),
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
            actions: [
              TextButton(
                onPressed: () async {
                  await _addPlayer();
                  refresh();
                },
                child: const Text('Add player'),
              ),
              TextButton(
                onPressed: () => Navigator.pop(ctx),
                child: const Text('Done'),
              ),
            ],
          );
        },
      ),
    );
    _refocusHome();
  }

  // Ask the native side to watch for the next gamepad button press;
  // completes with {id, name} of the controller that was pressed.
  // The channel handler completes the waiter and closes the dialog.
  VoidCallback? _closeCaptureDialog;

  Future<Map<String, dynamic>?> _captureController(String player) async {
    final waiter = Completer<Map<String, dynamic>?>();
    _captureWaiter = waiter;
    try {
      await _channel.invokeMethod('captureController');
    } catch (_) {
      _captureWaiter = null;
      return null;
    }
    if (!mounted) return null;
    final captured = await showDialog<Map<String, dynamic>?>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.85),
      builder: (ctx) {
        _closeCaptureDialog = () {
          if (Navigator.of(ctx).canPop()) Navigator.of(ctx).pop();
        };
        return AlertDialog(
          backgroundColor: _dialogBg,
          title: Text('Controller for $player'),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.sports_esports,
                  size: 54, color: Colors.white70),
              const SizedBox(height: 14),
              Text(
                'Press any button on the controller for $player.',
                style: const TextStyle(fontSize: 18),
                textAlign: TextAlign.center,
              ),
            ],
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(ctx, null),
              child: const Text('Cancel'),
            ),
          ],
        );
      },
    ).then((_) => waiter.isCompleted ? waiter.future : null);
    _closeCaptureDialog = null;
    if (!waiter.isCompleted) {
      try {
        await _channel.invokeMethod('cancelCapture');
      } catch (_) {}
    }
    _captureWaiter = null;
    _refocusHome();
    return captured;
  }

  Future<void> _assignController(String player, VoidCallback refresh) async {
    final captured = await _captureController(player);
    if (captured == null || captured['id'] == null) return;
    // One controller belongs to one player: drop it from anyone else.
    _controllerMap.removeWhere(
      (key, v) => v is Map && v['id'] == captured['id'] && key != player,
    );
    setState(() {
      _controllerMap[player] = {
        'id': captured['id'],
        'name': captured['name']?.toString() ?? 'Controller',
      };
    });
    await _saveControllerMap();
    refresh();
  }

  Future<void> _openPlayerPicker() async {
    await showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.82),
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setLocal) {
          void refresh() {
            setLocal(() {});
            setState(() {});
          }

          return AlertDialog(
            backgroundColor: _dialogBg,
            title: const Text("Who's playing?"),
            content: SizedBox(
              width: 660,
              child: ListView(
                shrinkWrap: true,
                children: [
                  for (final p in _players)
                    Container(
                      margin: const EdgeInsets.only(bottom: 8),
                      decoration: BoxDecoration(
                        borderRadius: BorderRadius.circular(12),
                        border: Border.all(
                          color: p == _currentPlayer
                              ? const Color(0xFF00E676)
                              : Colors.white12,
                          width: p == _currentPlayer ? 1.6 : 1,
                        ),
                      ),
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(12),
                        child: Material(
                          color: const Color(0xFF1B1B28),
                          child: InkWell(
                            onTap: () async {
                              Navigator.pop(ctx);
                              await _selectPlayer(_players.indexOf(p));
                            },
                            focusColor: Colors.white.withOpacity(0.14),
                            child: Padding(
                              padding:
                                  const EdgeInsets.fromLTRB(14, 10, 6, 10),
                              child: Row(
                          children: [
                            PlayerAvatar(name: p, size: 46),
                            const SizedBox(width: 14),
                            Expanded(
                              child: Column(
                                crossAxisAlignment:
                                    CrossAxisAlignment.start,
                                children: [
                                    Text(
                                      p,
                                      style: const TextStyle(
                                        fontSize: 20,
                                        fontWeight: FontWeight.bold,
                                      ),
                                    ),
                                    Text(
                                      p == _currentPlayer
                                          ? 'Playing now · tap to keep playing'
                                          : 'Tap to play as $p',
                                      style: const TextStyle(
                                        fontSize: 13.5,
                                        color: Colors.white54,
                                      ),
                                    ),
                                ],
                              ),
                            ),
                            Column(
                              crossAxisAlignment: CrossAxisAlignment.end,
                              children: [
                                Row(
                                  mainAxisSize: MainAxisSize.min,
                                  children: [
                                    const Icon(Icons.sports_esports,
                                        size: 18, color: Colors.white54),
                                    const SizedBox(width: 6),
                                    Container(
                                      constraints:
                                          const BoxConstraints(maxWidth: 170),
                                      child: Text(
                                        controllerLabelFor(p),
                                        overflow: TextOverflow.ellipsis,
                                        style: const TextStyle(
                                            fontSize: 14.5),
                                      ),
                                    ),
                                  ],
                                ),
                                TextButton(
                                  onPressed: () =>
                                      _assignController(p, refresh),
                                  child: Text(
                                    _controllerMap.containsKey(p)
                                        ? 'Change controller'
                                        : 'Assign controller',
                                    style: const TextStyle(fontSize: 14.5),
                                  ),
                                ),
                              ],
                            ),
                          ],
                        ),
                      ),
                      ),
                      ),
                      ),
                      ),
                ],
              ),
            ),
            actions: [
              TextButton(
                onPressed: () async {
                  await _addPlayer();
                  refresh();
                },
                child: const Text('Add player'),
              ),
              TextButton(
                onPressed: () async {
                  await _managePlayers();
                  refresh();
                },
                child: const Text('Manage'),
              ),
              TextButton(
                onPressed: () => Navigator.pop(ctx),
                child: const Text('Done'),
              ),
            ],
          );
        },
      ),
    );
    _refocusHome();
  }

  Future<void> _addPlayer() async {
    final name = await showTvKeyboard(context, title: 'Add a player');
    _refocusHome();
    final clean = (name ?? '').trim().replaceAll('\n', ' ');
    if (clean.isEmpty || _players.contains(clean)) return;
    final players = [..._players, clean];
    await Prefs.set('players', players.join('\n'));
    setState(() => _players = players);
    await _selectPlayer(players.length - 1);
  }

  @override
  void dispose() {
    _bannerTimer?.cancel();
    _channel.setMethodCallHandler(null);
    _gridScroll.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  bool _isSelect(KeyEvent event) =>
      event.logicalKey == LogicalKeyboardKey.enter ||
      event.logicalKey == LogicalKeyboardKey.select ||
      event.logicalKey == LogicalKeyboardKey.space ||
      event.logicalKey == LogicalKeyboardKey.gameButtonA;

  final Map<int, GlobalKey> _cardKeys = {};

  GlobalKey _cardKey(int index) =>
      _cardKeys.putIfAbsent(index, () => GlobalKey());

  // The systems grid shows exactly two full rows (one page of 6) and
  // never a cut card: the layout sizes cards to fit two rows exactly,
  // and moving the selector snaps the scroll position to the page the
  // selection lives on. _rowStride is set by the grid's LayoutBuilder.
  double? _rowStride;

  void _scrollSystemsTo(int index) {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_gridScroll.hasClients) return;
      final position = _gridScroll.position;
      final stride = _rowStride;
      if (stride == null || stride <= 0) return;
      // Page p starts at row 2p; its scroll offset is 2p row strides,
      // which the exact-fit layout makes equal to maxScrollExtent on
      // the last page, so pages land perfectly every time.
      final target = ((index ~/ 6) * 2 * stride).clamp(
        0.0,
        position.maxScrollExtent,
      );
      if ((position.pixels - target).abs() > 1) {
        _gridScroll.animateTo(
          target,
          duration: const Duration(milliseconds: 300),
          curve: Curves.easeOut,
        );
      }
    });
  }

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    // Frozen background: while any dialog or screen sits on top of
    // home, the remote belongs to the foreground, not to this screen.
    if (ModalRoute.of(context)?.isCurrent == false) return;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowDown) {
      setState(() {
        if (_zone == _zoneSettings) {
          _zone = _zonePlayers;
        } else if (_zone == _zonePlayers) {
          _zone = _zoneSystems;
        } else if (_systemIndex + 3 < kSystems.length) {
          _systemIndex += 3;
          _scrollSystemsTo(_systemIndex);
        }
      });
    } else if (key == LogicalKeyboardKey.arrowUp) {
      setState(() {
        if (_zone == _zoneSystems) {
          if (_systemIndex >= 3) {
            _systemIndex -= 3;
            _scrollSystemsTo(_systemIndex);
          } else {
            _zone = _zonePlayers;
          }
        } else if (_zone == _zonePlayers) {
          _zone = _zoneSettings;
        }
      });
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() {
        if (_zone == _zoneSettings) {
          _topIndex = 0;
        } else if (_zone == _zoneSystems && _systemIndex > 0) {
          _systemIndex -= 1;
          _scrollSystemsTo(_systemIndex);
        }
        // Players zone is a single chip now: no left/right scrolling.
      });
    } else if (key == LogicalKeyboardKey.arrowRight) {
      setState(() {
        if (_zone == _zoneSettings) {
          _topIndex = 1;
        } else if (_zone == _zoneSystems &&
            _systemIndex < kSystems.length - 1) {
          _systemIndex += 1;
          _scrollSystemsTo(_systemIndex);
        }
      });
    } else if (_isSelect(event)) {
      if (_zone == _zoneSettings) {
        if (_topIndex == 0) {
          _openLeaderboard();
        } else {
          _openSettings();
        }
      } else if (_zone == _zonePlayers) {
        _openPlayerPicker();
      } else {
        _openGames(kSystems[_systemIndex]);
      }
    }
  }

  void _openGames(SystemInfo system) {
    if (_locked(system)) {
      _infoDialog(
        '${system.name} needs a stronger box',
        'This system is 64-bit, and this device is 32-bit, so it '
        'cannot run these games. On a 64-bit TV box or a PC, this '
        'same app unlocks ${system.name} by itself.',
      );
      return;
    }
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => GameGridScreen(system: system)),
    ).then((_) => _focusNode.requestFocus());
  }

  void _openSettings() {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => const SettingsScreen()),
    ).then((_) => _focusNode.requestFocus());
  }

  void _openLeaderboard() {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => const LeaderboardScreen()),
    ).then((_) => _focusNode.requestFocus());
  }

  // One chip: the current player. Selecting it opens the player
  // list, where you switch players and assign controllers.
  Widget _currentPlayerChip() {
    final focused = _zone == _zonePlayers;
    final name = _currentPlayer.isEmpty ? 'Player 1' : _currentPlayer;
    final padName = controllerLabelFor(name);
    return GestureDetector(
      onTap: () {
        setState(() => _zone = _zonePlayers);
        _openPlayerPicker();
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 150),
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        decoration: BoxDecoration(
          color: const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: focused ? Colors.white : const Color(0xFF00E676),
            width: focused ? 3 : 2,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            PlayerAvatar(name: name, size: 38),
            const SizedBox(width: 10),
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Text(
                      name,
                      style: const TextStyle(
                        fontSize: 20,
                        fontWeight: FontWeight.bold,
                        color: Colors.white,
                      ),
                    ),
                    const SizedBox(width: 7),
                    const Icon(Icons.check_circle,
                        size: 19, color: Color(0xFF00E676)),
                  ],
                ),
                Text(
                  padName == 'No controller'
                      ? 'Tap to switch player'
                      : 'Pad: $padName · tap to switch',
                  style:
                      const TextStyle(fontSize: 12.5, color: Colors.white54),
                ),
              ],
            ),
            const SizedBox(width: 8),
            const Icon(Icons.expand_more, color: Colors.white54),
          ],
        ),
      ),
    );
  }

  static const _greyMatrix = <double>[
    0.33, 0.59, 0.11, 0, 0, //
    0.33, 0.59, 0.11, 0, 0,
    0.33, 0.59, 0.11, 0, 0,
    0, 0, 0, 1, 0,
  ];

  Widget _systemCard(int index) {
    final sys = kSystems[index];
    final focused = _zone == _zoneSystems && _systemIndex == index;
    final locked = _locked(sys);
    final img = _systemImages[sys.id];
    final count = _gameCounts[sys.id];
    Widget art = img != null
        ? Image.file(
            img,
            fit: BoxFit.contain,
            errorBuilder: (_, __, ___) => _consoleFallback(sys),
          )
        : _consoleFallback(sys);
    if (locked) {
      art = ColorFiltered(
        colorFilter: const ColorFilter.matrix(_greyMatrix),
        child: art,
      );
    }
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = _zoneSystems;
          _systemIndex = index;
        });
        _scrollSystemsTo(index);
        _openGames(sys);
      },
      child: AnimatedScale(
        scale: focused ? 1.09 : 1.0,
        duration: const Duration(milliseconds: 140),
        child: Opacity(
          opacity: locked ? 0.5 : 1.0,
          child: Container(
            key: _cardKey(index),
            decoration: BoxDecoration(
              color: const Color(0xFF1B1B28),
              borderRadius: BorderRadius.circular(20),
              border: Border.all(
                color: focused ? Colors.white : Colors.white12,
                width: focused ? 3.5 : 1,
              ),
              boxShadow: focused
                  ? [
                      BoxShadow(
                        color: sys.color.withOpacity(0.65),
                        blurRadius: 38,
                        spreadRadius: 5,
                      ),
                      const BoxShadow(
                        color: Colors.white24,
                        blurRadius: 12,
                      ),
                    ]
                  : null,
            ),
            clipBehavior: Clip.antiAlias,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Expanded(
                  child: Stack(
                    children: [
                      Container(
                        decoration: const BoxDecoration(
                          gradient: LinearGradient(
                            begin: Alignment.topCenter,
                            end: Alignment.bottomCenter,
                            colors: [Color(0xFF2B2B45), Color(0xFF14141F)],
                          ),
                        ),
                        padding: const EdgeInsets.all(6),
                        child: SizedBox.expand(child: art),
                      ),
                      if (locked)
                        const Positioned(
                          top: 10,
                          right: 12,
                          child: Chip(
                            avatar: Icon(Icons.lock, size: 15),
                            label: Text('64-BIT',
                                style: TextStyle(fontSize: 12)),
                            visualDensity: VisualDensity.compact,
                          ),
                        ),
                    ],
                  ),
                ),
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 16, vertical: 7),
                  decoration: BoxDecoration(
                    gradient: LinearGradient(
                      colors: [sys.color.withOpacity(0.85), sys.color],
                    ),
                  ),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        sys.name,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                          fontSize: 21,
                          fontWeight: FontWeight.bold,
                          color: Colors.white,
                        ),
                      ),
                      const SizedBox(height: 2),
                      Row(
                        children: [
                          Expanded(
                            child: Text(
                              locked
                                  ? 'Needs a 64-bit device'
                                  : sys.tagline,
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: const TextStyle(
                                  fontSize: 13.5, color: Colors.white70),
                            ),
                          ),
                          if (count != null)
                            Text(
                              count == 0 ? 'No games' : '$count games',
                              style: const TextStyle(
                                  fontSize: 14.5, color: Colors.white),
                            ),
                        ],
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _consoleFallback(SystemInfo sys) {
    return Center(
      child: Icon(Icons.videogame_asset, size: 84, color: sys.color),
    );
  }

  Widget _topChip({
    required IconData icon,
    required String label,
    required bool focused,
    required VoidCallback onTap,
  }) {
    return GestureDetector(
      onTap: onTap,
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 140),
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 10),
        decoration: BoxDecoration(
          color: const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: focused ? Colors.white : Colors.white24,
            width: focused ? 3 : 2,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 22),
            const SizedBox(width: 8),
            Text(label, style: const TextStyle(fontSize: 19)),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final settingsFocused = _zone == _zoneSettings;
    return Scaffold(
      body: KeyboardListener(
        focusNode: _focusNode,
        onKeyEvent: _onKey,
        child: Container(
          decoration: const BoxDecoration(
            gradient: LinearGradient(
              begin: Alignment.topLeft,
              end: Alignment.bottomRight,
              colors: [
                Color(0xFF2A1152),
                Color(0xFF140B2E),
                Color(0xFF0A1A33),
              ],
            ),
          ),
          child: Stack(
            children: [
              // Soft color glows in the corners.
              Positioned(
                top: -140,
                left: -100,
                child: Container(
                  width: 420,
                  height: 420,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: const Color(0xFF7B1FA2).withOpacity(0.20),
                  ),
                ),
              ),
              Positioned(
                bottom: -160,
                right: -80,
                child: Container(
                  width: 460,
                  height: 460,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: const Color(0xFF1565C0).withOpacity(0.18),
                  ),
                ),
              ),
              Positioned(
                bottom: -120,
                left: 420,
                child: Container(
                  width: 300,
                  height: 300,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: const Color(0xFF00E676).withOpacity(0.07),
                  ),
                ),
              ),
              Column(
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(58, 26, 58, 0),
                    child: Row(
                      children: [
                        const Text(
                          'iTaK',
                          style: TextStyle(
                            fontSize: 46,
                            fontWeight: FontWeight.w800,
                            color: Colors.white,
                            letterSpacing: 1.0,
                          ),
                        ),
                        const Text(
                          ' Gaming',
                          style: TextStyle(
                            fontSize: 46,
                            fontWeight: FontWeight.w800,
                            color: Color(0xFF00E676),
                            letterSpacing: 1.0,
                          ),
                        ),
                        const Spacer(),
                        _topChip(
                          icon: Icons.emoji_events,
                          label: 'Leaderboard',
                          focused: settingsFocused && _topIndex == 0,
                          onTap: () {
                            setState(() {
                              _zone = _zoneSettings;
                              _topIndex = 0;
                            });
                            _openLeaderboard();
                          },
                        ),
                        const SizedBox(width: 12),
                        _topChip(
                          icon: Icons.settings,
                          label: 'Settings',
                          focused: settingsFocused && _topIndex == 1,
                          onTap: () {
                            setState(() {
                              _zone = _zoneSettings;
                              _topIndex = 1;
                            });
                            _openSettings();
                          },
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(height: 14),
                  if (_banner != null)
                    Padding(
                      padding: const EdgeInsets.only(bottom: 8),
                      child: Text(
                        _banner!,
                        style: const TextStyle(
                          fontSize: 19,
                          color: Color(0xFF00E676),
                          fontWeight: FontWeight.bold,
                        ),
                      ),
                    ),
                  Row(
                    children: [
                      const SizedBox(width: 58),
                      const Text(
                        "Who's playing?",
                        style: TextStyle(fontSize: 20, color: Colors.white70),
                      ),
                      const SizedBox(width: 16),
                      _currentPlayerChip(),
                      const Spacer(),
                      const SizedBox(width: 34),
                    ],
                  ),
                  const SizedBox(height: 18),
                  Expanded(
                    child: LayoutBuilder(
                      builder: (context, constraints) {
                        // Exactly two full rows on screen, sized from
                        // the real space: card height is whatever
                        // remains after the grid's top/bottom padding
                        // (16 each) and the one gap between the rows.
                        final gridW = constraints.maxWidth - 80;
                        final cardW = (gridW - 60 - 44) / 3;
                        final cardH =
                            (constraints.maxHeight - 16 - 16 - 22) / 2;
                        final aspect =
                            cardH > 40 ? cardW / cardH : 1.52;
                        _rowStride = cardH + 22;
                        return Padding(
                          padding:
                              const EdgeInsets.symmetric(horizontal: 40),
                          child: GridView.builder(
                            controller: _gridScroll,
                            physics:
                                const NeverScrollableScrollPhysics(),
                            padding:
                                const EdgeInsets.fromLTRB(30, 16, 30, 16),
                            gridDelegate:
                                SliverGridDelegateWithFixedCrossAxisCount(
                              crossAxisCount: 3,
                              crossAxisSpacing: 22,
                              mainAxisSpacing: 22,
                              childAspectRatio: aspect,
                            ),
                            itemCount: kSystems.length,
                            itemBuilder: (context, index) =>
                                _systemCard(index),
                          ),
                        );
                      },
                    ),
                  ),
                  const SizedBox(height: 14),
                  const Text(
                    'UP / DOWN switch rows · LEFT / RIGHT browse · OK to select · In game, BACK opens the menu',
                    style: TextStyle(fontSize: 16.5, color: Colors.white54),
                  ),
                  const SizedBox(height: 22),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Settings.
// ---------------------------------------------------------------------------

const List<String> kFilterNames = ['Sharp', 'Smooth', 'Pixel Perfect'];
const List<String> kDisplayNames = ['Smooth 60', 'Steady 30'];

class SettingsScreen extends StatefulWidget {
  const SettingsScreen({super.key});

  @override
  State<SettingsScreen> createState() => _SettingsScreenState();
}

class _SettingsScreenState extends State<SettingsScreen> {
  int _row = 0;
  int _filter = 0;
  int _display = 0;
  bool _swapAB = false;
  bool _autoArt = true;
  final FocusNode _focusNode = FocusNode();

  static const int _rowCount = 5; // Picture, Display, Swap, Auto art, Back

  @override
  void initState() {
    super.initState();
    _load();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  Future<void> _load() async {
    final f = await Prefs.getInt('filter_mode');
    final d = await Prefs.getInt('display_mode');
    final sw = await Prefs.getInt('swap_ab');
    final a = await Prefs.get('art_autofetch');
    if (!mounted) return;
    setState(() {
      _filter = (f ?? 0).clamp(0, kFilterNames.length - 1);
      _display = (d ?? 0).clamp(0, kDisplayNames.length - 1);
      _swapAB = (sw ?? 0) != 0;
      _autoArt = a != '0';
    });
  }

  @override
  void dispose() {
    _focusNode.dispose();
    super.dispose();
  }

  void _adjust(int delta) {
    if (_row == 0) {
      final v = (_filter + delta + kFilterNames.length) % kFilterNames.length;
      setState(() => _filter = v);
      Prefs.setInt('filter_mode', v);
    } else if (_row == 1) {
      final v = (_display + delta + kDisplayNames.length) % kDisplayNames.length;
      setState(() => _display = v);
      Prefs.setInt('display_mode', v);
    } else if (_row == 2) {
      setState(() => _swapAB = !_swapAB);
      Prefs.setInt('swap_ab', _swapAB ? 1 : 0);
    } else if (_row == 3) {
      setState(() => _autoArt = !_autoArt);
      Prefs.set('art_autofetch', _autoArt ? '1' : '0');
    }
  }

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowUp) {
      setState(() => _row = (_row - 1 + _rowCount) % _rowCount);
    } else if (key == LogicalKeyboardKey.arrowDown) {
      setState(() => _row = (_row + 1) % _rowCount);
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      _adjust(-1);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      _adjust(1);
    } else if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.select ||
        key == LogicalKeyboardKey.space) {
      if (_row == 4) {
        Navigator.pop(context);
      } else {
        _adjust(1);
      }
    }
  }

  Widget _settingRow({
    required int index,
    required String label,
    required String value,
    required String hint,
  }) {
    final focused = _row == index;
    return GestureDetector(
      onTap: () {
        setState(() => _row = index);
        _adjust(1);
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 120),
        margin: const EdgeInsets.symmetric(vertical: 7),
        padding: const EdgeInsets.symmetric(horizontal: 22, vertical: 16),
        decoration: BoxDecoration(
          color: const Color(0xFF1B1B28),
          borderRadius: BorderRadius.circular(14),
          border: Border.all(
            color: focused ? Colors.white : Colors.white12,
            width: focused ? 3 : 1,
          ),
        ),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(label,
                      style: const TextStyle(
                          fontSize: 22, fontWeight: FontWeight.bold)),
                  Text(hint,
                      style: const TextStyle(
                          fontSize: 15, color: Colors.white54)),
                ],
              ),
            ),
            const Icon(Icons.chevron_left, color: Colors.white54),
            SizedBox(
              width: 190,
              child: Text(
                value,
                textAlign: TextAlign.center,
                style: const TextStyle(
                    fontSize: 21, color: Color(0xFF00E676)),
              ),
            ),
            const Icon(Icons.chevron_right, color: Colors.white54),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Settings'),
        backgroundColor: Colors.transparent,
      ),
      body: KeyboardListener(
        focusNode: _focusNode,
        onKeyEvent: _onKey,
        child: Container(
          decoration: const BoxDecoration(
            gradient: LinearGradient(
              begin: Alignment.topLeft,
              end: Alignment.bottomRight,
              colors: [Color(0xFF2A1152), Color(0xFF140B2E), Color(0xFF0A1A33)],
            ),
          ),
          child: Center(
            child: SizedBox(
              width: 860,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  _settingRow(
                    index: 0,
                    label: 'Picture',
                    value: kFilterNames[_filter],
                    hint: 'Sharp keeps pixels crisp, Smooth softens, Pixel Perfect is raw',
                  ),
                  _settingRow(
                    index: 1,
                    label: 'Display',
                    value: kDisplayNames[_display],
                    hint: 'Steady 30 draws an even 30 frames a second; the game always runs full speed',
                  ),
                  _settingRow(
                    index: 2,
                    label: 'Swap A and B buttons',
                    value: _swapAB ? 'On' : 'Off',
                    hint: 'Trade the two main buttons if a controller feels backwards',
                  ),
                  _settingRow(
                    index: 3,
                    label: 'Download box art',
                    value: _autoArt ? 'On' : 'Off',
                    hint: 'Fetches cover art automatically when a new game shows up',
                  ),
                  GestureDetector(
                    onTap: () => Navigator.pop(context),
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 120),
                      margin: const EdgeInsets.symmetric(vertical: 7),
                      padding: const EdgeInsets.symmetric(
                          horizontal: 22, vertical: 16),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1B1B28),
                        borderRadius: BorderRadius.circular(14),
                        border: Border.all(
                          color:
                              _row == 4 ? Colors.white : Colors.white12,
                          width: _row == 4 ? 3 : 1,
                        ),
                      ),
                      child: const Row(
                        children: [
                          Icon(Icons.arrow_back),
                          SizedBox(width: 12),
                          Text('Back',
                              style: TextStyle(
                                  fontSize: 22, fontWeight: FontWeight.bold)),
                        ],
                      ),
                    ),
                  ),
                  const SizedBox(height: 10),
                  const Text(
                    'Changes apply the next time a game starts. You can also change Picture and Display mid-game from the pause menu.',
                    textAlign: TextAlign.center,
                    style: TextStyle(fontSize: 15, color: Colors.white54),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Game grid with search and sorting.
// ---------------------------------------------------------------------------

const List<String> kSortNames = ['A to Z', 'Z to A', 'Most played', 'Recent'];

class GameGridScreen extends StatefulWidget {
  final SystemInfo system;

  const GameGridScreen({super.key, required this.system});

  @override
  State<GameGridScreen> createState() => _GameGridScreenState();
}

class _GameGridScreenState extends State<GameGridScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  List<File> _games = [];
  final Map<String, File?> _art = {};
  bool _loading = true;
  String _status = '';
  int _focused = 0;
  String _query = '';
  int _sort = 0;

  @override
  void initState() {
    super.initState();
    _init();
  }

  Future<void> _openSearch() async {
    final text = await showTvKeyboard(
      context,
      title: 'Search games',
      initial: _query,
    );
    if (text == null || !mounted) return;
    setState(() {
      _query = text.trim();
      _focused = 0;
    });
  }

  Future<void> _init() async {
    await PlayStats.load();
    final s = await Prefs.get('sort_mode');
    if (s != null) _sort = (int.tryParse(s) ?? 0).clamp(0, kSortNames.length - 1);
    await _scan();
  }

  Future<void> _scan() async {
    final sys = widget.system;
    final dir = Directory('/sdcard/ROMs/${sys.dir}');
    final found = <File>[];
    try {
      if (await dir.exists()) {
        await for (final e in dir.list()) {
          if (e is File) {
            final lower = e.path.toLowerCase();
            if (sys.exts.any((x) => lower.endsWith(x))) found.add(e);
          }
        }
      }
    } catch (err) {
      setState(() {
        _loading = false;
        _status =
            'Cannot read games folder. Grant "All files access" to iTaK Gaming in Settings.';
      });
      return;
    }
    found.sort((a, b) => a.path.compareTo(b.path));
    // Cached art first so the grid paints instantly.
    for (final f in found) {
      final art = File('${dir.path}/boxart/${_displayName(f)}.png');
      _art[f.path] = art.existsSync() ? art : null;
    }
    setState(() {
      _games = found;
      _loading = false;
      _status = found.isEmpty ? 'No games found in ${dir.path}' : '';
    });
    // Then fetch anything missing in the background.
    final autoArt = (await Prefs.get('art_autofetch')) != '0';
    if (!autoArt) return;
    for (final f in found) {
      if (_art[f.path] == null) {
        final fetched =
            await Artwork.boxart(sys, _displayName(f), dir.path);
        if (fetched != null && mounted) {
          setState(() => _art[f.path] = fetched);
        }
      }
    }
  }

  String _displayName(File f) {
    var name = f.uri.pathSegments.last;
    final dot = name.lastIndexOf('.');
    if (dot > 0) name = name.substring(0, dot);
    return name;
  }

  List<File> get _visible {
    var list = _games.where((f) {
      return _query.isEmpty ||
          _displayName(f).toLowerCase().contains(_query.toLowerCase());
    }).toList();
    switch (_sort) {
      case 1:
        list.sort((a, b) => _displayName(b).compareTo(_displayName(a)));
        break;
      case 2:
        list.sort((a, b) {
          final c = PlayStats.count(b.path).compareTo(PlayStats.count(a.path));
          return c != 0 ? c : _displayName(a).compareTo(_displayName(b));
        });
        break;
      case 3:
        list.sort((a, b) {
          final c = PlayStats.lastPlayed(b.path)
              .compareTo(PlayStats.lastPlayed(a.path));
          return c != 0 ? c : _displayName(a).compareTo(_displayName(b));
        });
        break;
      default:
        list.sort((a, b) => _displayName(a).compareTo(_displayName(b)));
    }
    return list;
  }

  Future<void> _launch(File f) async {
    await PlayStats.record(f.path);
    await _channel.invokeMethod('launch', {
      'core': widget.system.core,
      'rom': f.path,
    });
  }

  void _cycleSort(int delta) {
    setState(() {
      _sort = (_sort + delta + kSortNames.length) % kSortNames.length;
      _focused = 0;
    });
    Prefs.set('sort_mode', _sort.toString());
  }

  Widget _tile(int index, List<File> visible) {
    final game = visible[index];
    final art = _art[game.path];
    final focused = _focused == index;
    final sys = widget.system;
    return Focus(
      autofocus: index == 0,
      onFocusChange: (hasFocus) {
        if (hasFocus) setState(() => _focused = index);
      },
      onKeyEvent: (node, event) {
        if (event is KeyDownEvent &&
            (event.logicalKey == LogicalKeyboardKey.enter ||
                event.logicalKey == LogicalKeyboardKey.select ||
                event.logicalKey == LogicalKeyboardKey.space ||
                event.logicalKey == LogicalKeyboardKey.gameButtonA)) {
          _launch(game);
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: GestureDetector(
        onTap: () => _launch(game),
        child: AnimatedScale(
          scale: focused ? 1.09 : 1.0,
          duration: const Duration(milliseconds: 120),
          child: Container(
            decoration: BoxDecoration(
              color: const Color(0xFF1E1E2A),
              borderRadius: BorderRadius.circular(12),
              border: Border.all(
                color: focused ? Colors.white : Colors.white10,
                width: focused ? 3.5 : 1,
              ),
              boxShadow: focused
                  ? [
                      BoxShadow(
                        color: sys.color.withOpacity(0.6),
                        blurRadius: 30,
                        spreadRadius: 3,
                      ),
                    ]
                  : null,
            ),
            clipBehavior: Clip.antiAlias,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Expanded(
                  child: art != null
                      ? Image.file(
                          art,
                          // Whole cover visible; covers differ per
                          // system (SNES landscape, NES portrait).
                          fit: BoxFit.contain,
                          errorBuilder: (_, __, ___) => _artFallback(),
                        )
                      : _artFallback(),
                ),
                Container(
                  color: const Color(0xFF14141E),
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
                  child: Text(
                    _displayName(game),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    textAlign: TextAlign.center,
                    style: TextStyle(
                      fontSize: 16,
                      fontWeight: focused ? FontWeight.bold : FontWeight.normal,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _artFallback() {
    return Container(
      color: widget.system.color.withOpacity(0.22),
      child: Icon(
        Icons.sports_esports,
        size: 56,
        color: widget.system.color.withOpacity(0.85),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final visible = _visible;
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.system.name),
        backgroundColor: Colors.transparent,
      ),
      body: Container(
        decoration: const BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topLeft,
            end: Alignment.bottomRight,
            colors: [Color(0xFF22103F), Color(0xFF120826), Color(0xFF081426)],
          ),
        ),
        child: _loading
            ? const Center(child: CircularProgressIndicator())
            : Column(
                children: [
                  Padding(
                    padding: const EdgeInsets.fromLTRB(24, 6, 24, 4),
                    child: Row(
                      children: [
                        Expanded(
                          child: Focus(
                            onKeyEvent: (node, event) {
                              if (event is KeyDownEvent &&
                                  (event.logicalKey == LogicalKeyboardKey.enter ||
                                      event.logicalKey ==
                                          LogicalKeyboardKey.select ||
                                      event.logicalKey ==
                                          LogicalKeyboardKey.gameButtonA)) {
                                _openSearch();
                                return KeyEventResult.handled;
                              }
                              return KeyEventResult.ignored;
                            },
                            child: Builder(builder: (ctx) {
                              final barFocused = Focus.of(ctx).hasFocus;
                              return GestureDetector(
                                onTap: _openSearch,
                                child: Container(
                                  padding: const EdgeInsets.symmetric(
                                      horizontal: 16, vertical: 13),
                                  decoration: BoxDecoration(
                                    color: const Color(0xFF1B1B28),
                                    borderRadius: BorderRadius.circular(12),
                                    border: Border.all(
                                      color: barFocused
                                          ? Colors.white
                                          : Colors.white12,
                                      width: barFocused ? 3 : 1,
                                    ),
                                  ),
                                  child: Row(
                                    children: [
                                      const Icon(Icons.search, size: 21),
                                      const SizedBox(width: 10),
                                      Expanded(
                                        child: Text(
                                          _query.isEmpty
                                              ? 'Search games...'
                                              : _query,
                                          style: TextStyle(
                                            fontSize: 19,
                                            color: _query.isEmpty
                                                ? Colors.white38
                                                : Colors.white,
                                          ),
                                        ),
                                      ),
                                      if (_query.isNotEmpty)
                                        GestureDetector(
                                          onTap: () => setState(() {
                                            _query = '';
                                            _focused = 0;
                                          }),
                                          child: const Icon(Icons.clear,
                                              size: 21),
                                        ),
                                    ],
                                  ),
                                ),
                              );
                            }),
                          ),
                        ),
                        const SizedBox(width: 16),
                        Focus(
                          onKeyEvent: (node, event) {
                            if (event is KeyDownEvent) {
                              if (event.logicalKey ==
                                  LogicalKeyboardKey.arrowRight) {
                                _cycleSort(1);
                                return KeyEventResult.handled;
                              }
                              if (event.logicalKey ==
                                  LogicalKeyboardKey.arrowLeft) {
                                _cycleSort(-1);
                                return KeyEventResult.handled;
                              }
                              if (event.logicalKey == LogicalKeyboardKey.enter ||
                                  event.logicalKey ==
                                      LogicalKeyboardKey.select ||
                                  event.logicalKey ==
                                      LogicalKeyboardKey.gameButtonA) {
                                _cycleSort(1);
                                return KeyEventResult.handled;
                              }
                            }
                            return KeyEventResult.ignored;
                          },
                          child: Builder(builder: (ctx) {
                            final chipFocused = Focus.of(ctx).hasFocus;
                            return GestureDetector(
                              onTap: () => _cycleSort(1),
                              child: Container(
                                padding: const EdgeInsets.symmetric(
                                    horizontal: 16, vertical: 12),
                                decoration: BoxDecoration(
                                  color: const Color(0xFF1B1B28),
                                  borderRadius: BorderRadius.circular(12),
                                  border: Border.all(
                                    color: chipFocused
                                        ? Colors.white
                                        : Colors.white12,
                                    width: chipFocused ? 3 : 1,
                                  ),
                                ),
                                child: Row(
                                  mainAxisSize: MainAxisSize.min,
                                  children: [
                                    const Icon(Icons.sort, size: 20),
                                    const SizedBox(width: 8),
                                    Text('Sort: ${kSortNames[_sort]}',
                                        style: const TextStyle(fontSize: 18)),
                                  ],
                                ),
                              ),
                            );
                          }),
                        ),
                      ],
                    ),
                  ),
                  Expanded(
                    child: _games.isEmpty
                        ? Center(
                            child: Padding(
                              padding: const EdgeInsets.all(32),
                              child: Text(
                                _status,
                                textAlign: TextAlign.center,
                                style: const TextStyle(
                                    fontSize: 22, color: Colors.grey),
                              ),
                            ),
                          )
                        : visible.isEmpty
                            ? const Center(
                                child: Text(
                                  'No games match your search',
                                  style: TextStyle(
                                      fontSize: 22, color: Colors.grey),
                                ),
                              )
                            : LayoutBuilder(
                                builder: (context, constraints) {
                                  // Whole rows only: pick the largest
                                  // row count whose tiles stay near
                                  // their natural shape, then size the
                                  // tiles so exactly that many rows
                                  // fill the screen. Combined with zero
                                  // vertical grid padding, the focus
                                  // auto-scroll always settles on a row
                                  // boundary, so no tile is ever cut.
                                  final tileW = (constraints.maxWidth -
                                          28 -
                                          28 -
                                          3 * 20) /
                                      4;
                                  var rows = 1;
                                  for (var r = 6; r >= 1; r--) {
                                    final th = (constraints.maxHeight -
                                            (r - 1) * 22) /
                                        r;
                                    if (th >= tileW / 1.30) {
                                      rows = r;
                                      break;
                                    }
                                  }
                                  final tileH = (constraints.maxHeight -
                                          (rows - 1) * 22) /
                                      rows;
                                  final aspect =
                                      tileH > 40 ? tileW / tileH : 0.92;
                                  return GridView.builder(
                                    padding: const EdgeInsets.fromLTRB(
                                        28, 0, 28, 0),
                                    gridDelegate:
                                        SliverGridDelegateWithFixedCrossAxisCount(
                                      crossAxisCount: 4,
                                      crossAxisSpacing: 20,
                                      mainAxisSpacing: 22,
                                      childAspectRatio: aspect,
                                    ),
                                    itemCount: visible.length,
                                    itemBuilder: (context, index) =>
                                        _tile(index, visible),
                                  );
                                },
                              ),
                  ),
                  const SizedBox(height: 12),
                ],
              ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Leaderboard: per-player play stats written by EmulatorActivity into
// files/stats/<player>.json and read back through the channel.
// ---------------------------------------------------------------------------

class _GameStat {
  final String rom;
  final int plays;
  final int seconds;
  final int last;
  const _GameStat(this.rom, this.plays, this.seconds, this.last);
  String get name {
    var n = rom.split('/').last;
    final dot = n.lastIndexOf('.');
    if (dot > 0) n = n.substring(0, dot);
    return n;
  }
}

class _PlayerStat {
  final String name;
  final List<_GameStat> games;
  const _PlayerStat(this.name, this.games);
  int get totalSeconds => games.fold(0, (a, g) => a + g.seconds);
  int get totalPlays => games.fold(0, (a, g) => a + g.plays);
  _GameStat? get favorite => games.isEmpty
      ? null
      : games.reduce((a, b) => a.seconds >= b.seconds ? a : b);
  _GameStat? get mostRecent => games.isEmpty
      ? null
      : games.reduce((a, b) => a.last >= b.last ? a : b);
}

String _fmtDuration(int seconds) {
  if (seconds >= 3600) {
    final h = seconds ~/ 3600;
    final m = (seconds % 3600) ~/ 60;
    return '${h}h ${m}m';
  }
  if (seconds >= 60) return '${seconds ~/ 60}m';
  return '${seconds}s';
}

String _fmtWhen(int millis) {
  if (millis <= 0) return '';
  const months = [
    'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
    'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
  ];
  final d = DateTime.fromMillisecondsSinceEpoch(millis);
  var h = d.hour % 12;
  if (h == 0) h = 12;
  final ampm = d.hour < 12 ? 'AM' : 'PM';
  final mm = d.minute.toString().padLeft(2, '0');
  return '${months[d.month - 1]} ${d.day} · $h:$mm $ampm';
}

class LeaderboardScreen extends StatefulWidget {
  const LeaderboardScreen({super.key});

  @override
  State<LeaderboardScreen> createState() => _LeaderboardScreenState();
}

class _LeaderboardScreenState extends State<LeaderboardScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  static const _modes = ['Players', 'Top Games', 'Most Played', 'Last Played'];
  List<_PlayerStat> _players = [];
  bool _loading = true;
  int _mode = 0;
  final FocusNode _focusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _load();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  @override
  void dispose() {
    _focusNode.dispose();
    super.dispose();
  }

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    if (event.logicalKey == LogicalKeyboardKey.arrowLeft) {
      setState(() => _mode = (_mode - 1 + _modes.length) % _modes.length);
    } else if (event.logicalKey == LogicalKeyboardKey.arrowRight) {
      setState(() => _mode = (_mode + 1) % _modes.length);
    }
  }

  Future<void> _load() async {
    var players = <_PlayerStat>[];
    try {
      final raw =
          await _channel.invokeMethod<String>('getLeaderboard') ?? '[]';
      final list = jsonDecode(raw) as List<dynamic>;
      for (final entry in list) {
        final map = entry as Map<String, dynamic>;
        final gamesMap =
            (map['games'] as Map<String, dynamic>? ?? <String, dynamic>{});
        final games = gamesMap.entries.map((e) {
          final g = e.value as Map<String, dynamic>;
          return _GameStat(
            e.key,
            (g['plays'] as num? ?? 0).toInt(),
            (g['seconds'] as num? ?? 0).toInt(),
            (g['last'] as num? ?? 0).toInt(),
          );
        }).toList();
        players.add(_PlayerStat(map['name']?.toString() ?? 'Player', games));
      }
    } catch (_) {
      players = [];
    }
    players.sort((a, b) => b.totalSeconds.compareTo(a.totalSeconds));
    if (!mounted) return;
    setState(() {
      _players = players;
      _loading = false;
    });
  }

  List<_GameStat> _aggGames({required bool byPlays}) {
    final byRom = <String, _GameStat>{};
    for (final p in _players) {
      for (final g in p.games) {
        final cur = byRom[g.rom];
        byRom[g.rom] = cur == null
            ? g
            : _GameStat(g.rom, cur.plays + g.plays,
                cur.seconds + g.seconds, cur.last > g.last ? cur.last : g.last);
      }
    }
    final all = byRom.values.toList();
    if (byPlays) {
      all.sort((a, b) => b.plays.compareTo(a.plays));
    } else {
      all.sort((a, b) => b.seconds.compareTo(a.seconds));
    }
    return all.take(10).toList();
  }

  Widget _playerCard(int rank, _PlayerStat p) {
    final fav = p.favorite;
    final medalColor = rank == 1
        ? const Color(0xFFFFD54F)
        : rank == 2
            ? const Color(0xFFB0BEC5)
            : rank == 3
                ? const Color(0xFFBCAAA4)
                : const Color(0xFF424242);
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 7),
      padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 16),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(14),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 24,
            backgroundColor: medalColor,
            child: Text(
              '$rank',
              style: const TextStyle(
                fontSize: 21,
                fontWeight: FontWeight.bold,
                color: Colors.black87,
              ),
            ),
          ),
          const SizedBox(width: 16),
          PlayerAvatar(name: p.name, size: 44),
          const SizedBox(width: 14),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(p.name,
                    style: const TextStyle(
                        fontSize: 23, fontWeight: FontWeight.bold)),
                Text(
                  'Time played: ${_fmtDuration(p.totalSeconds)} · '
                  'Games: ${p.games.length} · Launches: ${p.totalPlays}',
                  style:
                      const TextStyle(fontSize: 16, color: Colors.white70),
                ),
                if (_mode == 0 && fav != null)
                  Text(
                    'Favorite: ${fav.name} (${_fmtDuration(fav.seconds)})',
                    style:
                        const TextStyle(fontSize: 16, color: Colors.white54),
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Leaderboard'),
        backgroundColor: Colors.transparent,
      ),
      body: Container(
        decoration: const BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topLeft,
            end: Alignment.bottomRight,
            colors: [Color(0xFF2A1152), Color(0xFF140B2E), Color(0xFF0A1A33)],
          ),
        ),
        child: _loading
            ? const Center(child: CircularProgressIndicator())
            : _players.isEmpty
                ? const Center(
                    child: Padding(
                      padding: EdgeInsets.all(32),
                      child: Text(
                        'No play time logged yet.\nStats start counting as soon as someone plays.',
                        textAlign: TextAlign.center,
                        style: TextStyle(fontSize: 21, color: Colors.grey),
                      ),
                    ),
                  )
                : KeyboardListener(
                    focusNode: _focusNode,
                    onKeyEvent: _onKey,
                    child: Column(
                      children: [
                        const SizedBox(height: 14),
                        Wrap(
                          spacing: 10,
                          alignment: WrapAlignment.center,
                          children: [
                            for (var i = 0; i < _modes.length; i++)
                              GestureDetector(
                                onTap: () => setState(() => _mode = i),
                                child: Container(
                                  padding: const EdgeInsets.symmetric(
                                      horizontal: 20, vertical: 10),
                                  decoration: BoxDecoration(
                                    color: _mode == i
                                        ? const Color(0xFF00E676)
                                        : const Color(0xFF1E1E2E),
                                    borderRadius: BorderRadius.circular(999),
                                    border: Border.all(
                                      color: _mode == i
                                          ? Colors.white
                                          : Colors.white24,
                                      width: _mode == i ? 2.5 : 1,
                                    ),
                                  ),
                                  child: Text(
                                    _modes[i],
                                    style: TextStyle(
                                      fontSize: 18,
                                      fontWeight: FontWeight.bold,
                                      color: _mode == i
                                          ? Colors.black
                                          : Colors.white,
                                    ),
                                  ),
                                ),
                              ),
                          ],
                        ),
                        Expanded(
                          child: Center(
                            child: SizedBox(
                              width: 900,
                              child: ListView(
                                padding:
                                    const EdgeInsets.symmetric(vertical: 18),
                                children: _modeContent(),
                              ),
                            ),
                          ),
                        ),
                      ],
                    ),
                  ),
      ),
    );
  }

  List<Widget> _modeContent() {
    switch (_mode) {
      case 1: // Top Games, by time
        return [
          for (var i = 0; i < _aggGames(byPlays: false).length; i++)
            _gameRow(i + 1, _aggGames(byPlays: false)[i],
                '${_fmtDuration(_aggGames(byPlays: false)[i].seconds)} · ${_aggGames(byPlays: false)[i].plays} plays'),
        ];
      case 2: // Most Played, by launches
        return [
          for (var i = 0; i < _aggGames(byPlays: true).length; i++)
            _gameRow(i + 1, _aggGames(byPlays: true)[i],
                '${_aggGames(byPlays: true)[i].plays} plays · ${_fmtDuration(_aggGames(byPlays: true)[i].seconds)}'),
        ];
      case 3: // Last Played, newest first across players
        final entries = <(_PlayerStat, _GameStat)>[
          for (final p in _players)
            if (p.mostRecent != null) (p, p.mostRecent!),
        ]..sort((a, b) => b.$2.last.compareTo(a.$2.last));
        return [
          for (final e in entries)
            Container(
              margin: const EdgeInsets.symmetric(vertical: 6),
              padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 13),
              decoration: BoxDecoration(
                color: const Color(0xFF1B1B28),
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.white12),
              ),
              child: Row(
                children: [
                  PlayerAvatar(name: e.$1.name, size: 38),
                  const SizedBox(width: 13),
                  Text(
                    e.$1.name,
                    style: const TextStyle(
                        fontSize: 19, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(width: 14),
                  Expanded(
                    child: Text(
                      e.$2.name,
                      style: const TextStyle(fontSize: 18),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  Text(
                    _fmtWhen(e.$2.last),
                    style:
                        const TextStyle(fontSize: 16, color: Colors.white60),
                  ),
                ],
              ),
            ),
        ];
      default: // Players
        return [
          for (var i = 0; i < _players.length; i++)
            _playerCard(i + 1, _players[i]),
        ];
    }
  }

  Widget _gameRow(int rank, _GameStat g, String right) {
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 6),
      padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 14),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 17,
            backgroundColor: rank == 1
                ? const Color(0xFFFFD54F)
                : const Color(0xFF37474F),
            child: Text(
              '$rank',
              style: TextStyle(
                fontSize: 16,
                fontWeight: FontWeight.bold,
                color: rank == 1 ? Colors.black87 : Colors.white,
              ),
            ),
          ),
          const SizedBox(width: 14),
          Expanded(
            child: Text(
              g.name,
              style: const TextStyle(fontSize: 19),
              overflow: TextOverflow.ellipsis,
            ),
          ),
          Text(
            right,
            style: const TextStyle(fontSize: 17, color: Colors.white70),
          ),
        ],
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// TV keyboard: a big on-screen keyboard the remote and gamepad can drive.
// The TV's own pop-up keyboard does not take D-pad focus inside our
// dialogs, so names and search are typed here instead.
// ---------------------------------------------------------------------------

Future<String?> showTvKeyboard(
  BuildContext context, {
  required String title,
  String initial = '',
}) {
  return showDialog<String>(
    context: context,
    barrierColor: Colors.black.withOpacity(0.85),
    builder: (_) => TvKeyboardDialog(title: title, initial: initial),
  );
}

class TvKeyboardDialog extends StatefulWidget {
  final String title;
  final String initial;
  const TvKeyboardDialog({super.key, required this.title, this.initial = ''});

  @override
  State<TvKeyboardDialog> createState() => _TvKeyboardDialogState();
}

class _TvKeyboardDialogState extends State<TvKeyboardDialog> {
  static const _rows = [
    'ABCDEFGHIJ',
    'KLMNOPQRST',
    'UVWXYZ1234',
    "567890-.'",
  ];
  static const _actions = ['Space', 'Delete', 'Done', 'Cancel'];
  late String _text;
  int _row = 0;
  int _col = 0;
  final FocusNode _focusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _text = widget.initial;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  @override
  void dispose() {
    _focusNode.dispose();
    super.dispose();
  }

  int get _cols => _row < _rows.length ? _rows[_row].length : _actions.length;

  bool _isSelectKey(LogicalKeyboardKey k) =>
      k == LogicalKeyboardKey.enter ||
      k == LogicalKeyboardKey.select ||
      k == LogicalKeyboardKey.space ||
      k == LogicalKeyboardKey.gameButtonA;

  void _press() {
    if (_row < _rows.length) {
      setState(() {
        if (_text.length < 40) _text += _rows[_row][_col];
      });
      return;
    }
    switch (_col) {
      case 0: // Space
        setState(() {
          if (_text.length < 40) _text += ' ';
        });
        break;
      case 1: // Delete
        setState(() {
          if (_text.isNotEmpty) _text = _text.substring(0, _text.length - 1);
        });
        break;
      case 2: // Done
        Navigator.pop(context, _text);
        break;
      case 3: // Cancel
        Navigator.pop(context, null);
        break;
    }
  }

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    final key = event.logicalKey;
    // A real keyboard can just type.
    final ch = event.character;
    if (ch != null && ch.length == 1 && ch.codeUnitAt(0) >= 32) {
      setState(() {
        if (_text.length < 40) _text += ch;
      });
      return;
    }
    if (key == LogicalKeyboardKey.backspace) {
      setState(() {
        if (_text.isNotEmpty) _text = _text.substring(0, _text.length - 1);
      });
    } else if (key == LogicalKeyboardKey.escape ||
        key == LogicalKeyboardKey.gameButtonB) {
      Navigator.pop(context, null);
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() => _col = (_col - 1 + _cols) % _cols);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      setState(() => _col = (_col + 1) % _cols);
    } else if (key == LogicalKeyboardKey.arrowUp) {
      setState(() {
        _row = (_row - 1 + _rows.length + 1) % (_rows.length + 1);
        if (_col >= _cols) _col = _cols - 1;
      });
    } else if (key == LogicalKeyboardKey.arrowDown) {
      setState(() {
        _row = (_row + 1) % (_rows.length + 1);
        if (_col >= _cols) _col = _cols - 1;
      });
    } else if (_isSelectKey(key)) {
      _press();
    }
  }

  Widget _cell(String label, bool focused, {bool wide = false}) {
    return AnimatedContainer(
      duration: const Duration(milliseconds: 100),
      width: wide ? 150 : 68,
      height: 58,
      margin: const EdgeInsets.all(3),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: focused ? const Color(0xFF00E676) : const Color(0xFF2E2E47),
        borderRadius: BorderRadius.circular(10),
        border: Border.all(
          color: focused ? Colors.white : Colors.white12,
          width: focused ? 3 : 1,
        ),
      ),
      child: Text(
        label,
        style: TextStyle(
          fontSize: wide ? 19 : 25,
          fontWeight: FontWeight.bold,
          color: focused ? Colors.black : Colors.white,
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Dialog(
      backgroundColor: const Color(0xFF1E1E2E),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(22)),
      child: KeyboardListener(
        focusNode: _focusNode,
        onKeyEvent: _onKey,
        child: Container(
          width: 820,
          padding: const EdgeInsets.all(26),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                widget.title,
                style:
                    const TextStyle(fontSize: 26, fontWeight: FontWeight.bold),
              ),
              const SizedBox(height: 16),
              Container(
                width: double.infinity,
                padding:
                    const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
                decoration: BoxDecoration(
                  color: const Color(0xFF12121E),
                  borderRadius: BorderRadius.circular(10),
                  border: Border.all(color: Colors.white24),
                ),
                child: Text(
                  _text.isEmpty ? 'Type a name…' : '$_text▌',
                  style: TextStyle(
                    fontSize: 24,
                    color: _text.isEmpty ? Colors.white38 : Colors.white,
                  ),
                ),
              ),
              const SizedBox(height: 16),
              for (var r = 0; r < _rows.length; r++)
                Row(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    for (var c = 0; c < _rows[r].length; c++)
                      GestureDetector(
                        onTap: () {
                          setState(() {
                            _row = r;
                            _col = c;
                          });
                          _press();
                        },
                        child: _cell(_rows[r][c], _row == r && _col == c),
                      ),
                  ],
                ),
              const SizedBox(height: 6),
              Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  for (var c = 0; c < _actions.length; c++)
                    GestureDetector(
                      onTap: () {
                        setState(() {
                          _row = _rows.length;
                          _col = c;
                        });
                        _press();
                      },
                      child: _cell(
                        _actions[c],
                        _row == _rows.length && _col == c,
                        wide: true,
                      ),
                    ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Player avatars: generated pictures (DiceBear) picked per player, cached
// on the device. Falls back to the colored initial letter.
// ---------------------------------------------------------------------------

class Avatars {
  static const styleIds = [
    'pixel-art',
    'bottts',
    'adventurer',
    'avataaars',
    'fun-emoji',
    'personas',
  ];
  static const styleNames = [
    'Pixel',
    'Robots',
    'Adventurer',
    'Toons',
    'Fun Faces',
    'People',
  ];
  static Map<String, String> _map = {};
  static bool _loaded = false;

  static Future<void> load() async {
    if (_loaded) return;
    _loaded = true;
    final raw = await Prefs.get('avatars');
    if (raw != null && raw.isNotEmpty) {
      try {
        final decoded = jsonDecode(raw) as Map<String, dynamic>;
        _map = decoded.map((k, v) => MapEntry(k, v.toString()));
      } catch (_) {
        _map = {};
      }
    }
  }

  static (String, String)? parse(String name) {
    final k = _map[name];
    if (k == null) return null;
    final i = k.indexOf(':');
    if (i <= 0) return null;
    return (k.substring(0, i), k.substring(i + 1));
  }

  static Future<void> _save() => Prefs.set('avatars', jsonEncode(_map));

  static Future<void> set(String name, String style, String seed) async {
    await load();
    _map[name] = '$style:$seed';
    await _save();
  }

  static Future<void> clear(String name) async {
    await load();
    _map.remove(name);
    await _save();
  }

  static Future<void> rename(String oldName, String newName) async {
    await load();
    final k = _map.remove(oldName);
    if (k != null) {
      _map[newName] = k;
      await _save();
    }
  }

  static File fileFor(String style, String seed) {
    final safe = seed.replaceAll(RegExp(r'[^A-Za-z0-9_-]'), '_');
    return File('/sdcard/iTaK/avatars/${style}_$safe.png');
  }

  static Future<File?> get(String style, String seed) async {
    final f = fileFor(style, seed);
    if (f.existsSync()) return f;
    final url =
        'https://api.dicebear.com/9.x/$style/png?seed=${Uri.encodeComponent(seed)}&size=96';
    final bytes = await Artwork._download(url);
    if (bytes == null || bytes.length < 400) return null;
    try {
      await f.parent.create(recursive: true);
      await f.writeAsBytes(bytes, flush: true);
      return f;
    } catch (_) {
      return null;
    }
  }
}

class PlayerAvatar extends StatelessWidget {
  final String name;
  final double size;
  const PlayerAvatar({super.key, required this.name, required this.size});

  Widget _letter() {
    return CircleAvatar(
      radius: size / 2,
      backgroundColor:
          kPlayerColors[name.hashCode.abs() % kPlayerColors.length],
      child: Text(
        name.isEmpty ? '?' : name[0].toUpperCase(),
        style: TextStyle(
          fontSize: size * 0.5,
          fontWeight: FontWeight.bold,
          color: Colors.white,
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final parsed = Avatars.parse(name);
    if (parsed == null) return _letter();
    return FutureBuilder<File?>(
      future: Avatars.get(parsed.$1, parsed.$2),
      builder: (ctx, snap) {
        final f = snap.data;
        if (f == null) return _letter();
        return ClipOval(
          child: Image.file(
            f,
            width: size,
            height: size,
            fit: BoxFit.cover,
            errorBuilder: (_, __, ___) => _letter(),
          ),
        );
      },
    );
  }
}

Future<void> showAvatarPicker(BuildContext context, String playerName) {
  return showDialog<void>(
    context: context,
    barrierColor: Colors.black.withOpacity(0.85),
    builder: (_) => _AvatarPickerDialog(playerName: playerName),
  );
}

class _AvatarPickerDialog extends StatefulWidget {
  final String playerName;
  const _AvatarPickerDialog({required this.playerName});

  @override
  State<_AvatarPickerDialog> createState() => _AvatarPickerDialogState();
}

class _AvatarPickerDialogState extends State<_AvatarPickerDialog> {
  int _style = 0;

  List<String> get _seeds => [
        widget.playerName,
        for (var i = 2; i <= 24; i++) '${widget.playerName}-$i',
      ];

  @override
  Widget build(BuildContext context) {
    final style = Avatars.styleIds[_style];
    return AlertDialog(
      backgroundColor: const Color(0xFF262640),
      title: Text('Pick a picture for ${widget.playerName}'),
      content: SizedBox(
        width: 660,
        height: 540,
        child: Column(
          children: [
            Wrap(
              spacing: 8,
              children: [
                for (var i = 0; i < Avatars.styleIds.length; i++)
                  ChoiceChip(
                    label: Text(Avatars.styleNames[i]),
                    selected: _style == i,
                    onSelected: (_) => setState(() => _style = i),
                  ),
              ],
            ),
            const SizedBox(height: 12),
            Expanded(
              child: GridView.builder(
                gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                  crossAxisCount: 6,
                  crossAxisSpacing: 10,
                  mainAxisSpacing: 10,
                ),
                itemCount: _seeds.length,
                itemBuilder: (ctx, i) {
                  final seed = _seeds[i];
                  return GestureDetector(
                    onTap: () async {
                      await Avatars.set(widget.playerName, style, seed);
                      if (mounted) Navigator.pop(context);
                    },
                    child: FutureBuilder<File?>(
                      future: Avatars.get(style, seed),
                      builder: (ctx, snap) {
                        final f = snap.data;
                        if (f == null) {
                          return Container(
                            decoration: BoxDecoration(
                              color: const Color(0xFF1B1B28),
                              borderRadius: BorderRadius.circular(12),
                            ),
                            child: const Center(
                              child: SizedBox(
                                width: 22,
                                height: 22,
                                child:
                                    CircularProgressIndicator(strokeWidth: 2),
                              ),
                            ),
                          );
                        }
                        return ClipRRect(
                          borderRadius: BorderRadius.circular(12),
                          child: Image.file(f, fit: BoxFit.cover),
                        );
                      },
                    ),
                  );
                },
              ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          onPressed: () async {
            await Avatars.clear(widget.playerName);
            if (mounted) Navigator.pop(context);
          },
          child: const Text('Use letter instead'),
        ),
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Cancel'),
        ),
      ],
    );
  }
}
