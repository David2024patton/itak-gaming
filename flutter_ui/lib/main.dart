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
const MethodChannel _logChannel = MethodChannel('net.itak/emu');

/// Writes one line into the app debug log (Settings -> Logs).
/// Native writes it to logcat, and the capture thread files it
/// with every emulator and system line.
Future<void> appLog(String msg) async {
  try {
    await _logChannel.invokeMethod('log', {'message': msg});
  } catch (_) {}
}

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
// Player identity. The first name stays the account key everywhere
// (saves, stats files, server). Each player also carries a middle
// initial, a last initial, and a home state or region, collected when
// the player is added or edited, so the boards can show a fuller
// identity ("David M. P.", Kentucky) and stay organized when more
// people join. Stored as one JSON map in the 'player_info' pref.
// ---------------------------------------------------------------------------

const List<String> kUsStates = [
  'AL', 'AK', 'AZ', 'AR', 'CA', 'CO', 'CT', 'DE', 'DC', 'FL', 'GA', 'HI',
  'ID', 'IL', 'IN', 'IA', 'KS', 'KY', 'LA', 'ME', 'MD', 'MA', 'MI', 'MN',
  'MS', 'MO', 'MT', 'NE', 'NV', 'NH', 'NJ', 'NM', 'NY', 'NC', 'ND', 'OH',
  'OK', 'OR', 'PA', 'RI', 'SC', 'SD', 'TN', 'TX', 'UT', 'VT', 'VA', 'WA',
  'WV', 'WI', 'WY',
];

class PlayerIdentity {
  final String middle; // one letter, may be empty
  final String last; // one letter, may be empty
  final String region; // state code or free text, may be empty
  const PlayerIdentity(this.middle, this.last, this.region);

  bool get complete =>
      middle.isNotEmpty && last.isNotEmpty && region.isNotEmpty;

  String displayName(String name) {
    final parts = <String>[name];
    if (middle.isNotEmpty) parts.add('$middle.');
    if (last.isNotEmpty) parts.add('$last.');
    return parts.join(' ');
  }
}

class PlayerInfoStore {
  static Map<String, PlayerIdentity> _cache = {};
  static bool _loaded = false;

  static Future<Map<String, PlayerIdentity>> load() async {
    if (_loaded) return _cache;
    _loaded = true;
    final raw = await Prefs.get('player_info');
    if (raw != null && raw.isNotEmpty) {
      try {
        final map = jsonDecode(raw) as Map<String, dynamic>;
        _cache = map.map((k, v) {
          final m = v as Map<String, dynamic>;
          return MapEntry(
            k,
            PlayerIdentity(
              (m['m'] ?? '').toString(),
              (m['l'] ?? '').toString(),
              (m['st'] ?? '').toString(),
            ),
          );
        });
      } catch (_) {}
    }
    return _cache;
  }

  static Future<void> _persist() async {
    final map = _cache.map(
      (k, v) => MapEntry(k, {'m': v.middle, 'l': v.last, 'st': v.region}),
    );
    await Prefs.set('player_info', jsonEncode(map));
  }

  static Future<void> save(String name, PlayerIdentity id) async {
    await load();
    _cache[name] = id;
    await _persist();
  }

  static Future<void> rename(String oldName, String newName) async {
    await load();
    final id = _cache.remove(oldName);
    if (id != null) _cache[newName] = id;
    await _persist();
  }

  static Future<void> remove(String name) async {
    await load();
    _cache.remove(name);
    await _persist();
  }

  static PlayerIdentity? of(String name) => _cache[name];

  static String displayOf(String name) =>
      _cache[name]?.displayName(name) ?? name;

