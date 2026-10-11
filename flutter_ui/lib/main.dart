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
        scaffoldBackgroundColor: const Color(0xFF0A0A0A),
        primaryColor: const Color(0xFF00E676),
      ),
      home: const SystemSelectScreen(),
    );
  }
}

/// Tiny key/value store backed by Android SharedPreferences (itak_prefs),
/// shared with the emulator screen so it knows who is playing.
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
    } catch (_) {
      // Non-fatal: worst case the pick is not remembered.
    }
  }
}

// Systems - filtered by build (32-bit vs 64-bit done at compile time)
const List<Map<String, String>> kSystems32Bit = [
  {'id': 'nes', 'name': 'Nintendo', 'color': '0xFFE53935'},
  {'id': 'snes', 'name': 'Super Nintendo', 'color': '0xFF7B1FA2'},
  {'id': 'genesis', 'name': 'Sega Genesis', 'color': '0xFF1565C0'},
  {'id': 'gameboy', 'name': 'Game Boy', 'color': '0xFF9E9E9E'},
  {'id': 'gba', 'name': 'Game Boy Advance', 'color': '0xFF5C6BC0'},
  {'id': 'ps1', 'name': 'PlayStation', 'color': '0xFF616161'},
];

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

class SystemSelectScreen extends StatefulWidget {
  const SystemSelectScreen({super.key});

  @override
  State<SystemSelectScreen> createState() => _SystemSelectScreenState();
}

