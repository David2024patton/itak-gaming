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

// Systems - filtered by build (32-bit vs 64-bit done at compile time)
const List<Map<String, String>> kSystems32Bit = [
  {'id': 'nes', 'name': 'Nintendo', 'color': '0xFFE53935'},
  {'id': 'snes', 'name': 'Super Nintendo', 'color': '0xFF7B1FA2'},
  {'id': 'genesis', 'name': 'Sega Genesis', 'color': '0xFF1565C0'},
  {'id': 'gameboy', 'name': 'Game Boy', 'color': '0xFF9E9E9E'},
  {'id': 'gba', 'name': 'Game Boy Advance', 'color': '0xFF5C6BC0'},
  {'id': 'ps1', 'name': 'PlayStation', 'color': '0xFF616161'},
];

class SystemSelectScreen extends StatefulWidget {
  const SystemSelectScreen({super.key});

  @override
  State<SystemSelectScreen> createState() => _SystemSelectScreenState();
}

class _SystemSelectScreenState extends State<SystemSelectScreen> {
  int _selected = 1; // SNES default
  final FocusNode _focusNode = FocusNode();

  @override
  void initState() {
    super.initState();
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
    if (event is KeyDownEvent) {
      if (event.logicalKey == LogicalKeyboardKey.arrowLeft) {
        setState(() {
          _selected = (_selected - 1 + kSystems32Bit.length) % kSystems32Bit.length;
        });
      } else if (event.logicalKey == LogicalKeyboardKey.arrowRight) {
        setState(() {
          _selected = (_selected + 1) % kSystems32Bit.length;
        });
      } else if (event.logicalKey == LogicalKeyboardKey.enter ||
                 event.logicalKey == LogicalKeyboardKey.select) {
        _openGames(kSystems32Bit[_selected]);
      }
    }
  }

  void _openGames(Map<String, String> system) {
    Navigator.push(
      context,
      MaterialPageRoute(
        builder: (_) => GameGridScreen(system: system),
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
            const SizedBox(height: 60),
            const Text(
              'iTaK Gaming',
              style: TextStyle(fontSize: 64, fontWeight: FontWeight.bold),
            ),
            const SizedBox(height: 20),
            const Text(
              'Select a system',
              style: TextStyle(fontSize: 24, color: Colors.grey),
            ),
            const SizedBox(height: 40),
            Expanded(
              child: Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: List.generate(kSystems32Bit.length, (index) {
                  final sys = kSystems32Bit[index];
                  final isSelected = index == _selected;
                  final color = Color(int.parse(sys['color']!));
                  return GestureDetector(
                    onTap: () => _openGames(sys),
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
                            ? [BoxShadow(color: color.withOpacity(0.5), blurRadius: 20)]
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
            const SizedBox(height: 40),
            const Text(
              'Use LEFT / RIGHT to browse, OK to select',
              style: TextStyle(fontSize: 18, color: Colors.grey),
            ),
            const SizedBox(height: 40),
          ],
        ),
      ),
    );
  }
}

class GameGridScreen extends StatelessWidget {
  final Map<String, String> system;

  const GameGridScreen({super.key, required this.system});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: Text(system['name']!),
        backgroundColor: Colors.transparent,
      ),
      body: const Center(
        child: Text(
          'Games load from Go backend\n(TODO: connect API)',
          textAlign: TextAlign.center,
          style: TextStyle(fontSize: 24, color: Colors.grey),
        ),
      ),
    );
  }
}