  // The native stats uploader reads these three prefs when it
  // reports a finished session to the global board, so the board
  // can show the same identity as the local screens. Refreshed
  // whenever the current player or their identity changes.
  static Future<void> publishForUpload(String name) async {
    await load();
    final id = _cache[name];
    await Prefs.set('identity_name', name);
    await Prefs.set('identity_display', id?.displayName(name) ?? name);
    await Prefs.set('identity_region', id?.region ?? '');
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
    core: 'libgo_nes.so',
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
    core: 'libgo_genesis.so',
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
    core: 'libgo_gameboy.so',
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
const int _zoneTabs = 0;
const int _zonePlayers = 1;
const int _zoneSystems = 2;

/// One queued friend alert: the friend's account name (which keys
/// their avatar) and the line the banner shows.
class _FriendAlertToast {
  final String name;
  final String message;
  const _FriendAlertToast(this.name, this.message);
}

class SystemSelectScreen extends StatefulWidget {
  const SystemSelectScreen({super.key});

  @override
  State<SystemSelectScreen> createState() => _SystemSelectScreenState();
}

class _SystemSelectScreenState extends State<SystemSelectScreen>
    with WidgetsBindingObserver {
  static const _channel = MethodChannel('net.itak/emu');
  int _systemIndex = 1; // SNES default
  int _tabIndex = 0; // tab row: 0 Home, 1 Leaderboard, 2 Online, 3 Settings
  int _zone = _zoneSystems;
  List<String> _players = [];
  String _currentPlayer = '';
  String? _banner;
  Timer? _bannerTimer;
  // Friend alerts live in the shell because the home screen state
  // lives for the whole Flutter session: this box pings its
  // presence every 30 s, and the friends list is polled every
  // 20 s so a friend coming online (or starting a game) raises a
  // banner and a chime anywhere in the Flutter UI.
  Timer? _presenceTimer;
  Timer? _friendsTimer;
  String _alertsForPlayer = '';
  Map<String, (String, String)>? _friendWas; // name -> (status, game)
  final List<_FriendAlertToast> _alertQueue = [];
  OverlayEntry? _alertEntry;
  Timer? _alertTimer;
  final Map<String, File?> _systemImages = {};
  final Map<String, int> _gameCounts = {};
  final FocusNode _focusNode = FocusNode();
  final ScrollController _gridScroll = ScrollController();

  bool _locked(SystemInfo sys) => sys.requires64 && !gIs64Bit;

  bool _updateBusy = false;

  Future<void> _checkForUpdate() async {
    if (_updateBusy || !mounted) return;
    if (ModalRoute.of(context)?.isCurrent != true) return;
    _updateBusy = true;
    try {
      final check = await AppUpdate.check();
      if (check == null) {
        appLog('update check: server unreachable');
        return;
      }
      if (!mounted) return;
      if (ModalRoute.of(context)?.isCurrent != true) return;
      if (check.available != null) {
        appLog('update check: ${check.available!.versionName} available');
        await showAppUpdateDialog(context, check, manual: false);
      }
    } finally {
      _updateBusy = false;
    }
  }

  // ----- Friend alerts: presence ping + friends polling -----

  // Who alerts run for: the shell's current player once players
  // have loaded, else the stored pref.
  Future<String> _alertsPlayer() async {
    if (_currentPlayer.isNotEmpty) return _currentPlayer;
    final p = await Prefs.get('current_player');
    return (p == null || p.isEmpty) ? '' : p;
  }

  // Tell the friends server this box is online. Best effort: a
  // missed ping only means friends see this box go offline a
  // little sooner.
  Future<void> _pingPresence() async {
    final me = await _alertsPlayer();
    if (me.isEmpty) return;
    try {
      final client = HttpClient();
      client.connectionTimeout = const Duration(seconds: 6);
      try {
        final req =
            await client.postUrl(Uri.parse('$kServerBase/friends/presence'));
        req.headers.contentType = ContentType.json;
        req.write(jsonEncode({'player': me}));
        final resp = await req.close().timeout(const Duration(seconds: 6));
        await resp.drain();
      } finally {
        client.close();
      }
    } catch (_) {}
  }

  // Poll the friends list and diff it against the last snapshot.
  // The first successful poll only learns the snapshot (nobody
  // gets a banner for friends who were already around). Rules:
  // offline -> online raises "is online"; anything -> in_lobby
  // raises "is playing GAME", except staying in the same game's
  // lobby, which stays quiet. A failed poll is silent and keeps
  // the old snapshot.
  Future<void> _pollFriends() async {
    final me = await _alertsPlayer();
    if (me.isEmpty) return;
    if (me != _alertsForPlayer) {
      // The player at the box changed: learn the new friend list
      // as a fresh baseline instead of diffing across accounts.
      _alertsForPlayer = me;
      _friendWas = null;
    }
    List<dynamic> friends;
    try {
      final client = HttpClient();
      client.connectionTimeout = const Duration(seconds: 6);
      try {
        final req = await client.getUrl(Uri.parse(
            '$kServerBase/friends?player=${Uri.encodeQueryComponent(me)}'));
        final resp = await req.close().timeout(const Duration(seconds: 6));
        final text = await resp.transform(utf8.decoder).join();
        if (resp.statusCode >= 400) return;
        final data = jsonDecode(text);
        friends = (data['friends'] as List?) ?? [];
      } finally {
        client.close();
      }
    } catch (_) {
      return;
    }
    if (!mounted) return;
    final baseline = _friendWas == null;
    final now = <String, (String, String)>{};
    final fresh = <_FriendAlertToast>[];
    for (final f in friends) {
      final m = f as Map<String, dynamic>;
      final name = (m['name'] ?? '').toString();
      if (name.isEmpty) continue;
      final status = (m['status'] ?? 'offline').toString();
      final game = (m['game'] ?? '').toString();
      now[name] = (status, game);
      if (baseline) continue; // learn only, never notify
      final was = _friendWas?[name];
      final display = (m['display'] ?? '').toString();
      final label = display.isNotEmpty ? display : name;
      if (status == 'in_lobby') {
        if (was == null || was.$1 != 'in_lobby' || was.$2 != game) {
          fresh.add(_FriendAlertToast(
            name,
            game.isNotEmpty
                ? '$label is playing $game'
                : '$label is in a lobby',
          ));
        }
      } else if (status == 'online') {
        if (was != null && was.$1 == 'offline') {
          fresh.add(_FriendAlertToast(name, '$label is online'));
        }
      }
    }
    _friendWas = now;
    if (fresh.isEmpty) return;
    // Alerts off (Settings -> Friend alerts): the snapshot still
    // updated above, so turning alerts back on never replays a
    // backlog of stale arrivals.
    if ((await Prefs.get('friend_alerts')) == '0') return;
    _alertQueue.addAll(fresh);
    _showNextAlert();
  }

  // Show queued banners one at a time, about 4 s each, never
  // stacked. The banner ignores pointers, so the remote keeps
  // driving whatever screen is underneath.
  void _showNextAlert() {
    if (!mounted || _alertEntry != null || _alertQueue.isEmpty) return;
    final toast = _alertQueue.removeAt(0);
    final entry = OverlayEntry(
      builder: (_) => _FriendAlertBanner(
        avatarName: toast.name,
        message: toast.message,
      ),
    );
    _alertEntry = entry;
    Overlay.of(context).insert(entry);
    _playAlertSound();
    _alertTimer = Timer(const Duration(seconds: 4), _dismissAlert);
  }

  void _dismissAlert() {
    _alertTimer?.cancel();
    _alertTimer = null;
    _alertEntry?.remove();
    _alertEntry = null;
    if (_alertQueue.isNotEmpty) {
      // A beat of quiet between banners so two friends arriving
      // together read as two alerts, not one long one.
      Timer(const Duration(milliseconds: 250), _showNextAlert);
    }
  }

  Future<void> _playAlertSound() async {
    try {
      await _channel.invokeMethod('playNotifySound');
    } catch (_) {}
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) _checkForUpdate();
  }

  String _versionName = '';

  Future<void> _loadDeviceInfo() async {
    try {
      final info = await _channel.invokeMethod<Map>('deviceInfo');
      final is64 = info?['is64'] == true;
      final ai = await AppUpdate.installed();
      if (!mounted) return;
      setState(() {
        gIs64Bit = is64;
        _versionName = ai?['versionName']?.toString() ?? '';
      });
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
    // Self-update: check the server at launch and again whenever
    // the app comes back to the front (TV apps stay open for
    // days; an update published meanwhile should still appear).
    WidgetsBinding.instance.addObserver(this);
    Future.delayed(const Duration(seconds: 4), _checkForUpdate);
    // Friend alerts: announce this box as online right away and
    // every 30 s after, and watch the friends list every 20 s.
    // initState runs once for the shell, so the timers cannot
    // duplicate on rebuilds; dispose cancels them.
    _pingPresence();
    _pollFriends();
    _presenceTimer =
        Timer.periodic(const Duration(seconds: 30), (_) => _pingPresence());
    _friendsTimer =
        Timer.periodic(const Duration(seconds: 20), (_) => _pollFriends());
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
    await PlayerInfoStore.load();
    await PlayerInfoStore.publishForUpload(cur);
    if (!mounted) return;
    setState(() {});
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
    await PlayerInfoStore.publishForUpload(_currentPlayer);
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
    await PlayerInfoStore.remove(name);
    if (_controllerMap.remove(name) != null) await _saveControllerMap();
    var cur = _currentPlayer;
    if (cur == name) {
      cur = players.first;
      await Prefs.set('current_player', cur);
      await PlayerInfoStore.publishForUpload(cur);
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
        backgroundColor: const Color(0xFF262640),
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

  // The player form: first name, middle initial, last initial, and
  // home state or region. Every field is required (David,
  // 2026-10-12) so the leaderboards can tell people apart. Returns
  // the cleaned values, or null when cancelled.
  Future<({String name, PlayerIdentity id})?> _playerForm({
    String? existing,
  }) async {
    await PlayerInfoStore.load();
    var name = existing ?? '';
    var id = existing != null
        ? (PlayerInfoStore.of(existing) ??
            const PlayerIdentity('', '', ''))
        : const PlayerIdentity('', '', '');
    String? error;
    if (!mounted) return null;
    final result = await showDialog<({String name, PlayerIdentity id})>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setLocal) {
          Future<void> editName() async {
            final v = await showTvKeyboard(
              ctx,
              title: 'First name',
              initial: name,
            );
            if (v == null) return;
            setLocal(() => name = v.trim().replaceAll('\n', ' '));
          }

          Future<void> editInitial(String title, bool middle) async {
            final v = await showTvKeyboard(
              ctx,
              title: title,
              initial: middle ? id.middle : id.last,
            );
            if (v == null) return;
            final letters = v.toUpperCase().replaceAll(RegExp(r'[^A-Z]'), '');
            final one = letters.isEmpty ? '' : letters[0];
            setLocal(() {
              id = middle
                  ? PlayerIdentity(one, id.last, id.region)
                  : PlayerIdentity(id.middle, one, id.region);
            });
          }

          Future<void> editRegion() async {
            final picked = await _pickRegion(id.region);
            if (picked == null) return;
            setLocal(() => id = PlayerIdentity(id.middle, id.last, picked));
          }

          Widget field(
            String label,
            String value,
            VoidCallback onTap, {
            bool auto = false,
          }) {
            return Padding(
              padding: const EdgeInsets.only(top: 10),
              child: InkWell(
                autofocus: auto,
                borderRadius: BorderRadius.circular(10),
                onTap: onTap,
                child: Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
                  decoration: BoxDecoration(
                    color: const Color(0xFF1B1B28),
                    borderRadius: BorderRadius.circular(10),
                    border: Border.all(color: Colors.white24),
                  ),
                  child: Row(
                    children: [
                      SizedBox(
                        width: 150,
                        child: Text(
                          label,
                          style: const TextStyle(
                              fontSize: 15, color: Colors.white60),
                        ),
                      ),
                      Expanded(
                        child: Text(
                          value.isEmpty ? 'Tap to set' : value,
                          style: TextStyle(
                            fontSize: 18,
                            fontWeight: FontWeight.bold,
                            color: value.isEmpty ? Colors.white38 : Colors.white,
                          ),
                        ),
                      ),
                      const Icon(Icons.edit, size: 18, color: Colors.white54),
                    ],
                  ),
                ),
              ),
            );
          }

          final taken =
              _players.any((p) => p != existing && p == name.trim());
          final valid = name.trim().isNotEmpty && id.complete && !taken;
          return AlertDialog(
            backgroundColor: _dialogBg,
            title: Text(existing == null ? 'Add a player' : 'Edit player'),
            content: SizedBox(
              width: 560,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text(
                    'Every player needs a first name, a middle and last '
                    'initial, and a home state. That is how the '
                    'leaderboards tell people apart.',
                    style: TextStyle(fontSize: 15, color: Colors.white70),
                  ),
                  field('First name', name, editName, auto: true),
                  field('Middle initial', id.middle,
                      () => editInitial('Middle initial', true)),
                  field('Last initial', id.last,
                      () => editInitial('Last initial', false)),
                  field('State / region', id.region, editRegion),
                  if (error != null)
                    Padding(
                      padding: const EdgeInsets.only(top: 10),
                      child: Text(
                        error!,
                        style: const TextStyle(
                            fontSize: 14.5, color: Colors.redAccent),
                      ),
                    ),
                ],
              ),
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(ctx),
                child: const Text('Cancel'),
              ),
              TextButton(
                onPressed: () {
                  if (!valid) {
                    setLocal(() => error = taken
                        ? 'That first name is already taken on this box.'
                        : 'Fill in every field to save.');
                    return;
                  }
                  Navigator.pop(ctx, (name: name.trim(), id: id));
                },
                child: const Text('Save'),
              ),
            ],
          );
        },
      ),
    );
    _refocusHome();
    return result;
  }

  // State picker: the 50 states plus DC as tappable rows, or a
  // free-text entry for anywhere outside the US.
  Future<String?> _pickRegion(String current) async {
    final picked = await showDialog<String>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.85),
      builder: (dctx) => AlertDialog(
        backgroundColor: _dialogBg,
        title: const Text('Home state'),
        content: SizedBox(
          width: 420,
          height: 430,
          child: ListView(
            children: [
              for (final st in kUsStates)
                ListTile(
                  dense: true,
                  title: Text(st, style: const TextStyle(fontSize: 18)),
                  trailing: st == current
                      ? const Icon(Icons.check, color: Color(0xFF00E676))
                      : null,
                  onTap: () => Navigator.pop(dctx, st),
                ),
              const Divider(color: Colors.white24),
              ListTile(
                dense: true,
                title: const Text(
                  'Outside the US...',
                  style: TextStyle(fontSize: 18),
                ),
                onTap: () => Navigator.pop(dctx, '__other__'),
              ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(dctx),
            child: const Text('Cancel'),
          ),
        ],
      ),
    );
    if (picked == '__other__') {
      if (!mounted) return null;
      final typed = await showTvKeyboard(
        context,
        title: 'State or region',
        initial: kUsStates.contains(current) ? '' : current,
      );
      final clean = (typed ?? '').trim();
      return clean.isEmpty ? null : clean;
    }
    return picked;
  }

  // Edit a player through the form. A changed first name runs the
  // full rename path (saves folder, stats file, avatar, controller
  // assignment, identity record all follow the new name).
  Future<void> _editPlayer(String oldName, VoidCallback refresh) async {
    final form = await _playerForm(existing: oldName);
    if (form == null) return;
    final newName = form.name;
    if (newName != oldName) {
      final players =
          _players.map((p) => p == oldName ? newName : p).toList();
      await Prefs.set('players', players.join('\n'));
      if (_currentPlayer == oldName) {
        await Prefs.set('current_player', newName);
      }
      await Avatars.rename(oldName, newName);
      // A rename keeps the player's controller assignment.
      final assigned = _controllerMap.remove(oldName);
      if (assigned != null) {
        _controllerMap[newName] = assigned;
        await _saveControllerMap();
      }
      // Move the player's save folder and stats file to the new name.
      try {
        await _channel.invokeMethod('renamePlayerFiles', {
          'old': oldName,
          'new': newName,
        });
      } catch (_) {}
      await PlayerInfoStore.rename(oldName, newName);
      if (!mounted) return;
      setState(() {
        _players = players;
        if (_currentPlayer == oldName) _currentPlayer = newName;
      });
    }
    await PlayerInfoStore.save(newName, form.id);
    if (_currentPlayer == newName) {
      await PlayerInfoStore.publishForUpload(newName);
    }
    refresh();
    _refocusHome();
  }

  // One-line summary under a player in the manage table: region,
  // whether they are playing now, and a nudge when the identity
  // fields are still missing.
  String _playerSubtitle(String p) {
    final id = PlayerInfoStore.of(p);
    final parts = <String>[
      if ((id?.region ?? '').isNotEmpty) id!.region,
      p == _currentPlayer ? 'Playing now' : 'Tap row to switch',
      if (id == null || !id.complete) 'tap Edit to finish the profile',
    ];
    return parts.join(' · ');
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
                                            PlayerInfoStore.displayOf(p),
                                            style: const TextStyle(
                                              fontSize: 20,
                                              fontWeight: FontWeight.bold,
                                            ),
                                          ),
                                          Text(
                                            _playerSubtitle(p),
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
                                      tooltip: 'Edit player',
                                      onPressed: () =>
                                          _editPlayer(p, refresh),
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
                                      PlayerInfoStore.displayOf(p),
                                      style: const TextStyle(
                                        fontSize: 20,
                                        fontWeight: FontWeight.bold,
                                      ),
                                    ),
                                    Text(
                                      p == _currentPlayer
                                          ? 'Playing now · tap to keep playing'
                                          : 'Tap to play as ${PlayerInfoStore.displayOf(p)}',
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
    final form = await _playerForm();
    if (form == null) return;
    final players = [..._players, form.name];
    await Prefs.set('players', players.join('\n'));
    await PlayerInfoStore.save(form.name, form.id);
    setState(() => _players = players);
    await _selectPlayer(players.length - 1);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _bannerTimer?.cancel();
    _presenceTimer?.cancel();
    _friendsTimer?.cancel();
    _alertTimer?.cancel();
    _alertEntry?.remove();
    _alertEntry = null;
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

  // The systems grid shows exactly two full rows at a time (8
  // systems, four across) and never a cut card: cards are sized to
  // fit two rows exactly, and the view scrolls in whole-row steps so
  // every resting offset sits on a row boundary. _rowStride is set
  // by the grid's LayoutBuilder.
  double? _rowStride;

  void _scrollSystemsTo(int index) {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_gridScroll.hasClients) return;
      final stride = _rowStride;
      if (stride == null || stride <= 0) return;
      final position = _gridScroll.position;
      final row = index ~/ 4;
      final firstRow = (position.pixels / stride).round();
      int? targetRow;
      if (row < firstRow) {
        targetRow = row;
      } else if (row > firstRow + 1) {
        targetRow = row - 1;
      }
      if (targetRow == null) return;
      final target = (targetRow * stride).clamp(
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
        if (_zone == _zoneTabs) {
          _zone = _zonePlayers;
        } else if (_zone == _zonePlayers) {
          _zone = _zoneSystems;
        } else if (_systemIndex + 4 < kSystems.length) {
          _systemIndex += 4;
          _scrollSystemsTo(_systemIndex);
        }
      });
    } else if (key == LogicalKeyboardKey.arrowUp) {
      setState(() {
        if (_zone == _zoneSystems) {
          if (_systemIndex >= 4) {
            _systemIndex -= 4;
            _scrollSystemsTo(_systemIndex);
          } else {
            _zone = _zonePlayers;
          }
        } else if (_zone == _zonePlayers) {
          _zone = _zoneTabs;
        }
      });
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() {
        if (_zone == _zoneTabs) {
          _tabIndex = (_tabIndex + 3) % 4;
        } else if (_zone == _zoneSystems && _systemIndex > 0) {
          _systemIndex -= 1;
          _scrollSystemsTo(_systemIndex);
        }
        // Players zone is a single chip now: no left/right scrolling.
      });
    } else if (key == LogicalKeyboardKey.arrowRight) {
      setState(() {
        if (_zone == _zoneTabs) {
          _tabIndex = (_tabIndex + 1) % 4;
        } else if (_zone == _zoneSystems &&
            _systemIndex < kSystems.length - 1) {
          _systemIndex += 1;
          _scrollSystemsTo(_systemIndex);
        }
      });
    } else if (_isSelect(event)) {
      if (_zone == _zoneTabs) {
        _activateTab(_tabIndex);
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

  void _openOnline() {
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => const OnlineScreen()),
    ).then((_) => _focusNode.requestFocus());
  }

  // What selecting a top tab does: Home drops focus into the
  // systems grid; the rest open their screens.
  void _activateTab(int index) {
    switch (index) {
      case 0:
        setState(() => _zone = _zoneSystems);
        break;
      case 1:
        _openLeaderboard();
        break;
      case 2:
        _openOnline();
        break;
      case 3:
        _openSettings();
        break;
    }
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
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
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
            PlayerAvatar(name: name, size: 32),
            const SizedBox(width: 8),
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Text(
                      PlayerInfoStore.displayOf(name),
                      style: const TextStyle(
                        fontSize: 17,
                        fontWeight: FontWeight.bold,
                        color: Colors.white,
                      ),
                    ),
                    const SizedBox(width: 6),
                    const Icon(Icons.check_circle,
                        size: 16, color: Color(0xFF00E676)),
                  ],
                ),
                Text(
                  padName == 'No controller'
                      ? 'Tap to switch player'
                      : 'Pad: $padName · tap to switch',
                  style:
                      const TextStyle(fontSize: 11.5, color: Colors.white54),
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
              borderRadius: BorderRadius.circular(24),
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
                        padding: const EdgeInsets.all(14),
                        child: SizedBox.expand(child: art),
                      ),
                      if (count != null)
                        Positioned(
                          top: 10,
                          left: 12,
                          child: Container(
                            padding: const EdgeInsets.symmetric(
                                horizontal: 9, vertical: 4),
                            decoration: BoxDecoration(
                              color: Colors.black.withOpacity(0.55),
                              borderRadius: BorderRadius.circular(999),
                            ),
                            child: Text(
                              count == 0 ? 'No games' : '$count games',
                              style: const TextStyle(
                                  fontSize: 12.5, color: Colors.white),
                            ),
                          ),
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
                // Slim name bar, same style as the game tiles: the
                // console picture gets the rest of the card.
                Container(
                  color: const Color(0xFF14141E),
                  padding:
                      const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
                  child: Text(
                    sys.name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(
                      fontSize: 17,
                      fontWeight: FontWeight.bold,
                      color: Colors.white,
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

  Widget _consoleFallback(SystemInfo sys) {
    return Center(
      child: Icon(Icons.videogame_asset, size: 84, color: sys.color),
    );
  }

  // One item in the top tab row. The current section is marked by
  // its green, bold name alone (no underline bar; David's call,
  // 2026-10-12); remote focus adds the bordered pill.
  Widget _tabItem(int index, IconData icon, String label) {
    final focused = _zone == _zoneTabs && _tabIndex == index;
    final current = index == 0;
    final color = current ? const Color(0xFF00E676) : Colors.white;
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = _zoneTabs;
          _tabIndex = index;
        });
        _activateTab(index);
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 140),
        padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
        decoration: BoxDecoration(
          color: focused
              ? const Color(0xFF2A2A3E)
              : current
                  ? const Color(0xFF00E676).withOpacity(0.12)
                  : Colors.transparent,
          borderRadius: BorderRadius.circular(10),
          border: Border.all(
            color: focused ? Colors.white : Colors.transparent,
            width: 2.5,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 21, color: color),
            const SizedBox(width: 8),
            Text(
              label,
              style: TextStyle(
                fontSize: 19,
                fontWeight: current ? FontWeight.bold : FontWeight.normal,
                color: color,
              ),
            ),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
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
                  // Tab row at the very top: Home, Leaderboard,
                  // Online, Settings.
                  Padding(
                    padding: const EdgeInsets.fromLTRB(40, 12, 40, 0),
                    child: Row(
                      children: [
                        _tabItem(0, Icons.home, 'Home'),
                        const SizedBox(width: 6),
                        _tabItem(1, Icons.emoji_events, 'Leaderboard'),
                        const SizedBox(width: 6),
                        _tabItem(2, Icons.public, 'Online'),
                        const SizedBox(width: 6),
                        _tabItem(3, Icons.settings, 'Settings'),
                      ],
                    ),
                  ),
                  Container(
                    height: 1,
                    margin: const EdgeInsets.only(top: 4),
                    color: Colors.white10,
                  ),
                  // Title row: brand on the left, the player chip at
                  // the far right.
                  Padding(
                    padding: const EdgeInsets.fromLTRB(58, 14, 58, 0),
                    child: Row(
                      children: [
                        const Text(
                          'iTaK',
                          style: TextStyle(
                            fontSize: 42,
                            fontWeight: FontWeight.w800,
                            color: Colors.white,
                            letterSpacing: 1.0,
                          ),
                        ),
                        const Text(
                          ' Gaming',
                          style: TextStyle(
                            fontSize: 42,
                            fontWeight: FontWeight.w800,
                            color: Color(0xFF00E676),
                            letterSpacing: 1.0,
                          ),
                        ),
                        const Spacer(),
                        _currentPlayerChip(),
                      ],
                    ),
                  ),
                  const SizedBox(height: 12),
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
                  const SizedBox(height: 14),
                  Expanded(
                    child: LayoutBuilder(
                      builder: (context, constraints) {
                        // Exactly two full rows on screen (4 across,
                        // 8 systems), sized from the real space: card
                        // height is whatever remains after the grid's
                        // top/bottom padding (16 each) and the one gap
                        // between the rows.
                        final gridW = constraints.maxWidth - 80;
                        final cardW = (gridW - 60 - 3 * 22) / 4;
                        final cardH =
                            (constraints.maxHeight - 16 - 16 - 22) / 2;
                        final aspect =
                            cardH > 40 ? cardW / cardH : 1.0;
                        _rowStride = cardH + 22;
                        return Padding(
                          padding:
                              const EdgeInsets.symmetric(horizontal: 40),
                          child: GridView.builder(
                            controller: _gridScroll,
                            // Clip.none so a focused card's glow and
                            // scale fade out past the grid's edge
                            // instead of slicing off hard at the top
                            // and left (David, 2026-10-12). cacheExtent
                            // 0 keeps off-screen rows unbuilt so
                            // nothing paints outside at rest.
                            clipBehavior: Clip.none,
                            cacheExtent: 0,
                            physics:
                                const NeverScrollableScrollPhysics(),
                            padding:
                                const EdgeInsets.fromLTRB(30, 16, 30, 16),
                            gridDelegate:
                                SliverGridDelegateWithFixedCrossAxisCount(
                              crossAxisCount: 4,
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
                  Text(
                    'UP / DOWN switch rows · LEFT / RIGHT browse · OK to select · In game, BACK opens the menu${_versionName.isEmpty ? '' : ' · iTaK Gaming v$_versionName'}',
                    style: const TextStyle(fontSize: 16.5, color: Colors.white54),
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

// ---------------------------------------------------------------------------
// Online play: browse, create, and join lobbies on the iTaK lobby
// server. One box hosts, the other joins; the server introduces the
// two and relays for them. Game sessions run over the same link.
// ---------------------------------------------------------------------------

const String kServerBase = 'https://arcade.itak.live';

/// One game installed on this box, found by scanning the ROM
/// folders. Used by the Online screen to host or join a session:
/// both players must own the identical ROM file.
class LocalGame {
  final SystemInfo system;
  final File file;
  const LocalGame(this.system, this.file);

  String get fileName => file.path.split('/').last;

  String get display {
    final dot = fileName.lastIndexOf('.');
    return dot > 0 ? fileName.substring(0, dot) : fileName;
  }
}

Future<List<LocalGame>> scanLocalGames() async {
  final out = <LocalGame>[];
  for (final sys in kSystems) {
    if (sys.requires64 && !gIs64Bit) continue;
    final dir = Directory('/sdcard/ROMs/${sys.dir}');
    if (await dir.exists()) {
      await for (final e in dir.list()) {
        if (e is File &&
            sys.exts.any((x) => e.path.toLowerCase().endsWith(x))) {
          out.add(LocalGame(sys, e));
        }
      }
    }
  }
  out.sort((a, b) => a.display.compareTo(b.display));
  return out;
}

class OnlineScreen extends StatefulWidget {
  const OnlineScreen({super.key});

  @override
  State<OnlineScreen> createState() => _OnlineScreenState();
}

class _OnlineScreenState extends State<OnlineScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  final FocusNode _focusNode = FocusNode();
  List<dynamic> _lobbies = [];
  List<dynamic> _friends = [];
  String _status = 'Connecting to the lobby server...';
  bool _online = false;
  // Focus rows: 0 = Create, 1 = Refresh, then one row per friend,
  // then two stops per lobby (Join, then Watch on the same card),
  // last = Back. The Friends and Open lobbies section labels sit
  // between the rows in the list but are not selectable, so they
  // own no focus index.
  int _sel = 0;
  Timer? _ticker;
  final ScrollController _listScroll = ScrollController();
  final Map<int, GlobalKey> _rowKeys = {};

  int get _itemCount => _lobbies.length * 2 + _friends.length + 3;

  @override
  void initState() {
    super.initState();
    _refresh();
    _ticker =
        Timer.periodic(const Duration(seconds: 5), (_) => _refresh());
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  @override
  void dispose() {
    _ticker?.cancel();
    _listScroll.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  // Keep the remote-selected lobby row on screen as selection moves.
  void _scrollToSel() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final ctx = _rowKeys[_sel]?.currentContext;
      if (ctx != null) {
        Scrollable.ensureVisible(
          ctx,
          duration: const Duration(milliseconds: 160),
        );
      }
    });
  }

  Future<dynamic> _api(String method, String path,
      [Map<String, dynamic>? body]) async {
    final client = HttpClient();
    client.connectionTimeout = const Duration(seconds: 6);
    try {
      final uri = Uri.parse('$kServerBase$path');
      final req = method == 'POST'
          ? await client.postUrl(uri)
          : await client.getUrl(uri);
      if (body != null) {
        req.headers.contentType = ContentType.json;
        req.write(jsonEncode(body));
      }
      final resp = await req.close().timeout(const Duration(seconds: 6));
      final text = await resp.transform(utf8.decoder).join();
      if (resp.statusCode >= 400) {
        throw Exception(text.isEmpty ? 'Error ${resp.statusCode}' : text);
      }
      return text.isEmpty ? null : jsonDecode(text);
    } finally {
      client.close();
    }
  }

  Future<void> _refresh() async {
    // Friends ride the same cadence as the lobby list: loaded on
    // open (initState calls _refresh) and on every timed refresh.
    _loadFriends();
    try {
      final data = await _api('GET', '/lobbies');
      if (!mounted) return;
      setState(() {
        _online = true;
        _lobbies = (data['lobbies'] as List?) ?? [];
        _status = _lobbies.isEmpty
            ? 'Server online. No open lobbies right now: create one and tell a friend to join.'
            : 'Server online. Pick a lobby to join, or create your own.';
        if (_sel >= _itemCount) _sel = _itemCount - 1;
      });
    } catch (_) {
      if (!mounted) return;
      setState(() {
        _online = false;
        _status = 'Cannot reach the lobby server. It may be offline.';
      });
    }
  }

  // Friends of the current player, from the same server the
  // leaderboard uses. A failed load just leaves the section as it
  // was (empty on first open): no error noise over the lobby list.
  Future<void> _loadFriends() async {
    try {
      final player = await _playerName();
      final data = await _api(
          'GET', '/friends?player=${Uri.encodeQueryComponent(player)}');
      if (!mounted) return;
      setState(() {
        _friends = (data['friends'] as List?) ?? [];
        if (_sel >= _itemCount) _sel = _itemCount - 1;
      });
    } catch (_) {}
  }

  // OK on a friend row. Someone in a lobby joins that lobby through
  // the normal join flow when the open lobby list has it (matched
  // by host or guest name); anything else just notes it in the
  // status line and stays on this screen.
  void _tapFriend(Map<String, dynamic> f) {
    final name = (f['name'] ?? '').toString();
    if (f['status'] != 'in_lobby') {
      setState(() => _status = '$name is not in a lobby right now');
      return;
    }
    Map<String, dynamic>? found;
    for (final l in _lobbies) {
      final m = l as Map<String, dynamic>;
      if (m['hostPlayer']?.toString() == name ||
          m['guestPlayer']?.toString() == name ||
          m['guest']?.toString() == name) {
        found = m;
        break;
      }
    }
    if (found == null) {
      // The friend is in a lobby the open list cannot offer (full,
      // already started, or private): say so and stay here.
      setState(() => _status = 'That lobby is not joinable right now');
      return;
    }
    _joinLobby(found);
  }

  Future<String> _playerName() async {
    final p = await Prefs.get('current_player');
    return (p == null || p.isEmpty) ? 'Player 1' : p;
  }

  Future<LocalGame?> _pickGame(List<LocalGame> games) {
    return showDialog<LocalGame>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: const Color(0xFF262640),
        title: const Text('Pick the game to host'),
        content: SizedBox(
          width: 560,
          height: 380,
          child: ListView.builder(
            itemCount: games.length,
            itemBuilder: (_, i) {
              final g = games[i];
              return Padding(
                padding: const EdgeInsets.symmetric(vertical: 3),
                child: ClipRRect(
                  borderRadius: BorderRadius.circular(10),
                  child: Material(
                    color: const Color(0xFF1B1B28),
                    child: InkWell(
                      autofocus: i == 0,
                      onTap: () => Navigator.pop(ctx, g),
                      child: Padding(
                        padding: const EdgeInsets.symmetric(
                            horizontal: 16, vertical: 12),
                        child: Row(
                          children: [
                            Expanded(
                              child: Text(g.display,
                                  style: const TextStyle(
                                      fontSize: 19,
                                      fontWeight: FontWeight.w600)),
                            ),
                            Text(g.system.name,
                                style: const TextStyle(
                                    fontSize: 15, color: Colors.white60)),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
              );
            },
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel', style: TextStyle(fontSize: 17)),
          ),
        ],
      ),
    ).then((v) {
      _focusNode.requestFocus();
      return v;
    });
  }

  Future<void> _launchOnline(LocalGame game,
      {required String role,
      required String lobbyId,
      required String token,
      String password = '',
      String tournament = ''}) async {
    await PlayStats.record(game.file.path);
    await _channel.invokeMethod('launch', {
      'core': game.system.core,
      'rom': game.file.path,
      'npRole': role,
      'npLobby': lobbyId,
      'npToken': token,
      'npServer': kServerBase,
      // The game screen keeps the password so a seat rotation
      // can re-enter the room (watch or join) on its own.
      'npPassword': password,
      // Only meaningful for the host: the game screen reports
      // the room's results to the tournament board under this
      // name. Guests launch with it blank and learn the name
      // from the host inside the room.
      'npTournament': tournament,
    });
  }

  Future<void> _createLobby() async {
    final player = await _playerName();
    if (!mounted) return;
    final games = await scanLocalGames();
    if (!mounted) return;
    if (games.isEmpty) {
      _showMessage('No games on this box',
          'There are no games to host. Add games first, then come back.');
      return;
    }
    final game = await _pickGame(games);
    if (game == null || !mounted) return;
    final password = await showTvKeyboard(context,
        title: 'Password (blank = open to anyone)', initial: '');
    if (password == null || !mounted) return;
    final tournament = await showTvKeyboard(context,
        title: 'Tournament name (blank = casual game)', initial: '');
    if (tournament == null || !mounted) return;
    try {
      final data = await _api('POST', '/lobbies', {
        'name': "$player's lobby",
        'game': game.fileName,
        'hostPlayer': player,
        'password': password,
      });
      if (!mounted) return;
      final lobbyId = (data['id'] ?? data['lobby']?['id'] ?? '').toString();
      final token = (data['hostToken'] ?? '').toString();
      if (lobbyId.isEmpty || token.isEmpty) {
        _showMessage('Could not create the lobby',
            'The server did not send a lobby id. Try again.');
        return;
      }
      appLog('lobby created: $lobbyId for ${game.fileName} by $player');
      await _launchOnline(game,
          role: 'host',
          lobbyId: lobbyId,
          token: token,
          password: password,
          tournament: tournament.trim());
    } catch (e) {
      appLog('create lobby failed: $e');
      _showMessage('Could not create the lobby', '$e');
    }
  }

  Future<void> _joinLobby(Map<String, dynamic> lobby) async {
    final player = await _playerName();
    if (!mounted) return;
    var password = '';
    if (lobby['locked'] == true) {
      final pw = await showTvKeyboard(context,
          title: 'Lobby password', initial: '');
      if (pw == null || !mounted) return;
      password = pw;
    }
    try {
      final data = await _api('POST', '/lobbies/${lobby['id']}/join', {
        'player': player,
        'password': password,
      });
      if (!mounted) return;
      final token = (data['guestToken'] ?? '').toString();
      if (token.isEmpty) {
        _showMessage('Could not join', 'The server did not send a token.');
        return;
      }
      final games = await scanLocalGames();
      LocalGame? match;
      for (final g in games) {
        if (g.fileName == lobby['game']) {
          match = g;
          break;
        }
      }
      if (!mounted) return;
      if (match == null) {
        _showMessage('You do not have this game',
            '"${lobby['game']}" is not on this box. Both players need '
                'the same game file to play together.');
        return;
      }
      appLog(
          'lobby joined: ${lobby['id']} for ${match.fileName} by $player');
      await _launchOnline(match,
          role: 'guest',
          lobbyId: lobby['id'].toString(),
          token: token,
          password: password);
    } catch (e) {
      appLog('join lobby failed: $e');
      _showMessage('Could not join', '$e');
    }
  }

  // Watch a lobby without taking a seat: the server hands back a
  // spectator token, and the game screen reconstructs the match
  // locally from both players' inputs. Watching runs the same
  // game on this box, so the game file has to be in the library,
  // exactly like joining.
  Future<void> _watchLobby(Map<String, dynamic> lobby) async {
    final player = await _playerName();
    if (!mounted) return;
    var password = '';
    if (lobby['locked'] == true) {
      final pw = await showTvKeyboard(context,
          title: 'Lobby password', initial: '');
      if (pw == null || !mounted) return;
      password = pw;
    }
    try {
      final data = await _api('POST', '/lobbies/${lobby['id']}/watch', {
        'player': player,
        'password': password,
      });
      if (!mounted) return;
      final token = (data['spectatorToken'] ?? '').toString();
      if (token.isEmpty) {
        setState(() =>
            _status = 'The server did not send a spectator token.');
        return;
      }
      final games = await scanLocalGames();
      LocalGame? match;
      for (final g in games) {
        if (g.fileName == lobby['game']) {
          match = g;
          break;
        }
      }
      if (!mounted) return;
      if (match == null) {
        setState(() =>
            _status = 'You need this game in your library to watch');
        return;
      }
      appLog(
          'watch joined: ${lobby['id']} for ${match.fileName} by $player');
      await _channel.invokeMethod('launch', {
        'core': match.system.core,
        'rom': match.file.path,
        'mode': 'watch',
        'serverBase': kServerBase,
        'lobbyId': lobby['id'].toString(),
        'token': token,
        'playerName': player,
        'npPassword': password,
      });
    } catch (e) {
      appLog('watch lobby failed: $e');
      if (!mounted) return;
      // The server's own words (bad password, room full of
      // watchers) belong on the status line, like a failed join.
      setState(() =>
          _status = e.toString().replaceFirst('Exception: ', ''));
    }
  }

  void _showMessage(String title, String body) {
    if (!mounted) return;
    showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: const Color(0xFF262640),
        title: Text(title),
        content: Text(body, style: const TextStyle(fontSize: 17)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('OK', style: TextStyle(fontSize: 17)),
          ),
        ],
      ),
    ).then((_) => _focusNode.requestFocus());
  }

  void _activate() {
    if (_sel == 0) {
      _createLobby();
    } else if (_sel == 1) {
      _refresh();
    } else if (_sel == _itemCount - 1) {
      Navigator.pop(context);
    } else if (_sel < 2 + _friends.length) {
      _tapFriend(_friends[_sel - 2] as Map<String, dynamic>);
    } else {
      // Lobby stops come in pairs on one card: even offset is the
      // Join stop, odd offset is the Watch stop.
      final off = _sel - 2 - _friends.length;
      final lobby = _lobbies[off ~/ 2] as Map<String, dynamic>;
      if (off % 2 == 0) {
        _joinLobby(lobby);
      } else {
        _watchLobby(lobby);
      }
    }
  }

  void _onKey(KeyEvent event) {
    // Repeats count too, so holding UP or DOWN runs through the
    // lobby list instead of crawling one row per press.
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) return;
    if (ModalRoute.of(context)?.isCurrent == false) return;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowUp) {
      setState(() => _sel = (_sel - 1 + _itemCount) % _itemCount);
      _scrollToSel();
    } else if (key == LogicalKeyboardKey.arrowDown) {
      setState(() => _sel = (_sel + 1) % _itemCount);
      _scrollToSel();
    } else if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.select ||
        key == LogicalKeyboardKey.space ||
        key == LogicalKeyboardKey.gameButtonA) {
      _activate();
    } else if (key == LogicalKeyboardKey.escape ||
        key == LogicalKeyboardKey.gameButtonB ||
        key == LogicalKeyboardKey.browserBack) {
      Navigator.pop(context);
    }
  }

  Widget _actionButton(int index, IconData icon, String label,
      {bool primary = false}) {
    final focused = _sel == index;
    return GestureDetector(
      onTap: () {
        setState(() => _sel = index);
        _activate();
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 120),
        padding: const EdgeInsets.symmetric(horizontal: 22, vertical: 13),
        decoration: BoxDecoration(
          color: primary
              ? const Color(0xFF00E676)
              : const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(12),
          border: Border.all(
            color: focused
                ? Colors.white
                : (primary ? Colors.white54 : Colors.white24),
            width: focused ? 3 : 1.5,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon,
                size: 20, color: primary ? Colors.black : Colors.white),
            const SizedBox(width: 9),
            Text(
              label,
              style: TextStyle(
                fontSize: 18,
                fontWeight: FontWeight.bold,
                color: primary ? Colors.black : Colors.white,
              ),
            ),
          ],
        ),
      ),
    );
  }

  // One small pill on a lobby card (Join / Watch). Each pill is
  // its own focus stop in the flat index walk; tapping it selects
  // the stop and fires its action at once.
  Widget _lobbyAction(
      int index, String label, VoidCallback action,
      {bool primary = false}) {
    final focused = _sel == index;
    return GestureDetector(
      onTap: () {
        setState(() => _sel = index);
        action();
      },
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        decoration: BoxDecoration(
          color: primary
              ? const Color(0xFF00E676)
              : const Color(0xFF23233A),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: focused
                ? Colors.white
                : (primary ? Colors.white54 : Colors.white24),
            width: focused ? 2.5 : 1.2,
          ),
        ),
        child: Text(
          label,
          style: TextStyle(
            fontSize: 15.5,
            fontWeight: FontWeight.bold,
            color: primary ? Colors.black : Colors.white,
          ),
        ),
      ),
    );
  }

  Widget _lobbyRow(int lobbyIndex, Map<String, dynamic> lobby) {
    // The card owns two consecutive focus stops: Join, then Watch.
    final joinIndex = 2 + _friends.length + 2 * lobbyIndex;
    final watchIndex = joinIndex + 1;
    final focused = _sel == joinIndex || _sel == watchIndex;
    final locked = lobby['locked'] == true;
    final host = (lobby['hostPlayer'] ?? 'Player').toString();
    final spectators = (lobby['spectators'] as num?)?.toInt() ?? 0;
    // Both stops scroll to this card, so they share its key.
    final cardKey = _rowKeys.putIfAbsent(joinIndex, () => GlobalKey());
    _rowKeys[watchIndex] = cardKey;
    return GestureDetector(
      onTap: () {
        setState(() => _sel = joinIndex);
        _joinLobby(lobby);
      },
      child: AnimatedContainer(
        key: cardKey,
        duration: const Duration(milliseconds: 120),
        margin: const EdgeInsets.only(bottom: 12),
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 14),
        decoration: BoxDecoration(
          color: focused
              ? const Color(0xFF23233A)
              : const Color(0xFF1B1B28),
          borderRadius: BorderRadius.circular(14),
          border: Border.all(
            color: focused ? Colors.white : Colors.white12,
            width: focused ? 3 : 1,
          ),
        ),
        child: Row(
          children: [
            // Game tile.
            Container(
              width: 52,
              height: 52,
              decoration: BoxDecoration(
                color: const Color(0xFF2A2A4A),
                borderRadius: BorderRadius.circular(12),
              ),
              child: const Icon(Icons.sports_esports,
                  size: 27, color: Color(0xFF00E676)),
            ),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('${lobby['name']}',
                      style: const TextStyle(
                          fontSize: 20, fontWeight: FontWeight.bold),
                      overflow: TextOverflow.ellipsis),
                  const SizedBox(height: 2),
                  Text('${lobby['game']}',
                      style: const TextStyle(
                          fontSize: 15, color: Colors.white70),
                      overflow: TextOverflow.ellipsis),
                  const SizedBox(height: 5),
                  Row(
                    children: [
                      PlayerAvatar(name: host, size: 20),
                      const SizedBox(width: 7),
                      Text('Host: $host',
                          style: const TextStyle(
                              fontSize: 14, color: Colors.white54)),
                    ],
                  ),
                ],
              ),
            ),
            const SizedBox(width: 14),
            Column(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                // Open / Locked chip.
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                  decoration: BoxDecoration(
                    color: locked
                        ? Colors.amber.withOpacity(0.16)
                        : const Color(0xFF00E676).withOpacity(0.14),
                    borderRadius: BorderRadius.circular(999),
                    border: Border.all(
                      color: locked
                          ? Colors.amber.withOpacity(0.6)
                          : const Color(0xFF00E676).withOpacity(0.55),
                    ),
                  ),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(locked ? Icons.lock : Icons.lock_open,
                          size: 14,
                          color: locked
                              ? Colors.amber
                              : const Color(0xFF00E676)),
                      const SizedBox(width: 5),
                      Text(
                        locked ? 'Locked' : 'Open',
                        style: TextStyle(
                          fontSize: 13.5,
                          fontWeight: FontWeight.bold,
                          color: locked
                              ? Colors.amber
                              : const Color(0xFF00E676),
                        ),
                      ),
                    ],
                  ),
                ),
                const SizedBox(height: 8),
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const Icon(Icons.people, size: 16, color: Colors.white54),
                    const SizedBox(width: 5),
                    Text(
                      '${lobby['players']}/${lobby['maxPlayers']}',
                      style: const TextStyle(
                          fontSize: 14.5, color: Colors.white70),
                    ),
                  ],
                ),
                if (spectators > 0) ...[
                  const SizedBox(height: 6),
                  Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const Icon(Icons.visibility,
                          size: 15, color: Colors.white54),
                      const SizedBox(width: 5),
                      Text(
                        '$spectators watching',
                        style: const TextStyle(
                            fontSize: 13.5, color: Colors.white54),
                      ),
                    ],
                  ),
                ],
                const SizedBox(height: 10),
                Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    _lobbyAction(
                        joinIndex, 'Join', () => _joinLobby(lobby),
                        primary: true),
                    const SizedBox(width: 8),
                    _lobbyAction(
                        watchIndex, 'Watch', () => _watchLobby(lobby)),
                  ],
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  // Section label over a group of rows in the list, with a count
  // chip. Labels are visual only: they own no focus index.
  Widget _sectionHeader(String label, int count) {
    return Row(
      children: [
        Text(
          label,
          style: const TextStyle(fontSize: 20, fontWeight: FontWeight.bold),
        ),
        const SizedBox(width: 10),
        Container(
          padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 2),
          decoration: BoxDecoration(
            color: Colors.white10,
            borderRadius: BorderRadius.circular(999),
            border: Border.all(color: Colors.white24),
          ),
          child: Text(
            '$count',
            style: const TextStyle(fontSize: 13.5, color: Colors.white70),
          ),
        ),
      ],
    );
  }

  Widget _friendRow(int index, Map<String, dynamic> f) {
    final focused = _sel == index;
    final name = (f['name'] ?? 'Player').toString();
    final display = (f['display'] ?? '').toString();
    final region = (f['region'] ?? '').toString();
    final inLobby = f['status'] == 'in_lobby';
    final game = (f['game'] ?? '').toString();
    return GestureDetector(
      onTap: () {
        setState(() => _sel = index);
        _tapFriend(f);
      },
      child: AnimatedContainer(
        key: _rowKeys.putIfAbsent(index, () => GlobalKey()),
        duration: const Duration(milliseconds: 120),
        margin: const EdgeInsets.only(bottom: 12),
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 14),
        decoration: BoxDecoration(
          color: focused
              ? const Color(0xFF23233A)
              : const Color(0xFF1B1B28),
          borderRadius: BorderRadius.circular(14),
          border: Border.all(
            color: focused ? Colors.white : Colors.white12,
            width: focused ? 3 : 1,
          ),
        ),
        child: Row(
          children: [
            PlayerAvatar(name: name, size: 46),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Flexible(
                        child: Text(
                          display.isNotEmpty ? display : name,
                          style: const TextStyle(
                              fontSize: 20, fontWeight: FontWeight.bold),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                      if (region.isNotEmpty) ...[
                        const SizedBox(width: 9),
                        Container(
                          padding: const EdgeInsets.symmetric(
                              horizontal: 8, vertical: 2),
                          decoration: BoxDecoration(
                            color: Colors.white10,
                            borderRadius: BorderRadius.circular(999),
                            border: Border.all(color: Colors.white24),
                          ),
                          child: Text(
                            region,
                            style: const TextStyle(
                                fontSize: 12, color: Colors.white70),
                          ),
                        ),
                      ],
                    ],
                  ),
                  const SizedBox(height: 3),
                  Row(
                    children: [
                      Icon(Icons.circle,
                          size: 11,
                          color: inLobby
                              ? const Color(0xFF00E676)
                              : Colors.white38),
                      const SizedBox(width: 7),
                      Expanded(
                        child: Text(
                          inLobby
                              ? (game.isNotEmpty
                                  ? 'In lobby: $game'
                                  : 'In a lobby')
                              : 'Offline',
                          style: TextStyle(
                              fontSize: 15,
                              color: inLobby
                                  ? Colors.white70
                                  : Colors.white54),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
            if (inLobby)
              const Icon(Icons.play_circle_outline,
                  size: 26, color: Color(0xFF00E676)),
          ],
        ),
      ),
    );
  }

  // The lobby empty state, sized to live inside the shared list
  // under the Open lobbies label.
  Widget _noLobbies() {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 10),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Container(
            width: 72,
            height: 72,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: Colors.white.withOpacity(0.05),
              border: Border.all(color: Colors.white12),
            ),
            child: const Icon(Icons.sports_esports,
                size: 34, color: Colors.white38),
          ),
          const SizedBox(height: 14),
          const Text('No open lobbies right now',
              style: TextStyle(
                  fontSize: 19,
                  fontWeight: FontWeight.bold,
                  color: Colors.white70)),
          const SizedBox(height: 6),
          const Text(
            'Create one above and tell a friend to join. '
            'Both of you need the same game file.',
            style: TextStyle(fontSize: 15, color: Colors.white54),
            textAlign: TextAlign.center,
          ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
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
          child: Padding(
            padding: const EdgeInsets.fromLTRB(58, 30, 58, 26),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                // Header: badge, title, and the server status pill.
                Row(
                  children: [
                    Container(
                      width: 54,
                      height: 54,
                      decoration: BoxDecoration(
                        color: const Color(0xFF00E676).withOpacity(0.14),
                        borderRadius: BorderRadius.circular(14),
                        border: Border.all(
                          color: const Color(0xFF00E676).withOpacity(0.45),
                        ),
                      ),
                      child: const Icon(Icons.public,
                          size: 29, color: Color(0xFF00E676)),
                    ),
                    const SizedBox(width: 16),
                    const Text('Online Play',
                        style: TextStyle(
                            fontSize: 34, fontWeight: FontWeight.w800)),
                    const Spacer(),
                    Container(
                      padding: const EdgeInsets.symmetric(
                          horizontal: 14, vertical: 7),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1E1E2E),
                        borderRadius: BorderRadius.circular(999),
                        border: Border.all(color: Colors.white24),
                      ),
                      child: Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Icon(Icons.circle,
                              size: 12,
                              color: _online
                                  ? const Color(0xFF00E676)
                                  : Colors.redAccent),
                          const SizedBox(width: 8),
                          Text(
                            _online ? 'Server online' : 'Server offline',
                            style: const TextStyle(
                                fontSize: 15, fontWeight: FontWeight.bold),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 10),
                Text(_status,
                    style: const TextStyle(
                        fontSize: 16.5, color: Colors.white70)),
                const SizedBox(height: 18),
                Row(
                  children: [
                    _actionButton(0, Icons.add_circle, 'Create lobby',
                        primary: true),
                    const SizedBox(width: 14),
                    _actionButton(1, Icons.refresh, 'Refresh'),
                  ],
                ),
                const SizedBox(height: 22),
                // One scrolling list: the Friends section over the
                // Open lobbies section. Section labels and empty
                // lines are list children too, but they are not
                // selectable and own no focus index.
                Expanded(
                  child: ListView(
                    controller: _listScroll,
                    children: [
                      _sectionHeader('Friends', _friends.length),
                      const SizedBox(height: 6),
                      Container(height: 1, color: Colors.white10),
                      const SizedBox(height: 14),
                      if (_friends.isEmpty)
                        const Padding(
                          padding: EdgeInsets.symmetric(
                              horizontal: 4, vertical: 6),
                          child: Text(
                            'No friends yet. Open the Leaderboard and '
                            'press OK on a player to add them.',
                            style: TextStyle(
                                fontSize: 15.5, color: Colors.white54),
                          ),
                        )
                      else
                        for (var i = 0; i < _friends.length; i++)
                          _friendRow(2 + i,
                              _friends[i] as Map<String, dynamic>),
                      const SizedBox(height: 12),
                      _sectionHeader('Open lobbies', _lobbies.length),
                      const SizedBox(height: 6),
                      Container(height: 1, color: Colors.white10),
                      const SizedBox(height: 14),
                      if (_lobbies.isEmpty)
                        _noLobbies()
                      else
                        for (var i = 0; i < _lobbies.length; i++)
                          _lobbyRow(
                              i, _lobbies[i] as Map<String, dynamic>),
                    ],
                  ),
                ),
                const SizedBox(height: 8),
                Row(
                  children: [
                    _actionButton(_itemCount - 1, Icons.arrow_back, 'Back'),
                    const Spacer(),
                    const Text(
                      'UP / DOWN browse · OK to select · BACK returns',
                      style: TextStyle(fontSize: 14, color: Colors.white38),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Self-update: the server publishes /app/version.json plus the APK.
// The app compares version codes; the native side downloads the APK,
// verifies its SHA-256, and opens the system installer (Android
// always shows its own Install prompt; that press is the user's).
// ---------------------------------------------------------------------------

class AppUpdateInfo {
  final int versionCode;
  final String versionName;
  final String sha256;
  final String notes;
  final int size; // APK bytes from version.json; 0 when unpublished
  const AppUpdateInfo(
      this.versionCode, this.versionName, this.sha256, this.notes, this.size);
}

class UpdateCheck {
  final int currentCode;
  final String currentName;
  final AppUpdateInfo? available;
  const UpdateCheck(this.currentCode, this.currentName, this.available);
}

class AppUpdate {
  static const _channel = MethodChannel('net.itak/emu');
  static const _progressChannel =
      EventChannel('net.itak/emu/update_progress');

  /// Byte progress from the native downloader while install()
  /// runs: maps with int 'downloaded' and 'total' (total 0 means
  /// the size is unknown and the bar should stay indeterminate).
  static Stream<Map<String, int>> progress() {
    return _progressChannel.receiveBroadcastStream().map((e) {
      final m = (e as Map).map((k, v) => MapEntry(k.toString(), v));
      return {
        'downloaded': (m['downloaded'] as num?)?.toInt() ?? 0,
        'total': (m['total'] as num?)?.toInt() ?? 0,
      };
    });
  }

  static Future<Map<String, dynamic>?> installed() async {
    try {
      final m = await _channel.invokeMethod<Map>('appInfo');
      return m?.map((k, v) => MapEntry(k.toString(), v));
    } catch (_) {
      return null;
    }
  }

  /// Null when the check itself failed (offline, server down).
  static Future<UpdateCheck?> check() async {
    final info = await installed();
    final code = (info?['versionCode'] as num?)?.toInt() ?? 0;
    final name = info?['versionName']?.toString() ?? '';
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 6);
    try {
      final req =
          await client.getUrl(Uri.parse('$kServerBase/app/version.json'));
      req.headers.set(HttpHeaders.userAgentHeader, 'iTaK-Gaming/1.0');
      final resp = await req.close().timeout(const Duration(seconds: 6));
      if (resp.statusCode != 200) return UpdateCheck(code, name, null);
      final body = await resp.transform(utf8.decoder).join();
      final j = jsonDecode(body) as Map<String, dynamic>;
      final latest = (j['versionCode'] as num?)?.toInt() ?? 0;
      AppUpdateInfo? avail;
      if (latest > code) {
        avail = AppUpdateInfo(
          latest,
          j['versionName']?.toString() ?? '',
          j['sha256']?.toString() ?? '',
          j['notes']?.toString() ?? '',
          (j['size'] as num?)?.toInt() ?? 0,
        );
      }
      return UpdateCheck(code, name, avail);
    } catch (_) {
      return null;
    } finally {
      client.close(force: true);
    }
  }

  /// "" = installer opened; "needs-permission"; else an error line.
  /// Always downloads the server's current latest.apk, so a box
  /// many builds behind jumps straight to the newest build in this
  /// one step; there is no version-by-version chain.
  static Future<String> install(AppUpdateInfo u) async {
    try {
      final r = await _channel.invokeMethod<String>('installUpdate', {
        'url': '$kServerBase/app/latest.apk',
        'sha256': u.sha256,
        'size': u.size,
      });
      return r ?? '';
    } catch (e) {
      return 'Update failed: $e';
    }
  }
}

Future<void> showAppUpdateDialog(BuildContext context, UpdateCheck check,
    {required bool manual}) async {
  const bg = Color(0xFF262640);
  final avail = check.available;
  if (avail == null) {
    if (!manual) return;
    await showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: bg,
        title: const Text('App updates'),
        content: Text('You are up to date.\n\niTaK Gaming ${check.currentName} is the newest build.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('OK', style: TextStyle(color: Color(0xFF00E676))),
          ),
        ],
      ),
    );
    return;
  }
  final go = await showDialog<bool>(
    context: context,
    barrierColor: Colors.black.withOpacity(0.8),
    builder: (ctx) => AlertDialog(
      backgroundColor: bg,
      title: const Text('Update available'),
      content: Text(
          'iTaK Gaming ${avail.versionName} is ready to install.${avail.notes.isEmpty ? '' : '\n\n${avail.notes}'}'),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(ctx, false),
          child: const Text('Later', style: TextStyle(color: Colors.white70)),
        ),
        TextButton(
          onPressed: () => Navigator.pop(ctx, true),
          child: const Text('Update now',
              style: TextStyle(
                  color: Color(0xFF00E676), fontWeight: FontWeight.bold)),
        ),
      ],
    ),
  );
  if (go != true || !context.mounted) return;
  var result = await _downloadWithProgress(context, avail);
  while (true) {
    if (result.isEmpty) return; // the system installer is on screen
    if (!context.mounted) return;
    if (result == 'needs-permission') {
      await showDialog<void>(
        context: context,
        barrierColor: Colors.black.withOpacity(0.8),
        builder: (ctx) => AlertDialog(
          backgroundColor: bg,
          title: const Text('One more step'),
          content: const Text(
              'The TV opened a Settings page. Turn ON "Allow" for iTaK Gaming, press BACK, then choose Update again.'),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(ctx),
              child:
                  const Text('OK', style: TextStyle(color: Color(0xFF00E676))),
            ),
          ],
        ),
      );
      return;
    }
    // A failed download is never a dead end: try again or later.
    final retry = await showDialog<bool>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: bg,
        title: const Text('Update failed'),
        content: Text(result),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child:
                const Text('Later', style: TextStyle(color: Colors.white70)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Try again',
                style: TextStyle(
                    color: Color(0xFF00E676), fontWeight: FontWeight.bold)),
          ),
        ],
      ),
    );
    if (retry != true || !context.mounted) return;
    result = await _downloadWithProgress(context, avail);
  }
}