class _SystemSelectScreenState extends State<SystemSelectScreen> {
  int _systemIndex = 1; // SNES default
  int _playerIndex = 0;
  int _zone = 1; // 0 = players row, 1 = systems row
  List<String> _players = [];
  String _currentPlayer = '';
  final FocusNode _focusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _loadPlayers();
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
    setState(() {
      _players = players;
      _currentPlayer = cur;
      _playerIndex = players.indexOf(cur);
    });
  }

  Future<void> _selectPlayer(int index) async {
    setState(() {
      _playerIndex = index;
      _currentPlayer = _players[index];
    });
    await Prefs.set('current_player', _currentPlayer);
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
    if (key == LogicalKeyboardKey.arrowUp ||
        key == LogicalKeyboardKey.arrowDown) {
      setState(() => _zone = _zone == 0 ? 1 : 0);
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      setState(() {
        if (_zone == 0) {
          // Players row has one extra slot: the add tile.
          final count = _players.length + 1;
          _playerIndex = (_playerIndex - 1 + count) % count;
        } else {
          _systemIndex =
              (_systemIndex - 1 + kSystems32Bit.length) % kSystems32Bit.length;
        }
      });
    } else if (key == LogicalKeyboardKey.arrowRight) {
      setState(() {
        if (_zone == 0) {
          final count = _players.length + 1;
          _playerIndex = (_playerIndex + 1) % count;
        } else {
          _systemIndex = (_systemIndex + 1) % kSystems32Bit.length;
        }
      });
    } else if (_isSelect(event)) {
      if (_zone == 0) {
        if (_playerIndex >= _players.length) {
          _addPlayer();
        } else {
          _selectPlayer(_playerIndex);
        }
      } else {
        _openGames(kSystems32Bit[_systemIndex]);
      }
    }
  }

  void _openGames(Map<String, String> system) {
    Navigator.push(
      context,
      MaterialPageRoute(
        builder: (_) => GameGridScreen(system: system),
      ),
    ).then((_) => _focusNode.requestFocus());
  }

  Widget _playerChip(int index) {
    final isAdd = index >= _players.length;
    final focused = _zone == 0 && _playerIndex == index;
    final isCurrent = !isAdd && _players[index] == _currentPlayer;
    final color = isAdd
        ? const Color(0xFF424242)
        : kPlayerColors[index % kPlayerColors.length];
    return GestureDetector(
      onTap: () {
        setState(() {
          _zone = 0;
          _playerIndex = index;
        });
        if (isAdd) {
          _addPlayer();
        } else {
          _selectPlayer(index);
        }
      },
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 150),
        margin: const EdgeInsets.symmetric(horizontal: 10),
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
        decoration: BoxDecoration(
          color: const Color(0xFF1A1A1A),
          borderRadius: BorderRadius.circular(999),
          border: Border.all(
            color: focused
                ? Colors.white
                : isCurrent
                    ? const Color(0xFF00E676)
                    : Colors.transparent,
            width: focused ? 3 : 2,
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            CircleAvatar(
              radius: 20,
              backgroundColor: color,
              child: Text(
                isAdd ? '+' : _players[index][0].toUpperCase(),
                style: const TextStyle(
                  fontSize: 20,
                  fontWeight: FontWeight.bold,
                  color: Colors.white,
                ),
              ),
            ),
            const SizedBox(width: 10),
            Text(
              isAdd ? 'Add player' : _players[index],
              style: TextStyle(
                fontSize: 20,
                fontWeight: isCurrent ? FontWeight.bold : FontWeight.normal,
                color: Colors.white,
              ),
            ),
            if (isCurrent) ...[
              const SizedBox(width: 8),
              const Icon(Icons.check_circle,
                  size: 20, color: Color(0xFF00E676)),
            ],
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
        child: Column(
          children: [
            const SizedBox(height: 44),
            const Text(
              'iTaK Gaming',
              style: TextStyle(fontSize: 56, fontWeight: FontWeight.bold),
            ),
            const SizedBox(height: 24),
            const Text(
              "Who's playing?",
              style: TextStyle(fontSize: 22, color: Colors.grey),
            ),
            const SizedBox(height: 12),
            SizedBox(
              height: 64,
              child: Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: List.generate(
                  _players.length + 1,
                  (index) => _playerChip(index),
                ),
              ),
            ),
            const SizedBox(height: 8),
            Expanded(
              child: Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: List.generate(kSystems32Bit.length, (index) {
                  final sys = kSystems32Bit[index];
                  final isSelected = _zone == 1 && index == _systemIndex;
                  final color = Color(int.parse(sys['color']!));
                  return GestureDetector(
                    onTap: () {
                      setState(() {
                        _zone = 1;
                        _systemIndex = index;
                      });
                      _openGames(sys);
                    },
                    child: AnimatedContainer(
                      duration: const Duration(milliseconds: 200),
                      width: isSelected ? 220 : 180,
                      height: isSelected ? 220 : 180,
                      margin: const EdgeInsets.symmetric(horizontal: 12),
                      decoration: BoxDecoration(
                        color: color,
                        borderRadius: BorderRadius.circular(16),
                        border: isSelected
                            ? Border.all(color: Colors.white, width: 4)
                            : null,
                        boxShadow: isSelected
                            ? [
                                BoxShadow(
                                    color: color.withOpacity(0.5),
                                    blurRadius: 20)
                              ]
                            : null,
                      ),
                      child: Center(
                        child: Text(
                          sys['name']!,
                          textAlign: TextAlign.center,
                          style: TextStyle(
                            fontSize: isSelected ? 24 : 20,
                            fontWeight: FontWeight.bold,
                            color: Colors.white,
                          ),
                        ),
                      ),
                    ),
                  );
                }),
              ),
            ),
            const SizedBox(height: 24),
            const Text(
              'UP / DOWN switch rows, LEFT / RIGHT browse, OK to select',
              style: TextStyle(fontSize: 18, color: Colors.grey),
            ),
            const SizedBox(height: 32),
          ],
        ),
      ),
    );
  }
}

