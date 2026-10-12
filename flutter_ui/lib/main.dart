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
  });
}

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
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/8/82/NES-Console-Set.jpg/1280px-NES-Console-Set.jpg',
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
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/8/8f/Game-Boy-Original.jpg/1280px-Game-Boy-Original.jpg',
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
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/7/7d/Nintendo-Game-Boy-Advance-Purple-FL.jpg/1280px-Nintendo-Game-Boy-Advance-Purple-FL.jpg',
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
        'https://thumb.wikimedia.org/wikipedia/commons/thumb/3/39/PSX-Console-wController.jpg/1280px-PSX-Console-wController.jpg',
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
    for (final ext in ['.jpg', '.png']) {
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
  int _playerIndex = 0;
  int _topIndex = 1; // top bar: 0 = Leaderboard, 1 = Settings
  int _zone = _zoneSystems;
  List<String> _players = [];
  String _currentPlayer = '';
  String? _banner;
  Timer? _bannerTimer;
  final Map<String, File?> _systemImages = {};
  final Map<String, int> _gameCounts = {};
  final FocusNode _focusNode = FocusNode();
  final ScrollController _playersScroll = ScrollController();

  @override
  void initState() {
    super.initState();
    _loadPlayers();
    _loadSystemImages();
    _countGames();
    // Native side calls this when a gamepad connects: ask who picked
    // it up by jumping to the players row with a banner.
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'controllerConnected' && mounted) {
        final name = (call.arguments as Map?)?['name']?.toString() ?? 'Controller';
        setState(() {
          _zone = _zonePlayers;
          _banner = '$name connected. Who is playing? Pick your name.';
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
      _playerIndex = players.indexOf(cur);
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
      _playerIndex = index;
      _currentPlayer = _players[index];
      _banner = null;
    });
    await Prefs.set('current_player', _currentPlayer);
  }

  // Player tiles after the real players: Add, then Manage.
  bool _isAddTile(int index) => index == _players.length;
  bool _isManageTile(int index) => index == _players.length + 1;
  int get _tileCount => _players.length + 2;

  void _scrollPlayersTo(int index) {
    if (!_playersScroll.hasClients) return;
    final target = (index * 190.0 - 300).clamp(
      0.0,
      _playersScroll.position.maxScrollExtent,
    );
    _playersScroll.animateTo(
      target,
      duration: const Duration(milliseconds: 200),
      curve: Curves.easeOut,
    );
  }

  Future<void> _removePlayer(String name) async {
    if (_players.length <= 1) return;
    final players = _players.where((p) => p != name).toList();
    await Prefs.set('players', players.join('\n'));
    var cur = _currentPlayer;
    if (cur == name) {
      cur = players.first;
      await Prefs.set('current_player', cur);
    }
    if (!mounted) return;
    setState(() {
      _players = players;
      _currentPlayer = cur;
      _playerIndex = players.indexOf(cur);
    });
  }

  Future<void> _managePlayers() async {
    await showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Manage players'),
        content: SizedBox(
          width: 420,
          child: ListView(
            shrinkWrap: true,
            children: [
              for (final p in _players)
                ListTile(
                  leading: CircleAvatar(
                    backgroundColor:
                        kPlayerColors[p.hashCode.abs() % kPlayerColors.length],
                    child: Text(p[0].toUpperCase()),
                  ),
                  title: Text(p),
                  subtitle: p == _currentPlayer
                      ? const Text('Playing now')
                      : null,
                  trailing: _players.length > 1
                      ? TextButton(
                          onPressed: () async {
                            Navigator.pop(ctx);
                            await _removePlayer(p);
                          },
                          child: const Text(
                            'Remove',
                            style: TextStyle(color: Colors.redAccent),
                          ),
                        )
                      : null,
                  onTap: () async {
                    Navigator.pop(ctx);
                    await _selectPlayer(_players.indexOf(p));
                  },
                ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Done'),
          ),
        ],
      ),
    );
    _focusNode.requestFocus();
  }

  Future<void> _addPlayer() async {
    final controller = TextEditingController();
    final name = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Add a player'),
        content: TextField(
          controller: controller,
          autofocus: true,
          textCapitalization: TextCapitalization.words,
          decoration: const InputDecoration(hintText: 'Name'),
          onSubmitted: (v) => Navigator.pop(ctx, v),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, controller.text),
            child: const Text('Add'),
          ),
        ],
      ),
    );
    _focusNode.requestFocus();
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
    _playersScroll.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  bool _isSelect(KeyEvent event) =>
      event.logicalKey == LogicalKeyboardKey.enter ||
      event.logicalKey == LogicalKeyboardKey.select ||
      event.logicalKey == LogicalKeyboardKey.space;

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowDown) {
      setState(() => _zone = (_zone + 1).clamp(0, 2));
    } else if (key == LogicalKeyboardKey.arrowUp) {
      setState(() => _zone = (_zone - 1).clamp(0, 2));
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() {
        if (_zone == _zoneSettings) {
          _topIndex = 0;
        } else if (_zone == _zonePlayers) {
          _playerIndex = (_playerIndex - 1 + _tileCount) % _tileCount;
          _scrollPlayersTo(_playerIndex);
        } else if (_zone == _zoneSystems) {
          // From a grid column edge, step up a row (3 across).
          _systemIndex = (_systemIndex - 1 + kSystems.length) % kSystems.length;
        }
      });
    } else if (key == LogicalKeyboardKey.arrowRight) {
      setState(() {
        if (_zone == _zoneSettings) {
          _topIndex = 1;
        } else if (_zone == _zonePlayers) {
          _playerIndex = (_playerIndex + 1) % _tileCount;
          _scrollPlayersTo(_playerIndex);
        } else if (_zone == _zoneSystems) {
          _systemIndex = (_systemIndex + 1) % kSystems.length;
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
        if (_isAddTile(_playerIndex)) {
          _addPlayer();
        } else if (_isManageTile(_playerIndex)) {
          _managePlayers();
        } else {
          _selectPlayer(_playerIndex);
        }
      } else {
        _openGames(kSystems[_systemIndex]);
      }
    }
  }

  void _openGames(SystemInfo system) {
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

  Widget _playerChip(int index) {
    final isAdd = _isAddTile(index);
    final isManage = _isManageTile(index);
    final isTile = isAdd || isManage;
    final focused = _zone == _zonePlayers && _playerIndex == index;
    final isCurrent = !isTile && _players[index] == _currentPlayer;
    final color = isTile
        ? const Color(0xFF424242)
        : kPlayerColors[index % kPlayerColors.length];
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = _zonePlayers;
          _playerIndex = index;
        });
        if (isAdd) {
          _addPlayer();
        } else if (isManage) {
          _managePlayers();
        } else {
          _selectPlayer(index);
        }
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 150),
        margin: const EdgeInsets.symmetric(horizontal: 8),
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 7),
        decoration: BoxDecoration(
          color: const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: focused
                ? Colors.white
                : isCurrent
                    ? const Color(0xFF00E676)
                    : Colors.white24,
            width: focused ? 3 : 2,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            CircleAvatar(
              radius: 18,
              backgroundColor: color,
              child: isManage
                  ? const Icon(Icons.manage_accounts,
                      size: 19, color: Colors.white)
                  : Text(
                      isAdd ? '+' : _players[index][0].toUpperCase(),
                      style: const TextStyle(
                        fontSize: 18,
                        fontWeight: FontWeight.bold,
                        color: Colors.white,
                      ),
                    ),
            ),
            const SizedBox(width: 9),
            Text(
              isAdd
                  ? 'Add player'
                  : isManage
                      ? 'Manage'
                      : _players[index],
              style: TextStyle(
                fontSize: 19,
                fontWeight: isCurrent ? FontWeight.bold : FontWeight.normal,
                color: Colors.white,
              ),
            ),
            if (isCurrent) ...[
              const SizedBox(width: 7),
              const Icon(Icons.check_circle, size: 19, color: Color(0xFF00E676)),
            ],
          ],
        ),
      ),
    );
  }

  Widget _systemCard(int index) {
    final sys = kSystems[index];
    final focused = _zone == _zoneSystems && _systemIndex == index;
    final img = _systemImages[sys.id];
    final count = _gameCounts[sys.id];
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = _zoneSystems;
          _systemIndex = index;
        });
        _openGames(sys);
      },
      child: AnimatedScale(
        scale: focused ? 1.045 : 1.0,
        duration: const Duration(milliseconds: 140),
        child: Container(
          decoration: BoxDecoration(
            color: const Color(0xFF1B1B28),
            borderRadius: BorderRadius.circular(20),
            border: Border.all(
              color: focused ? Colors.white : Colors.white12,
              width: focused ? 3 : 1,
            ),
            boxShadow: focused
                ? [BoxShadow(color: sys.color.withOpacity(0.5), blurRadius: 26)]
                : null,
          ),
          clipBehavior: Clip.antiAlias,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Expanded(
                child: Container(
                  decoration: const BoxDecoration(
                    gradient: LinearGradient(
                      begin: Alignment.topCenter,
                      end: Alignment.bottomCenter,
                      colors: [Color(0xFFF5F5F7), Color(0xFFC6C7CE)],
                    ),
                  ),
                  padding: const EdgeInsets.all(16),
                  child: img != null
                      ? Image.file(
                          img,
                          fit: BoxFit.contain,
                          errorBuilder: (_, __, ___) => _consoleFallback(sys),
                        )
                      : _consoleFallback(sys),
                ),
              ),
              Container(
                padding:
                    const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
                decoration: BoxDecoration(
                  gradient: LinearGradient(
                    colors: [sys.color.withOpacity(0.85), sys.color],
                  ),
                ),
                child: Row(
                  children: [
                    Expanded(
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
                          Text(
                            sys.tagline,
                            style: const TextStyle(
                                fontSize: 13.5, color: Colors.white70),
                          ),
                        ],
                      ),
                    ),
                    if (count != null)
                      Text(
                        count == 0 ? 'No games' : '$count games',
                        style: const TextStyle(
                            fontSize: 15, color: Colors.white),
                      ),
                  ],
                ),
              ),
            ],
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
                    padding: const EdgeInsets.fromLTRB(40, 26, 40, 0),
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
                      const SizedBox(width: 40),
                      const Text(
                        "Who's playing?",
                        style: TextStyle(fontSize: 20, color: Colors.white70),
                      ),
                      const SizedBox(width: 8),
                      Expanded(
                        child: SizedBox(
                          height: 56,
                          child: ListView.builder(
                            controller: _playersScroll,
                            scrollDirection: Axis.horizontal,
                            itemCount: _tileCount,
                            itemBuilder: (context, index) =>
                                _playerChip(index),
                          ),
                        ),
                      ),
                      const SizedBox(width: 24),
                    ],
                  ),
                  const SizedBox(height: 18),
                  Expanded(
                    child: Padding(
                      padding: const EdgeInsets.symmetric(horizontal: 40),
                      child: GridView.builder(
                        physics: const NeverScrollableScrollPhysics(),
                        gridDelegate:
                            const SliverGridDelegateWithFixedCrossAxisCount(
                          crossAxisCount: 3,
                          crossAxisSpacing: 22,
                          mainAxisSpacing: 22,
                          childAspectRatio: 1.52,
                        ),
                        itemCount: kSystems.length,
                        itemBuilder: (context, index) => _systemCard(index),
                      ),
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
  final TextEditingController _searchController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _init();
  }

  @override
  void dispose() {
    _searchController.dispose();
    super.dispose();
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
                event.logicalKey == LogicalKeyboardKey.space)) {
          _launch(game);
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      },
      child: GestureDetector(
        onTap: () => _launch(game),
        child: AnimatedScale(
          scale: focused ? 1.05 : 1.0,
          duration: const Duration(milliseconds: 120),
          child: Container(
            decoration: BoxDecoration(
              color: const Color(0xFF1E1E2A),
              borderRadius: BorderRadius.circular(12),
              border: Border.all(
                color: focused ? Colors.white : Colors.white10,
                width: focused ? 3 : 1,
              ),
              boxShadow: focused
                  ? [BoxShadow(color: sys.color.withOpacity(0.45), blurRadius: 18)]
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
                          child: TextField(
                            controller: _searchController,
                            onChanged: (v) => setState(() {
                              _query = v.trim();
                              _focused = 0;
                            }),
                            style: const TextStyle(fontSize: 19),
                            decoration: InputDecoration(
                              hintText: 'Search games...',
                              prefixIcon: const Icon(Icons.search),
                              filled: true,
                              fillColor: const Color(0xFF1B1B28),
                              border: OutlineInputBorder(
                                borderRadius: BorderRadius.circular(12),
                                borderSide: BorderSide.none,
                              ),
                              contentPadding:
                                  const EdgeInsets.symmetric(vertical: 12),
                            ),
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
                                      LogicalKeyboardKey.select) {
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
                            : GridView.builder(
                                padding: const EdgeInsets.all(24),
                                gridDelegate:
                                    const SliverGridDelegateWithFixedCrossAxisCount(
                                  crossAxisCount: 4,
                                  crossAxisSpacing: 20,
                                  mainAxisSpacing: 20,
                                  childAspectRatio: 0.92,
                                ),
                                itemCount: visible.length,
                                itemBuilder: (context, index) =>
                                    _tile(index, visible),
                              ),
                  ),
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

class LeaderboardScreen extends StatefulWidget {
  const LeaderboardScreen({super.key});

  @override
  State<LeaderboardScreen> createState() => _LeaderboardScreenState();
}

class _LeaderboardScreenState extends State<LeaderboardScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  List<_PlayerStat> _players = [];
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _load();
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

  List<_GameStat> get _topGames {
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
    final all = byRom.values.toList()
      ..sort((a, b) => b.seconds.compareTo(a.seconds));
    return all.take(5).toList();
  }

  Widget _playerCard(int rank, _PlayerStat p) {
    final fav = p.favorite;
    final recent = p.mostRecent;
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
          CircleAvatar(
            radius: 20,
            backgroundColor:
                kPlayerColors[p.name.hashCode.abs() % kPlayerColors.length],
            child: Text(
              p.name.isEmpty ? '?' : p.name[0].toUpperCase(),
              style: const TextStyle(
                  fontSize: 19,
                  fontWeight: FontWeight.bold,
                  color: Colors.white),
            ),
          ),
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
                if (fav != null)
                  Text(
                    'Favorite: ${fav.name} (${_fmtDuration(fav.seconds)})',
                    style:
                        const TextStyle(fontSize: 16, color: Colors.white54),
                  ),
                if (recent != null)
                  Text(
                    'Last played: ${recent.name}',
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
                : Center(
                    child: SizedBox(
                      width: 900,
                      child: ListView(
                        padding: const EdgeInsets.symmetric(vertical: 18),
                        children: [
                          for (var i = 0; i < _players.length; i++)
                            _playerCard(i + 1, _players[i]),
                          const SizedBox(height: 14),
                          const Text(
                            'Top games (everyone)',
                            style: TextStyle(
                                fontSize: 21, fontWeight: FontWeight.bold),
                          ),
                          const SizedBox(height: 6),
                          for (final g in _topGames)
                            Padding(
                              padding: const EdgeInsets.symmetric(vertical: 3),
                              child: Row(
                                children: [
                                  Expanded(
                                    child: Text(g.name,
                                        style: const TextStyle(fontSize: 18)),
                                  ),
                                  Text(
                                    '${_fmtDuration(g.seconds)} · ${g.plays} plays',
                                    style: const TextStyle(
                                        fontSize: 17, color: Colors.white70),
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
}