/// Shows the live download progress dialog while the native side
/// fetches the APK, then returns install()'s result string.
Future<String> _downloadWithProgress(
    BuildContext context, AppUpdateInfo avail) async {
  showDialog<void>(
    context: context,
    barrierDismissible: false,
    barrierColor: Colors.black.withOpacity(0.8),
    builder: (_) => _UpdateProgressDialog(expectedSize: avail.size),
  );
  final result = await AppUpdate.install(avail);
  if (context.mounted) {
    Navigator.of(context).pop(); // progress dialog
  }
  return result;
}

/// The update download dialog: a real progress bar with percent
/// and MB counts, driven by the native downloader's byte stream.
/// Falls back to an indeterminate bar (with MB downloaded) when
/// the size is unknown.
class _UpdateProgressDialog extends StatefulWidget {
  final int expectedSize;
  const _UpdateProgressDialog({required this.expectedSize});

  @override
  State<_UpdateProgressDialog> createState() => _UpdateProgressDialogState();
}

class _UpdateProgressDialogState extends State<_UpdateProgressDialog> {
  StreamSubscription<Map<String, int>>? _sub;
  int _downloaded = 0;
  int _total = 0;

  @override
  void initState() {
    super.initState();
    _total = widget.expectedSize;
    _sub = AppUpdate.progress().listen((p) {
      if (!mounted) return;
      setState(() {
        _downloaded = p['downloaded'] ?? 0;
        if ((p['total'] ?? 0) > 0) _total = p['total']!;
      });
    });
  }