// Core library asset per system id, and ROM folder + extensions on device.
const Map<String, Map<String, Object>> kSystemConfig = {
  'nes': {
    'core': 'libfceumm.so',
    'dir': 'NES',
    'exts': ['.nes'],
  },
  'snes': {
    'core': 'libsnes9x.so',
    'dir': 'SNES',
    'exts': ['.sfc', '.smc'],
  },
  'genesis': {
    'core': 'libgenesis_plus_gx.so',
    'dir': 'Genesis',
    'exts': ['.md', '.gen', '.bin'],
  },
  'gameboy': {
    'core': 'libgambatte.so',
    'dir': 'GameBoy',
    'exts': ['.gb', '.gbc'],
  },
  'gba': {
    'core': 'libmgba.so',
    'dir': 'GBA',
    'exts': ['.gba'],
  },
  'ps1': {
    'core': 'libpcsx_rearmed.so',
    'dir': 'PS1',
    'exts': ['.cue', '.iso'],
  },
};

class GameGridScreen extends StatefulWidget {
  final Map<String, String> system;

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

  @override
  void initState() {
    super.initState();
    _scan();
  }

  Future<void> _scan() async {
    final cfg = kSystemConfig[widget.system['id']];
    if (cfg == null) {
      setState(() {
        _loading = false;
        _status = 'System not configured yet';
      });
      return;
    }
    final dir = Directory('/sdcard/ROMs/${cfg['dir']}');
    final exts = (cfg['exts'] as List).cast<String>();
    final found = <File>[];
    try {
      if (await dir.exists()) {
        await for (final e in dir.list()) {
          if (e is File) {
            final lower = e.path.toLowerCase();
            if (exts.any((x) => lower.endsWith(x))) found.add(e);
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
    // Box art lives beside the ROMs: ROMs/<System>/boxart/<game name>.png
    for (final f in found) {
      final art = File('${dir.path}/boxart/${_displayName(f)}.png');
      _art[f.path] = art.existsSync() ? art : null;
    }
    setState(() {
      _games = found;
      _loading = false;
      _status = found.isEmpty ? 'No games found in ${dir.path}' : '';
    });
  }

  String _displayName(File f) {
    var name = f.uri.pathSegments.last;
    final dot = name.lastIndexOf('.');
    if (dot > 0) name = name.substring(0, dot);
    return name;
  }

  Future<void> _launch(File f) async {
    final cfg = kSystemConfig[widget.system['id']]!;
    await _channel.invokeMethod('launch', {
      'core': cfg['core'],
      'rom': f.path,
    });
  }

  Widget _tile(int index, Color systemColor) {
    final game = _games[index];
    final art = _art[game.path];
    final focused = _focused == index;
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
              color: const Color(0xFF1E1E1E),
              borderRadius: BorderRadius.circular(12),
              border: Border.all(
                color: focused ? Colors.white : Colors.transparent,
                width: 3,
              ),
              boxShadow: focused
                  ? [
                      BoxShadow(
                          color: systemColor.withOpacity(0.45), blurRadius: 18)
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
                          fit: BoxFit.cover,
                          errorBuilder: (_, __, ___) =>
                              _artFallback(systemColor),
                        )
                      : _artFallback(systemColor),
                ),
                Container(
                  color: const Color(0xFF141414),
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
                  child: Text(
                    _displayName(game),
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    textAlign: TextAlign.center,
                    style: TextStyle(
                      fontSize: 16,
                      fontWeight:
                          focused ? FontWeight.bold : FontWeight.normal,
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

  Widget _artFallback(Color systemColor) {
    return Container(
      color: systemColor.withOpacity(0.25),
      child: Icon(
        Icons.sports_esports,
        size: 56,
        color: systemColor.withOpacity(0.8),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final systemColor = Color(int.parse(widget.system['color']!));
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.system['name']!),
        backgroundColor: Colors.transparent,
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _games.isEmpty
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(32),
                    child: Text(
                      _status,
                      textAlign: TextAlign.center,
                      style: const TextStyle(fontSize: 22, color: Colors.grey),
                    ),
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
                  itemCount: _games.length,
                  itemBuilder: (context, index) => _tile(index, systemColor),
                ),
    );
  }
}