  @override
  void dispose() {
    _sub?.cancel();
    super.dispose();
  }

  static String _mb(int bytes) =>
      (bytes / (1024 * 1024)).toStringAsFixed(1);

  @override
  Widget build(BuildContext context) {
    final hasTotal = _total > 0;
    final frac =
        hasTotal ? (_downloaded / _total).clamp(0.0, 1.0).toDouble() : null;
    final pct = frac == null ? '' : ' · ${(frac * 100).floor()}%';
    return AlertDialog(
      backgroundColor: const Color(0xFF262640),
      title: const Text('Downloading update...'),
      content: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          LinearProgressIndicator(
            value: frac,
            minHeight: 10,
            color: const Color(0xFF00E676),
            backgroundColor: Colors.white12,
          ),
          const SizedBox(height: 14),
          Text(
            hasTotal
                ? '${_mb(_downloaded)} MB of ${_mb(_total)} MB$pct'
                : '${_mb(_downloaded)} MB downloaded',
            style: const TextStyle(fontSize: 16, color: Colors.white70),
          ),
        ],
      ),
    );
  }
}

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
  bool _autoSave = true;
  bool _friendAlerts = true;
  String _versionName = '';
  bool _checkingUpdate = false;
  int _logSize = 0;
  final FocusNode _focusNode = FocusNode();
  final ScrollController _settingsScroll = ScrollController();

  static const int _rowCount = 10;
  // Picture, Display, Swap, Auto art, Auto-save, Friend alerts,
  // Sound test, App updates, Logs, Back

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
    final asv = await Prefs.get('autosave');
    final fa = await Prefs.get('friend_alerts');
    final ai = await AppUpdate.installed();
    try {
      final info = await _logChannel
          .invokeMethod<Map<dynamic, dynamic>>('logInfo');
      _logSize = (info?['size'] as num?)?.toInt() ?? 0;
    } catch (_) {}
    if (!mounted) return;
    setState(() {
      _filter = (f ?? 0).clamp(0, kFilterNames.length - 1);
      _display = (d ?? 0).clamp(0, kDisplayNames.length - 1);
      _swapAB = (sw ?? 0) != 0;
      _autoArt = a != '0';
      _autoSave = asv != '0';
      _friendAlerts = fa != '0';
      _versionName = ai?['versionName']?.toString() ?? '';
    });
  }

  void _openLogs() {
    appLog('opened the Logs screen');
    Navigator.push(
      context,
      MaterialPageRoute(builder: (_) => const LogScreen()),
    );
  }

  Future<void> _openSoundTest() async {
    appLog('opened the Sound test screen');
    try {
      await _logChannel.invokeMethod('soundTest');
    } catch (_) {}
  }

  Future<void> _openUpdates() async {
    if (_checkingUpdate) return;
    setState(() => _checkingUpdate = true);
    final check = await AppUpdate.check();
    if (mounted) setState(() => _checkingUpdate = false);
    if (!mounted) return;
    if (check == null) {
      await showDialog<void>(
        context: context,
        barrierColor: Colors.black.withOpacity(0.8),
        builder: (ctx) => AlertDialog(
          backgroundColor: const Color(0xFF262640),
          title: const Text('App updates'),
          content: const Text(
              'Cannot reach the update server. Check the internet connection and try again.'),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(ctx),
              child:
                  const Text('OK', style: TextStyle(color: Color(0xFF00E676))),
            ),
          ],
        ),
      );
      return;
    }
    showAppUpdateDialog(context, check, manual: true);
  }

  @override
  void dispose() {
    _focusNode.dispose();
    _settingsScroll.dispose();
    super.dispose();
  }

  // Nine rows no longer fit on one TV screen: keep the focused
  // row in view as the remote walks the list.
  void _ensureRowVisible() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_settingsScroll.hasClients) return;
      final target = (_row * 99.0 - 150)
          .clamp(0.0, _settingsScroll.position.maxScrollExtent);
      _settingsScroll.animateTo(
        target,
        duration: const Duration(milliseconds: 180),
        curve: Curves.easeOut,
      );
    });
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
    } else if (_row == 4) {
      setState(() => _autoSave = !_autoSave);
      Prefs.set('autosave', _autoSave ? '1' : '0');
    } else if (_row == 5) {
      setState(() => _friendAlerts = !_friendAlerts);
      Prefs.set('friend_alerts', _friendAlerts ? '1' : '0');
    }
  }

  void _onKey(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    // Dialogs above this screen own the remote while they are up.
    if (ModalRoute.of(context)?.isCurrent != true) return;
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.arrowUp) {
      setState(() => _row = (_row - 1 + _rowCount) % _rowCount);
      _ensureRowVisible();
    } else if (key == LogicalKeyboardKey.arrowDown) {
      setState(() => _row = (_row + 1) % _rowCount);
      _ensureRowVisible();
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      _adjust(-1);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      _adjust(1);
    } else if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.select ||
        key == LogicalKeyboardKey.space) {
      if (_row == 9) {
        Navigator.pop(context);
      } else if (_row == 8) {
        _openLogs();
      } else if (_row == 7) {
        _openUpdates();
      } else if (_row == 6) {
        _openSoundTest();
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
              child: SingleChildScrollView(
                controller: _settingsScroll,
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
                  _settingRow(
                    index: 4,
                    label: 'Auto-save',
                    value: _autoSave ? 'On' : 'Off',
                    hint: 'Saves your game every minute and when you exit, so you never lose progress',
                  ),
                  _settingRow(
                    index: 5,
                    label: 'Friend alerts',
                    value: _friendAlerts ? 'On' : 'Off',
                    hint: 'A banner and a chime when a friend comes online or starts playing',
                  ),
                  GestureDetector(
                    onTap: () {
                      setState(() => _row = 6);
                      _openSoundTest();
                    },
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 120),
                      margin: const EdgeInsets.symmetric(vertical: 7),
                      padding: const EdgeInsets.symmetric(
                          horizontal: 22, vertical: 16),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1B1B28),
                        borderRadius: BorderRadius.circular(14),
                        border: Border.all(
                          color: _row == 6 ? Colors.white : Colors.white12,
                          width: _row == 6 ? 3 : 1,
                        ),
                      ),
                      child: const Row(
                        children: [
                          Icon(Icons.graphic_eq),
                          SizedBox(width: 12),
                          Expanded(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text('Sound test',
                                    style: TextStyle(
                                        fontSize: 22,
                                        fontWeight: FontWeight.bold)),
                                Text(
                                    'Play a test sound and watch the bars: speaker and microphone checks',
                                    style: TextStyle(
                                        fontSize: 15, color: Colors.white54)),
                              ],
                            ),
                          ),
                          Text(
                            'Open',
                            style: TextStyle(
                                fontSize: 19, color: Color(0xFF00E676)),
                          ),
                        ],
                      ),
                    ),
                  ),
                  GestureDetector(
                    onTap: () {
                      setState(() => _row = 7);
                      _openUpdates();
                    },
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 120),
                      margin: const EdgeInsets.symmetric(vertical: 7),
                      padding: const EdgeInsets.symmetric(
                          horizontal: 22, vertical: 16),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1B1B28),
                        borderRadius: BorderRadius.circular(14),
                        border: Border.all(
                          color: _row == 7 ? Colors.white : Colors.white12,
                          width: _row == 7 ? 3 : 1,
                        ),
                      ),
                      child: Row(
                        children: [
                          const Icon(Icons.system_update),
                          const SizedBox(width: 12),
                          const Expanded(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text('App updates',
                                    style: TextStyle(
                                        fontSize: 22,
                                        fontWeight: FontWeight.bold)),
                                Text('Check the iTaK server for a newer build',
                                    style: TextStyle(
                                        fontSize: 15, color: Colors.white54)),
                              ],
                            ),
                          ),
                          Text(
                            _checkingUpdate
                                ? 'Checking...'
                                : 'Version $_versionName',
                            style: const TextStyle(
                                fontSize: 19, color: Color(0xFF00E676)),
                          ),
                        ],
                      ),
                    ),
                  ),
                  GestureDetector(
                    onTap: () {
                      setState(() => _row = 8);
                      _openLogs();
                    },
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 120),
                      margin: const EdgeInsets.symmetric(vertical: 7),
                      padding: const EdgeInsets.symmetric(
                          horizontal: 22, vertical: 16),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1B1B28),
                        borderRadius: BorderRadius.circular(14),
                        border: Border.all(
                          color: _row == 8 ? Colors.white : Colors.white12,
                          width: _row == 8 ? 3 : 1,
                        ),
                      ),
                      child: Row(
                        children: [
                          const Icon(Icons.terminal),
                          const SizedBox(width: 12),
                          const Expanded(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text('Logs',
                                    style: TextStyle(
                                        fontSize: 22,
                                        fontWeight: FontWeight.bold)),
                                Text(
                                    'Everything the app does, written down for debugging',
                                    style: TextStyle(
                                        fontSize: 15, color: Colors.white54)),
                              ],
                            ),
                          ),
                          Text(
                            _logSize <= 0
                                ? 'View'
                                : '${(_logSize / 1024).toStringAsFixed(0)} KB',
                            style: const TextStyle(
                                fontSize: 19, color: Color(0xFF00E676)),
                          ),
                        ],
                      ),
                    ),
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
                              _row == 9 ? Colors.white : Colors.white12,
                          width: _row == 9 ? 3 : 1,
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
      ),
    );
  }
}

// ---------------------------------------------------------------------------
// Debug log viewer (Settings -> Logs). The native side streams the
// app's whole logcat into a file; this screen reads the tail of it.
// Remote model (David, 2026-10-12): a top row he can always reach
// (Back, Refresh, Save a copy to /sdcard/iTaK, Clear with a confirm)
// and dedicated scroll buttons beside the log; DOWN moves from the
// top row to the scroll buttons, OK on a scroll button pages the
// log, and holding OK keeps scrolling.
// ---------------------------------------------------------------------------

class LogScreen extends StatefulWidget {
  const LogScreen({super.key});

  @override
  State<LogScreen> createState() => _LogScreenState();
}

class _LogScreenState extends State<LogScreen> {
  static const _channel = MethodChannel('net.itak/emu');
  final ScrollController _scroll = ScrollController();
  String _text = '';
  int _size = 0;
  bool _loading = true;
  // Remote zones: 0 = top row (0 Back, 1 Refresh, 2 Save a copy,
  // 3 Clear), 1 = the log's scroll buttons (0 page up, 1 page down).
  int _zone = 0;
  int _topIndex = 0;
  int _logBtn = 1;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  Future<void> _refresh() async {
    setState(() => _loading = true);
    try {
      final data =
          await _channel.invokeMethod<Map<dynamic, dynamic>>('readLog');
      if (!mounted) return;
      setState(() {
        _text = (data?['text'] ?? '').toString();
        _size = (data?['size'] as num?)?.toInt() ?? 0;
        _loading = false;
      });
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (_scroll.hasClients) {
          _scroll.jumpTo(_scroll.position.maxScrollExtent);
        }
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _text = 'Could not read the log: $e';
        _loading = false;
      });
    }
  }

  Future<void> _export() async {
    try {
      final path = await _channel.invokeMethod<String>('exportLog');
      if (!mounted) return;
      _showInfo('Log saved',
          'A copy is at ${path ?? 'unknown'} on this box. Grab it with a file manager or USB.');
    } catch (e) {
      if (!mounted) return;
      _showInfo('Save failed', '$e');
    }
  }

  Future<void> _clear() async {
    final ok = await showDialog<bool>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: const Color(0xFF262640),
        title: const Text('Clear the log?'),
        content: const Text(
            'The saved copy file is not touched. New lines keep being written.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Keep', style: TextStyle(color: Colors.white70)),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Clear',
                style: TextStyle(color: Color(0xFF00E676))),
          ),
        ],
      ),
    );
    if (ok != true) return;
    try {
      await _channel.invokeMethod('clearLog');
      appLog('log cleared from the Logs screen');
      _refresh();
    } catch (_) {}
  }

  void _showInfo(String title, String body) {
    showDialog<void>(
      context: context,
      barrierColor: Colors.black.withOpacity(0.8),
      builder: (ctx) => AlertDialog(
        backgroundColor: const Color(0xFF262640),
        title: Text(title),
        content: Text(body, style: const TextStyle(fontSize: 17)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child:
                const Text('OK', style: TextStyle(color: Color(0xFF00E676))),
          ),
        ],
      ),
    );
  }

  void _scrollPage(int dir) {
    if (!_scroll.hasClients) return;
    final pos = _scroll.position;
    _scroll.jumpTo((pos.pixels + dir * 320).clamp(0.0, pos.maxScrollExtent));
  }

  void _activateTop() {
    switch (_topIndex) {
      case 0:
        Navigator.of(context).pop();
        break;
      case 1:
        _refresh();
        break;
      case 2:
        _export();
        break;
      case 3:
        _clear();
        break;
    }
  }

  Widget _logPill(int index, String label) {
    final focused = _zone == 0 && _topIndex == index;
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = 0;
          _topIndex = index;
        });
        _activateTop();
      },
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 9),
        decoration: BoxDecoration(
          color: const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(10),
          border: Border.all(
            color: focused ? Colors.white : Colors.white24,
            width: focused ? 2.5 : 1,
          ),
        ),
        child: Text(
          label,
          style: TextStyle(
            fontSize: 16.5,
            fontWeight: focused ? FontWeight.bold : FontWeight.normal,
            color: Colors.white,
          ),
        ),
      ),
    );
  }

  Widget _logScrollButton(int btn, IconData icon) {
    final focused = _zone == 1 && _logBtn == btn;
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = 1;
          _logBtn = btn;
        });
        _scrollPage(btn == 0 ? -1 : 1);
      },
      child: Container(
        width: 54,
        height: 54,
        decoration: BoxDecoration(
          color: const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(12),
          border: Border.all(
            color: focused ? Colors.white : Colors.white24,
            width: focused ? 2.5 : 1,
          ),
        ),
        child: Icon(icon, size: 30, color: Colors.white),
      ),
    );
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) {
      return KeyEventResult.ignored;
    }
    if (ModalRoute.of(context)?.isCurrent != true) {
      return KeyEventResult.ignored;
    }
    final key = event.logicalKey;
    if (key == LogicalKeyboardKey.escape ||
        key == LogicalKeyboardKey.gameButtonB ||
        key == LogicalKeyboardKey.browserBack) {
      Navigator.of(context).pop();
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() {
        if (_zone == 0) {
          _topIndex = (_topIndex - 1).clamp(0, 3);
        } else {
          _zone = 0;
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowRight) {
      setState(() {
        if (_zone == 0) {
          _topIndex = (_topIndex + 1).clamp(0, 3);
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowDown) {
      setState(() {
        if (_zone == 0) {
          _zone = 1;
          _logBtn = 1;
        } else if (_logBtn == 0) {
          _logBtn = 1;
        } else {
          _scrollPage(1);
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowUp) {
      setState(() {
        if (_zone == 1) {
          if (_logBtn == 1) {
            _logBtn = 0;
          } else {
            _zone = 0;
          }
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.select ||
        key == LogicalKeyboardKey.space ||
        key == LogicalKeyboardKey.gameButtonA) {
      if (_zone == 0) {
        _activateTop();
      } else {
        _scrollPage(_logBtn == 0 ? -1 : 1);
      }
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        leadingWidth: 76,
        leading: Center(
          child: GestureDetector(
            onTap: () => Navigator.of(context).pop(),
            child: Container(
              width: 44,
              height: 44,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                border: Border.all(
                  color: (_zone == 0 && _topIndex == 0)
                      ? Colors.white
                      : Colors.white24,
                  width: (_zone == 0 && _topIndex == 0) ? 3 : 1,
                ),
              ),
              child: const Icon(Icons.arrow_back_ios_new, size: 20),
            ),
          ),
        ),
        title: const Text('Logs'),
      ),
      body: Container(
        decoration: const BoxDecoration(
          gradient: LinearGradient(
            begin: Alignment.topLeft,
            end: Alignment.bottomRight,
            colors: [Color(0xFF2A1152), Color(0xFF140B2E), Color(0xFF0A1A33)],
          ),
        ),
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 8, 20, 4),
              child: Row(
                children: [
                  Text(
                    _loading
                        ? 'Loading...'
                        : '${(_size / 1024).toStringAsFixed(1)} KB logged · newest at the bottom',
                    style:
                        const TextStyle(fontSize: 16, color: Colors.white70),
                  ),
                  const Spacer(),
                  _logPill(1, 'Refresh'),
                  const SizedBox(width: 10),
                  _logPill(2, 'Save a copy'),
                  const SizedBox(width: 10),
                  _logPill(3, 'Clear'),
                ],
              ),
            ),
            Expanded(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(20, 4, 20, 8),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    Expanded(
                      child: Focus(
                        autofocus: true,
                        onKeyEvent: _onKey,
                        child: Container(
                          padding: const EdgeInsets.all(14),
                          decoration: BoxDecoration(
                            color: Colors.black.withOpacity(0.45),
                            borderRadius: BorderRadius.circular(12),
                            border:
                                Border.all(color: Colors.white12),
                          ),
                          child: SingleChildScrollView(
                            controller: _scroll,
                            child: SizedBox(
                              width: double.infinity,
                              child: Text(
                                _text.isEmpty
                                    ? '(the log is empty)'
                                    : _text,
                                style: const TextStyle(
                                  fontFamily: 'monospace',
                                  fontSize: 13,
                                  height: 1.35,
                                  color: Color(0xFFD8F3DC),
                                ),
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Column(
                      children: [
                        _logScrollButton(0, Icons.keyboard_arrow_up),
                        const SizedBox(height: 10),
                        _logScrollButton(
                            1, Icons.keyboard_arrow_down),
                      ],
                    ),
                  ],
                ),
              ),
            ),
            const Padding(
              padding: EdgeInsets.fromLTRB(20, 0, 20, 12),
              child: Text(
                'LEFT / RIGHT pick an action · OK runs it · DOWN reaches the scroll buttons · hold OK to scroll · BACK done',
                style: TextStyle(fontSize: 14, color: Colors.white54),
              ),
            ),
          ],
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
    appLog('launch game: ${f.path} on ${widget.system.core}');
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
                  // Breathing room so the focused tile (which scales
                  // up) never runs into the search bar above it.
                  const SizedBox(height: 14),
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
                                    // Same glow rule as the home grid:
                                    // a focused tile's glow fades past
                                    // the grid edge, never slices.
                                    clipBehavior: Clip.none,
                                    cacheExtent: 0,
                                    padding: const EdgeInsets.fromLTRB(
                                        28, 10, 28, 10),
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

  /// Set for global-board rows, where the server sends totals only
  /// (no per-game breakdown).
  final int? gamesCountOverride;
  final int? secondsOverride;
  final int? playsOverride;

  /// Global rows only: the identity the player's own box reported
  /// (display name with initials, home region). Empty when the
  /// player's app has not sent them; the raw name is shown instead.
  final String display;
  final String region;
  const _PlayerStat(this.name, this.games,
      {this.gamesCountOverride,
      this.secondsOverride,
      this.playsOverride,
      this.display = '',
      this.region = ''});
  int get totalSeconds =>
      secondsOverride ?? games.fold(0, (a, g) => a + g.seconds);
  int get totalPlays =>
      playsOverride ?? games.fold(0, (a, g) => a + g.plays);
  int get gamesCount => gamesCountOverride ?? games.length;
  _GameStat? get favorite => games.isEmpty
      ? null
      : games.reduce((a, b) => a.seconds >= b.seconds ? a : b);
  _GameStat? get mostRecent => games.isEmpty
      ? null
      : games.reduce((a, b) => a.last >= b.last ? a : b);
}

/// One row of the tournament board (PROTOCOL.md 8): a player's
/// reported record from the server's tournament store, ranked
/// there by titles, then wins. Nothing here counts play time;
/// a row exists because matches were reported.
class _TPlayer {
  final String name;
  final String display;
  final String region;
  final int wins;
  final int losses;
  final int matches;
  final int tournamentsWon;
  final String lastTournament;
  final String lastGame;
  final String lastTitle;
  const _TPlayer({
    required this.name,
    this.display = '',
    this.region = '',
    this.wins = 0,
    this.losses = 0,
    this.matches = 0,
    this.tournamentsWon = 0,
    this.lastTournament = '',
    this.lastGame = '',
    this.lastTitle = '',
  });
}

/// One reported tournament match from the recent feed.
class _TResult {
  final String tournament;
  final String game;
  final String winner;
  final String loser;
  final int at;
  const _TResult({
    required this.tournament,
    required this.game,
    required this.winner,
    required this.loser,
    required this.at,
  });
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

  // Board scope: 0 = this box (Local), 1 = everyone (Global),
  // 2 = the tournament board. The switch lives in the top bar
  // now (David, 2026-10-12), next to the back button. Remote
  // focus moves through three zones: 0 = top bar (Back and the
  // board pills), 1 = filter chips, 2 = the rows. UP/DOWN
  // travel between zones and through the rows, so the back
  // button is reachable and the board itself can be browsed.
  // The tournament board has no filter chips: its zone 1 is
  // skipped in both directions.
  int _scope = 0;
  int _zone = 1;
  int _barIndex = 0; // 0 = Back, 1 = Local, 2 = Global, 3 = Tournament
  int _selIndex = 0;
  int _rowCount = 0;
  // Hold-to-scroll: holding UP or DOWN on the remote sends repeat
  // events; the streak counts them so the rows move faster the
  // longer the button is held (David, 2026-10-12).
  int _streak = 0;
  LogicalKeyboardKey? _streakKey;

  int get _rowStep => _streak < 6 ? 1 : (_streak < 16 ? 3 : 5);
  final ScrollController _listScroll = ScrollController();
  final Map<int, GlobalKey> _rowKeys = {};
  List<_PlayerStat> _gPlayers = [];
  List<_GameStat> _gGames = [];
  List<({String player, String display, String region, _GameStat game})>
      _gRecent = [];
  bool _gLoaded = false;
  bool _gFailed = false;
  // The tournament board (PROTOCOL.md 8): ranked records plus
  // the recent results feed, from the server's matches store.
  List<_TPlayer> _tPlayers = [];
  List<_TResult> _tRecent = [];
  bool _tLoaded = false;
  bool _tFailed = false;
  // Friend names for the current player, from the server's friends
  // store. Players rows carry a Friend chip when their name is in
  // here, and OK on a Players row toggles membership.
  Set<String> _friendNames = {};
  String _myName = '';

  @override
  void initState() {
    super.initState();
    _load();
    _loadFriends();
    PlayerInfoStore.load().then((_) {
      if (mounted) setState(() {});
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _focusNode.requestFocus();
    });
  }

  @override
  void dispose() {
    _listScroll.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  void _setScope(int scope) {
    if (scope == _scope) return;
    setState(() {
      _scope = scope;
      _selIndex = 0;
    });
    _scrollTop();
    if (scope == 1 && !_gLoaded) _fetchGlobal();
    if (scope == 2 && !_tLoaded) _fetchTournament();
  }

  void _setMode(int mode) {
    setState(() {
      _mode = mode;
      _selIndex = 0;
    });
    _scrollTop();
  }

  void _scrollTop() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_listScroll.hasClients) _listScroll.jumpTo(0);
    });
  }

  void _scrollToSel() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final ctx = _rowKeys[_selIndex]?.currentContext;
      if (ctx != null) {
        Scrollable.ensureVisible(
          ctx,
          duration: const Duration(milliseconds: 160),
        );
      }
    });
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is KeyUpEvent) {
      _streak = 0;
      _streakKey = null;
      return KeyEventResult.ignored;
    }
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) {
      return KeyEventResult.ignored;
    }
    final key = event.logicalKey;
    // Hold streak bookkeeping: a fresh press starts at zero, each
    // repeat of the same key grows it, a different key resets it.
    if (event is KeyDownEvent) {
      _streak = 0;
      _streakKey = key;
    } else if (_streakKey == key) {
      _streak++;
    } else {
      _streak = 0;
      _streakKey = key;
    }
    if (key == LogicalKeyboardKey.escape ||
        key == LogicalKeyboardKey.gameButtonB ||
        key == LogicalKeyboardKey.browserBack) {
      Navigator.of(context).pop();
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowUp) {
      setState(() {
        if (_zone == 2) {
          if (_selIndex > 0) {
            _selIndex = (_selIndex - _rowStep).clamp(0, _selIndex);
            _scrollToSel();
          } else {
            // The tournament board has no chips zone to land on.
            _zone = _scope == 2 ? 0 : 1;
          }
        } else if (_zone == 1) {
          _zone = 0;
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowDown) {
      setState(() {
        if (_zone == 0) {
          // The tournament board has no filter chips: DOWN
          // from the top bar goes straight to its rows.
          if (_scope == 2) {
            if (_rowCount > 0) {
              _zone = 2;
              _selIndex = 0;
              _scrollToSel();
            }
          } else {
            _zone = 1;
          }
        } else if (_zone == 1) {
          if (_rowCount > 0) {
            _zone = 2;
            _selIndex = 0;
            _scrollToSel();
          }
        } else if (_selIndex < _rowCount - 1) {
          _selIndex = (_selIndex + _rowStep).clamp(0, _rowCount - 1);
          _scrollToSel();
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.arrowLeft ||
        key == LogicalKeyboardKey.arrowRight) {
      final dir = key == LogicalKeyboardKey.arrowLeft ? -1 : 1;
      setState(() {
        if (_zone == 0) {
          _barIndex = (_barIndex + dir).clamp(0, 3);
        } else if (_zone == 1) {
          _mode = (_mode + dir + _modes.length) % _modes.length;
          _selIndex = 0;
          _scrollTop();
        } else if (_rowCount > 0) {
          // In the rows, LEFT/RIGHT jump five rows at a time.
          _selIndex = (_selIndex + dir * 5).clamp(0, _rowCount - 1);
          _scrollToSel();
        }
      });
      return KeyEventResult.handled;
    }
    if (key == LogicalKeyboardKey.enter ||
        key == LogicalKeyboardKey.select ||
        key == LogicalKeyboardKey.space ||
        key == LogicalKeyboardKey.gameButtonA) {
      if (_zone == 0) {
        if (_barIndex == 0) {
          Navigator.of(context).pop();
        } else {
          _setScope(_barIndex - 1);
        }
      } else if (_zone == 2 && event is KeyDownEvent) {
        // OK on a Players row adds that player as a friend of the
        // current player, or removes them if they already are one.
        // Only on the fresh press: a held OK must not toggle twice.
        // Your own row does nothing (checked in _toggleFriend).
        final name = _selectedPlayerName();
        if (name != null) _toggleFriend(name);
      }
      return KeyEventResult.handled;
    }
    // Handled or not, arrows never fall through to focus traversal:
    // the AppBar back button would take focus on UP and strand input.
    return KeyEventResult.ignored;
  }

  Future<dynamic> _getJson(String path) async {
    final client = HttpClient();
    client.connectionTimeout = const Duration(seconds: 6);
    try {
      final req = await client.getUrl(Uri.parse('$kServerBase$path'));
      final resp = await req.close().timeout(const Duration(seconds: 6));
      final text = await resp.transform(utf8.decoder).join();
      if (resp.statusCode >= 400) throw Exception('HTTP ${resp.statusCode}');
      return jsonDecode(text);
    } finally {
      client.close();
    }
  }

  Future<dynamic> _postJson(String path, Map<String, dynamic> body) async {
    final client = HttpClient();
    client.connectionTimeout = const Duration(seconds: 6);
    try {
      final req = await client.postUrl(Uri.parse('$kServerBase$path'));
      req.headers.contentType = ContentType.json;
      req.write(jsonEncode(body));
      final resp = await req.close().timeout(const Duration(seconds: 6));
      final text = await resp.transform(utf8.decoder).join();
      if (resp.statusCode >= 400) throw Exception('HTTP ${resp.statusCode}');
      return text.isEmpty ? null : jsonDecode(text);
    } finally {
      client.close();
    }
  }

  // The current player, resolved the same way the rest of the app
  // resolves it: the current_player pref.
  Future<String> _currentPlayerName() async {
    final p = await Prefs.get('current_player');
    return (p == null || p.isEmpty) ? 'Player 1' : p;
  }

  void _applyFriends(dynamic data) {
    final list = (data is Map ? data['friends'] : null) as List?;
    if (list == null) return;
    final names = <String>{};
    for (final e in list) {
      if (e is Map) {
        final n = (e['name'] ?? '').toString();
        if (n.isNotEmpty) names.add(n);
      }
    }
    _friendNames = names;
  }

  // The friend set comes from the server, never from local play
  // stats: it belongs to the account name, not to this box.
  Future<void> _loadFriends() async {
    try {
      final me = await _currentPlayerName();
      _myName = me;
      final data =
          await _getJson('/friends?player=${Uri.encodeQueryComponent(me)}');
      if (!mounted) return;
      setState(() => _applyFriends(data));
    } catch (_) {
      // Friends are a bonus: a failed load just shows no chips.
    }
  }

  Future<void> _toggleFriend(String name) async {
    if (name.isEmpty) return;
    if (_myName.isEmpty) _myName = await _currentPlayerName();
    if (name == _myName) return;
    try {
      final path =
          _friendNames.contains(name) ? '/friends/remove' : '/friends';
      await _postJson(path, {'player': _myName, 'friend': name});
      // Re-read the set so the chips always match the server copy.
      await _loadFriends();
    } catch (_) {
      // Leave the chips as they are on any failure.
    }
  }

  // The player name behind the selected row, Players mode only.
  // Rows in the other modes are games or activity, not people.
  String? _selectedPlayerName() {
    // Tournament rows are match records, not friend toggles.
    if (_scope == 2) return null;
    if (_mode != 0) return null;
    final list = _scope == 1 ? _gPlayers : _players;
    if (_selIndex < 0 || _selIndex >= list.length) return null;
    return list[_selIndex].name;
  }

  Future<void> _fetchGlobal() async {
    try {
      final players = await _getJson('/leaderboard/players') as List;
      final games = await _getJson('/leaderboard/games') as List;
      final recent = await _getJson('/leaderboard/recent') as List;
      if (!mounted) return;
      setState(() {
        _gPlayers = players.map((e) {
          final m = e as Map<String, dynamic>;
          return _PlayerStat(
            m['player']?.toString() ?? 'Player',
            const [],
            gamesCountOverride: (m['games'] as num? ?? 0).toInt(),
            secondsOverride: (m['seconds'] as num? ?? 0).toInt(),
            playsOverride: (m['plays'] as num? ?? 0).toInt(),
            display: m['display']?.toString() ?? '',
            region: m['region']?.toString() ?? '',
          );
        }).toList();
        _gGames = games.map((e) {
          final m = e as Map<String, dynamic>;
          return _GameStat(
            m['game']?.toString() ?? '',
            (m['plays'] as num? ?? 0).toInt(),
            (m['seconds'] as num? ?? 0).toInt(),
            0,
          );
        }).toList();
        _gRecent = recent.map((e) {
          final m = e as Map<String, dynamic>;
          return (
            player: m['player']?.toString() ?? 'Player',
            display: m['display']?.toString() ?? '',
            region: m['region']?.toString() ?? '',
            game: _GameStat(
              m['game']?.toString() ?? '',
              (m['plays'] as num? ?? 0).toInt(),
              (m['seconds'] as num? ?? 0).toInt(),
              (m['lastPlayed'] as num? ?? 0).toInt(),
            ),
          );
        }).toList();
        _gLoaded = true;
        _gFailed = false;
      });
    } catch (_) {
      if (!mounted) return;
      setState(() {
        _gLoaded = true;
        _gFailed = true;
      });
    }
  }

  // The tournament board (PROTOCOL.md 8): ranked records and
  // the recent results feed from the server's matches store,
  // a different store from the playtime boards above. Fetched
  // lazily on first entry, the same way the global board is.
  Future<void> _fetchTournament() async {
    try {
      final board =
          await _getJson('/matches/leaderboard') as Map<String, dynamic>;
      final feed =
          await _getJson('/matches/recent') as Map<String, dynamic>;
      if (!mounted) return;
      setState(() {
        _tPlayers = ((board['players'] as List?) ?? []).map((e) {
          final m = e as Map<String, dynamic>;
          return _TPlayer(
            name: m['name']?.toString() ?? 'Player',
            display: m['display']?.toString() ?? '',
            region: m['region']?.toString() ?? '',
            wins: (m['wins'] as num? ?? 0).toInt(),
            losses: (m['losses'] as num? ?? 0).toInt(),
            matches: (m['matches'] as num? ?? 0).toInt(),
            tournamentsWon: (m['tournamentsWon'] as num? ?? 0).toInt(),
            lastTournament: m['lastTournament']?.toString() ?? '',
            lastGame: m['lastGame']?.toString() ?? '',
            lastTitle: m['lastTitle']?.toString() ?? '',
          );
        }).toList();
        _tRecent = ((feed['recent'] as List?) ?? []).map((e) {
          final m = e as Map<String, dynamic>;
          return _TResult(
            tournament: m['tournament']?.toString() ?? '',
            game: m['game']?.toString() ?? '',
            winner: m['winner']?.toString() ?? 'Player',
            loser: m['loser']?.toString() ?? 'Player',
            at: (m['at'] as num? ?? 0).toInt(),
          );
        }).toList();
        _tLoaded = true;
        _tFailed = false;
      });
    } catch (_) {
      if (!mounted) return;
      setState(() {
        _tLoaded = true;
        _tFailed = true;
      });
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

  // Display identity for a row. Global rows use the identity the
  // player's own box reported; local rows resolve through this box's
  // player info. The two never mix: a global "David" from another
  // house must not inherit this box's David's initials.
  String _dispName(_PlayerStat p, bool global) {
    if (global) return p.display.isNotEmpty ? p.display : p.name;
    return PlayerInfoStore.displayOf(p.name);
  }

  String _regionOf(_PlayerStat p, bool global) {
    if (global) return p.region;
    return PlayerInfoStore.of(p.name)?.region ?? '';
  }

  Widget _regionTag(String region) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: Colors.white10,
        borderRadius: BorderRadius.circular(999),
        border: Border.all(color: Colors.white24),
      ),
      child: Text(
        region,
        style: const TextStyle(fontSize: 12, color: Colors.white70),
      ),
    );
  }

  // Trailing marker on a Players row whose name is in the current
  // player's friend set. Styled like the lobby list's Open chip.
  Widget _friendChip() {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 3),
      decoration: BoxDecoration(
        color: const Color(0xFF00E676).withOpacity(0.14),
        borderRadius: BorderRadius.circular(999),
        border: Border.all(
          color: const Color(0xFF00E676).withOpacity(0.55),
        ),
      ),
      child: const Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(Icons.person, size: 13, color: Color(0xFF00E676)),
          SizedBox(width: 4),
          Text(
            'Friend',
            style: TextStyle(
              fontSize: 12.5,
              fontWeight: FontWeight.bold,
              color: Color(0xFF00E676),
            ),
          ),
        ],
      ),
    );
  }

  Widget _playerCard(int rank, _PlayerStat p, {bool global = false}) {
    final fav = p.favorite;
    final region = _regionOf(p, global);
    final medalColor = rank == 1
        ? const Color(0xFFFFD54F)
        : rank == 2
            ? const Color(0xFFB0BEC5)
            : rank == 3
                ? const Color(0xFFBCAAA4)
                : const Color(0xFF424242);
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 4),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 9),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 17,
            backgroundColor: medalColor,
            child: Text(
              '$rank',
              style: const TextStyle(
                fontSize: 15,
                fontWeight: FontWeight.bold,
                color: Colors.black87,
              ),
            ),
          ),
          const SizedBox(width: 12),
          PlayerAvatar(name: p.name, size: 32),
          const SizedBox(width: 11),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Flexible(
                      child: Text(
                        _dispName(p, global),
                        style: const TextStyle(
                            fontSize: 18, fontWeight: FontWeight.bold),
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    if (region.isNotEmpty) ...[
                      const SizedBox(width: 9),
                      _regionTag(region),
                    ],
                  ],
                ),
                Text(
                  'Time played: ${_fmtDuration(p.totalSeconds)} · '
                  'Games: ${p.gamesCount} · Launches: ${p.totalPlays}',
                  style:
                      const TextStyle(fontSize: 13.5, color: Colors.white70),
                ),
                if (_mode == 0 && fav != null)
                  Text(
                    'Favorite: ${fav.name} (${_fmtDuration(fav.seconds)})',
                    style:
                        const TextStyle(fontSize: 13, color: Colors.white54),
                  ),
              ],
            ),
          ),
          if (_friendNames.contains(p.name)) ...[
            const SizedBox(width: 10),
            _friendChip(),
          ],
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final rows = _loading ? const <Widget>[] : _visibleContent();
    _rowCount = rows.length;
    if (_selIndex >= _rowCount) {
      _selIndex = _rowCount == 0 ? 0 : _rowCount - 1;
    }
    return Scaffold(
      appBar: AppBar(
        title: const Text('Leaderboard'),
        backgroundColor: Colors.transparent,
        elevation: 0,
        // The board switch lives in the top bar now, beside
        // the back button (David, 2026-10-12). UP from anywhere
        // below reaches this row; OK picks Back or a board:
        // Local, Global, or Tournament.
        // A custom leading button (instead of the automatic one) so
        // remote focus on Back is visible.
        leading: Center(
          child: GestureDetector(
            onTap: () => Navigator.of(context).pop(),
            child: Container(
              width: 42,
              height: 42,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: const Color(0xFF1E1E2E),
                border: Border.all(
                  color: _zone == 0 && _barIndex == 0
                      ? Colors.white
                      : Colors.white24,
                  width: _zone == 0 && _barIndex == 0 ? 2.5 : 1,
                ),
              ),
              child: const Icon(Icons.arrow_back, size: 21),
            ),
          ),
        ),
        actions: [
          _scopePill('Local', 0),
          const SizedBox(width: 8),
          _scopePill('Global', 1),
          const SizedBox(width: 8),
          _scopePill('Tournament', 2),
          const SizedBox(width: 14),
        ],
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
            : Focus(
                focusNode: _focusNode,
                onKeyEvent: _onKey,
                child: Column(
                  children: [
                    const SizedBox(height: 10),
                    // The mode chips sort the playtime boards.
                    // The tournament board keeps the server's
                    // own order (titles, then wins), so the
                    // chips hide while it is up.
                    if (_scope != 2)
                      Wrap(
                        spacing: 10,
                        alignment: WrapAlignment.center,
                        children: [
                          for (var i = 0; i < _modes.length; i++)
                          GestureDetector(
                            onTap: () => _setMode(i),
                            child: Container(
                              padding: const EdgeInsets.symmetric(
                                  horizontal: 18, vertical: 8),
                              decoration: BoxDecoration(
                                color: _mode == i
                                    ? const Color(0xFF00E676)
                                    : const Color(0xFF1E1E2E),
                                borderRadius: BorderRadius.circular(999),
                                border: Border.all(
                                  color: _zone == 1 && _mode == i
                                      ? Colors.white
                                      : (_mode == i
                                          ? Colors.white54
                                          : Colors.white24),
                                  width: _zone == 1 && _mode == i ? 2.5 : 1,
                                ),
                              ),
                              child: Text(
                                _modes[i],
                                style: TextStyle(
                                  fontSize: 16.5,
                                  fontWeight: FontWeight.bold,
                                  color:
                                      _mode == i ? Colors.black : Colors.white,
                                ),
                              ),
                            ),
                          ),
                      ],
                    ),
                    Expanded(child: _contentArea(rows)),
                    const Padding(
                      padding: EdgeInsets.only(bottom: 10),
                      child: Text(
                        'UP for the top bar · DOWN to browse the board · '
                        'LEFT / RIGHT switch boards · BACK returns',
                        style: TextStyle(fontSize: 13.5, color: Colors.white38),
                      ),
                    ),
                  ],
                ),
              ),
      ),
    );
  }

  Widget _scopePill(String label, int scope) {
    final active = _scope == scope;
    final focused = _zone == 0 && _barIndex == scope + 1;
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = 0;
          _barIndex = scope + 1;
        });
        _setScope(scope);
      },
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 7),
        decoration: BoxDecoration(
          // Rectangular segmented look in the app's card language, no
          // green fill (David, 2026-10-12): the current board is the
          // lighter card with a plain border, remote focus thickens
          // the border to white.
          color: active ? const Color(0xFF2E2E4A) : const Color(0xFF1E1E2E),
          borderRadius: BorderRadius.circular(6),
          border: Border.all(
            color: focused
                ? Colors.white
                : (active ? Colors.white54 : Colors.white24),
            width: focused ? 2.5 : 1,
          ),
        ),
        child: Text(
          label,
          style: TextStyle(
            fontSize: 16.5,
            fontWeight: FontWeight.bold,
            color: active ? Colors.white : Colors.white60,
          ),
        ),
      ),
    );
  }

  String _emptyText() {
    if (_scope == 2) {
      if (!_tLoaded) return 'Loading the tournament board...';
      if (_tFailed) {
        return 'Cannot reach the leaderboard server.\n'
            'Tournament results are kept there.';
      }
      return 'No tournament matches yet.\n'
          'Host a room with a tournament name to start one.';
    }
    if (_scope == 1) {
      if (!_gLoaded) return 'Loading the global board...';
      if (_gFailed) {
        return 'Cannot reach the leaderboard server.\n'
            'Local scores are under the Local switch.';
      }
      return 'No global play time yet.\n'
          'Finished games send their totals here.';
    }
    return 'No play time logged yet.\n'
        'Stats start counting as soon as someone plays.';
  }

  Widget _contentArea(List<Widget> rows) {
    if (rows.isEmpty) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Text(
            _emptyText(),
            textAlign: TextAlign.center,
            style: const TextStyle(fontSize: 20, color: Colors.grey),
          ),
        ),
      );
    }
    return Center(
      child: SizedBox(
        width: 1060,
        child: ListView(
          controller: _listScroll,
          padding: const EdgeInsets.symmetric(vertical: 12),
          children: [
            for (var i = 0; i < rows.length; i++) _selectable(i, rows[i]),
          ],
        ),
      ),
    );
  }

  // Wraps one board row with its remote-selection ring and the key
  // the scroller uses to keep the selected row on screen.
  Widget _selectable(int i, Widget child) {
    final sel = _zone == 2 && _selIndex == i;
    return Container(
      key: _rowKeys.putIfAbsent(i, () => GlobalKey()),
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(15),
        border: Border.all(
          color: sel ? Colors.white : Colors.transparent,
          width: 2.5,
        ),
      ),
      child: child,
    );
  }

  List<Widget> _visibleContent() {
    if (_scope == 0) return _modeContent();
    if (_scope == 2) return _tournamentContent();
    switch (_mode) {
      case 1: // Top Games, by time
        final games = [..._gGames]
          ..sort((a, b) => b.seconds.compareTo(a.seconds));
        final top = games.take(10).toList();
        return [
          for (var i = 0; i < top.length; i++)
            _gameRow(i + 1, top[i],
                '${_fmtDuration(top[i].seconds)} · ${top[i].plays} plays'),
        ];
      case 2: // Most Played, by launches
        final games = [..._gGames]
          ..sort((a, b) => b.plays.compareTo(a.plays));
        final top = games.take(10).toList();
        return [
          for (var i = 0; i < top.length; i++)
            _gameRow(i + 1, top[i],
                '${top[i].plays} plays · ${_fmtDuration(top[i].seconds)}'),
        ];
      case 3: // Last Played across everyone
        return [
          for (final e in _gRecent)
            Container(
              margin: const EdgeInsets.symmetric(vertical: 4),
              padding:
                  const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
              decoration: BoxDecoration(
                color: const Color(0xFF1B1B28),
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.white12),
              ),
              child: Row(
                children: [
                  PlayerAvatar(name: e.player, size: 30),
                  const SizedBox(width: 11),
                  Text(
                    e.display.isNotEmpty ? e.display : e.player,
                    style: const TextStyle(
                        fontSize: 16.5, fontWeight: FontWeight.bold),
                  ),
                  if (e.region.isNotEmpty) ...[
                    const SizedBox(width: 8),
                    _regionTag(e.region),
                  ],
                  const SizedBox(width: 13),
                  Expanded(
                    child: Text(
                      e.game.name,
                      style: const TextStyle(fontSize: 15.5),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  Text(
                    _fmtWhen(e.game.last),
                    style:
                        const TextStyle(fontSize: 14, color: Colors.white60),
                  ),
                ],
              ),
            ),
        ];
      default: // Players
        return [
          for (var i = 0; i < _gPlayers.length; i++)
            _playerCard(i + 1, _gPlayers[i], global: true),
        ];
    }
  }

  // The tournament board: the ranked rows in the server's
  // order, then the recent results under a header. One list,
  // so the remote browses both the way it browses the other
  // boards.
  List<Widget> _tournamentContent() {
    final rows = <Widget>[
      for (var i = 0; i < _tPlayers.length; i++)
        _tournamentRow(i + 1, _tPlayers[i]),
    ];
    if (_tRecent.isNotEmpty) {
      rows.add(const Padding(
        padding: EdgeInsets.only(top: 18, bottom: 6, left: 4),
        child: Text(
          'Recent results',
          style: TextStyle(fontSize: 20, fontWeight: FontWeight.bold),
        ),
      ));
      for (final r in _tRecent) {
        rows.add(_tournamentRecentRow(r));
      }
    }
    return rows;
  }

  // One tournament row, in _playerCard's shape: rank medal,
  // avatar, identity with its region tag, the record under it.
  Widget _tournamentRow(int rank, _TPlayer p) {
    final medalColor = rank == 1
        ? const Color(0xFFFFD54F)
        : rank == 2
            ? const Color(0xFFB0BEC5)
            : rank == 3
                ? const Color(0xFFBCAAA4)
                : const Color(0xFF424242);
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 4),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 9),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 17,
            backgroundColor: medalColor,
            child: Text(
              '$rank',
              style: const TextStyle(
                fontSize: 15,
                fontWeight: FontWeight.bold,
                color: Colors.black87,
              ),
            ),
          ),
          const SizedBox(width: 12),
          PlayerAvatar(name: p.name, size: 32),
          const SizedBox(width: 11),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Flexible(
                      child: Text(
                        p.display.isNotEmpty ? p.display : p.name,
                        style: const TextStyle(
                            fontSize: 18, fontWeight: FontWeight.bold),
                        overflow: TextOverflow.ellipsis,
                      ),
                    ),
                    if (p.region.isNotEmpty) ...[
                      const SizedBox(width: 9),
                      _regionTag(p.region),
                    ],
                  ],
                ),
                Text(
                  'Record: ${p.wins}-${p.losses} · Matches: ${p.matches} · '
                  'Titles: ${p.tournamentsWon}',
                  style:
                      const TextStyle(fontSize: 13.5, color: Colors.white70),
                ),
                if (p.lastTitle.isNotEmpty)
                  Text(
                    'Latest title: ${p.lastTitle}',
                    style:
                        const TextStyle(fontSize: 13, color: Colors.white54),
                  )
                else if (p.lastTournament.isNotEmpty)
                  Text(
                    'Last event: ${p.lastTournament}',
                    style:
                        const TextStyle(fontSize: 13, color: Colors.white54),
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  // One recent result: who beat who, in which event, at what.
  Widget _tournamentRecentRow(_TResult r) {
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 4),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          PlayerAvatar(name: r.winner, size: 30),
          const SizedBox(width: 11),
          Text(
            '${r.winner} def. ${r.loser}',
            style: const TextStyle(
                fontSize: 16.5, fontWeight: FontWeight.bold),
          ),
          const SizedBox(width: 13),
          Expanded(
            child: Text(
              '${r.tournament} · ${_GameStat(r.game, 0, 0, 0).name}',
              style: const TextStyle(fontSize: 15.5),
              overflow: TextOverflow.ellipsis,
            ),
          ),
          Text(
            _fmtWhen(r.at),
            style: const TextStyle(fontSize: 14, color: Colors.white60),
          ),
        ],
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
              margin: const EdgeInsets.symmetric(vertical: 4),
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
              decoration: BoxDecoration(
                color: const Color(0xFF1B1B28),
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.white12),
              ),
              child: Row(
                children: [
                  PlayerAvatar(name: e.$1.name, size: 30),
                  const SizedBox(width: 11),
                  Text(
                    PlayerInfoStore.displayOf(e.$1.name),
                    style: const TextStyle(
                        fontSize: 16.5, fontWeight: FontWeight.bold),
                  ),
                  if ((PlayerInfoStore.of(e.$1.name)?.region ?? '')
                      .isNotEmpty) ...[
                    const SizedBox(width: 8),
                    _regionTag(PlayerInfoStore.of(e.$1.name)!.region),
                  ],
                  const SizedBox(width: 13),
                  Expanded(
                    child: Text(
                      e.$2.name,
                      style: const TextStyle(fontSize: 15.5),
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  Text(
                    _fmtWhen(e.$2.last),
                    style:
                        const TextStyle(fontSize: 14, color: Colors.white60),
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
      margin: const EdgeInsets.symmetric(vertical: 4),
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
      decoration: BoxDecoration(
        color: const Color(0xFF1B1B28),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white12),
      ),
      child: Row(
        children: [
          CircleAvatar(
            radius: 14,
            backgroundColor: rank == 1
                ? const Color(0xFFFFD54F)
                : const Color(0xFF37474F),
            child: Text(
              '$rank',
              style: TextStyle(
                fontSize: 13,
                fontWeight: FontWeight.bold,
                color: rank == 1 ? Colors.black87 : Colors.white,
              ),
            ),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Text(
              g.name,
              style: const TextStyle(fontSize: 16.5),
              overflow: TextOverflow.ellipsis,
            ),
          ),
          Text(
            right,
            style: const TextStyle(fontSize: 15, color: Colors.white70),
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

/// Friend alert banner: slides down from the top of the screen,
/// holds, and slides away on its own (the shell removes the
/// overlay entry at 4 s; the fade out starts a little before so
/// nothing pops). IgnorePointer wraps the whole thing: the banner
/// can never take the remote's focus or eat a key press.
class _FriendAlertBanner extends StatefulWidget {
  final String avatarName;
  final String message;
  const _FriendAlertBanner({required this.avatarName, required this.message});

  @override
  State<_FriendAlertBanner> createState() => _FriendAlertBannerState();
}

class _FriendAlertBannerState extends State<_FriendAlertBanner>
    with SingleTickerProviderStateMixin {
  late final AnimationController _ctrl;
  Timer? _outTimer;

  @override
  void initState() {
    super.initState();
    _ctrl = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 320),
    );
    _ctrl.forward();
    _outTimer = Timer(const Duration(milliseconds: 3650), () {
      if (mounted) _ctrl.reverse();
    });
  }

  @override
  void dispose() {
    _outTimer?.cancel();
    _ctrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final curve = CurvedAnimation(parent: _ctrl, curve: Curves.easeOut);
    return Positioned(
      top: 18,
      left: 0,
      right: 0,
      child: IgnorePointer(
        child: FadeTransition(
          opacity: curve,
          child: SlideTransition(
            position: Tween<Offset>(
              begin: const Offset(0, -1.4),
              end: Offset.zero,
            ).animate(curve),
            child: Center(
              child: Material(
                color: Colors.transparent,
                child: Container(
                  constraints: const BoxConstraints(maxWidth: 680),
                  padding: const EdgeInsets.symmetric(
                      horizontal: 18, vertical: 12),
                  decoration: BoxDecoration(
                    color: const Color(0xFF23233A),
                    borderRadius: BorderRadius.circular(14),
                    border: Border.all(
                      color: const Color(0xFF00E676).withOpacity(0.55),
                    ),
                    boxShadow: [
                      BoxShadow(
                        color: Colors.black.withOpacity(0.45),
                        blurRadius: 18,
                        offset: const Offset(0, 6),
                      ),
                    ],
                  ),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      PlayerAvatar(name: widget.avatarName, size: 42),
                      const SizedBox(width: 14),
                      Flexible(
                        child: Text(
                          widget.message,
                          style: const TextStyle(
                            fontSize: 19,
                            fontWeight: FontWeight.w600,
                            color: Colors.white,
                          ),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
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
