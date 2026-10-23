// Animated deep-space backdrop for the app shell.
//
// A starfield flies toward the viewer, like looking out a ship
// window. Flight speed breathes on its own in a loop: a slow
// drift where stars are points that twinkle and the galaxies
// show, an ease up to warp where the stars stretch into
// streaks, a hold at warp, an ease back down, then repeat.
// Phase lengths are randomized so the rhythm never feels
// mechanical.
//
// Galaxy sprites are generated once at startup from
// logarithmic spiral point clouds and cached as images; no
// asset files ship with this feature and nothing is
// downloaded. A faint nebula wash sits under everything so
// the scene reads as space at any speed.
//
// Cost discipline: the star list is preallocated and the
// painter allocates nothing per frame (nebula shaders are
// rebuilt only when the size changes). The ticker belongs to
// the widget, so it stops when the shell is covered by
// another route or removed from the tree, and games run in
// a separate Android activity where this widget never
// exists.

import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/scheduler.dart';

/// The flying-through-space background. Drop it into a Stack
/// as the bottom layer, under a translucent gradient and the
/// content.
class SpaceBackground extends StatefulWidget {
  const SpaceBackground({super.key});

  @override
  State<SpaceBackground> createState() => _SpaceBackgroundState();
}

enum _Phase { drift, accel, warp, decel }

class _Star {
  double x = 0; // view-plane offset at z = 1, about -0.95..0.95
  double y = 0;
  double z = 1; // depth: 1 is far, near 0 is at the viewer
  double twinklePhase = 0;
  double twinkleSpeed = 1;
  int colorIndex = 0;
}

class _Galaxy {
  ui.Image? image;
  double fx = 0; // center as a fraction of the canvas size
  double fy = 0;
  double vx = 0; // fraction of width per second
  double vy = 0;
  double size = 240; // sprite edge, pixels
  double rotation = 0;
  double spin = 0; // radians per second
}

class _Scene {
  final List<_Star> stars = [];
  final List<_Galaxy> galaxies = [];
  double speed = _SpaceBackgroundState._driftSpeed;
  double galaxyAlpha = 1;
  double time = 0;
  double width = 0;
  double height = 0;
  final List<ui.Shader?> nebulaShaders = [null, null, null];
  double nebulaW = -1;
  double nebulaH = -1;
}

class _SpaceBackgroundState extends State<SpaceBackground>
    with SingleTickerProviderStateMixin {
  static const double _driftSpeed = 0.055;
  static const double _warpSpeed = 1.6;
  static const int _starCount = 260;

  static const List<Color> _starColors = [
    Color(0xFFFFFFFF),
    Color(0xFFCFE0FF),
    Color(0xFFFFE3B3),
  ];

  // tint, sprite size, start position (fraction), drift
  // velocity (fraction of width per second), spin
  static const List<(Color, double, double, double, double, double)>
      _galaxySpecs = [
    (Color(0xFFBFD4FF), 300, 0.16, 0.24, 0.0035, 0.010),
    (Color(0xFFFFD9A0), 225, 0.83, 0.17, -0.0026, -0.014),
    (Color(0xFFD3B3FF), 260, 0.68, 0.80, -0.0030, 0.008),
    (Color(0xFFA8E6D8), 200, 0.27, 0.86, 0.0042, -0.011),
    (Color(0xFFBFD4FF), 165, 0.52, 0.44, 0.0022, 0.016),
  ];

  final _Scene _scene = _Scene();
  final math.Random _rnd = math.Random(20261022);
  final ValueNotifier<double> _frame = ValueNotifier<double>(0);
  late final Ticker _ticker;
  Duration? _lastTick;
  _Phase _phase = _Phase.drift;
  double _phaseDur = 7; // first drift is short: warp comes soon
  double _phaseLeft = 7;

  @override
  void initState() {
    super.initState();
    for (var i = 0; i < _starCount; i++) {
      final s = _Star();
      _respawn(s, randomDepth: true);
      _scene.stars.add(s);
    }
    for (var i = 0; i < _galaxySpecs.length; i++) {
      final spec = _galaxySpecs[i];
      final g = _Galaxy()
        ..size = spec.$2
        ..fx = spec.$3
        ..fy = spec.$4
        ..vx = spec.$5
        ..spin = spec.$6
        ..vy = spec.$5 * 0.35
        ..rotation = _rnd.nextDouble() * math.pi * 2;
      _scene.galaxies.add(g);
      _makeGalaxyImage(spec.$1, 9100 + i * 733).then((img) {
        if (!mounted) {
          img.dispose();
          return;
        }
        g.image = img; // the running ticker repaints within a frame
      });
    }
    _ticker = createTicker(_onTick)..start();
  }

  @override
  void dispose() {
    _ticker.dispose();
    _frame.dispose();
    for (final g in _scene.galaxies) {
      g.image?.dispose();
    }
    super.dispose();
  }

  void _respawn(_Star s, {bool randomDepth = false}) {
    s.x = (_rnd.nextDouble() * 2 - 1) * 0.95;
    s.y = (_rnd.nextDouble() * 2 - 1) * 0.95;
    s.z = randomDepth ? 0.08 + _rnd.nextDouble() * 0.92 : 1.0;
    s.twinklePhase = _rnd.nextDouble() * math.pi * 2;
    s.twinkleSpeed = 0.6 + _rnd.nextDouble() * 1.8;
    final roll = _rnd.nextDouble();
    s.colorIndex = roll < 0.6 ? 0 : (roll < 0.85 ? 1 : 2);
  }

  double _easeInOut(double u) =>
      u < 0.5 ? 4 * u * u * u : 1 - math.pow(-2 * u + 2, 3).toDouble() / 2;

  void _enterPhase(_Phase next) {
    _phase = next;
    switch (next) {
      case _Phase.drift:
        _phaseDur = 8 + _rnd.nextDouble() * 12; // 8..20 s
        break;
      case _Phase.accel:
        _phaseDur = 2.5;
        break;
      case _Phase.warp:
        _phaseDur = 4 + _rnd.nextDouble() * 6; // 4..10 s
        break;
      case _Phase.decel:
        _phaseDur = 3.0;
        break;
    }
    _phaseLeft = _phaseDur;
  }

  void _onTick(Duration elapsed) {
    final last = _lastTick;
    _lastTick = elapsed;
    if (last == null) return;
    var dt = (elapsed - last).inMicroseconds / 1000000.0;
    if (dt <= 0) return;
    if (dt > 0.05) dt = 0.05; // a hitch never teleports the field
    _advance(dt);
    _frame.value = elapsed.inMicroseconds / 1000000.0;
  }

  void _advance(double dt) {
    final scene = _scene;
    scene.time += dt;

    _phaseLeft -= dt;
    if (_phaseLeft <= 0) {
      _enterPhase(_Phase.values[(_phase.index + 1) % _Phase.values.length]);
    }
    final u = (1 - _phaseLeft / _phaseDur).clamp(0.0, 1.0);
    switch (_phase) {
      case _Phase.drift:
        scene.speed = _driftSpeed;
        break;
      case _Phase.accel:
        scene.speed =
            _driftSpeed + (_warpSpeed - _driftSpeed) * _easeInOut(u);
        break;
      case _Phase.warp:
        scene.speed = _warpSpeed;
        break;
      case _Phase.decel:
        scene.speed =
            _warpSpeed + (_driftSpeed - _warpSpeed) * _easeInOut(u);
        break;
    }

    // Galaxies are the payoff of slowing down: full view at
    // drift, nearly gone at warp, eased between.
    final norm =
        ((scene.speed - _driftSpeed) / (_warpSpeed - _driftSpeed))
            .clamp(0.0, 1.0);
    final target = 1 - 0.93 * norm;
    scene.galaxyAlpha +=
        (target - scene.galaxyAlpha) * math.min(1.0, dt * 0.9);

    final dz = scene.speed * dt * 0.5;
    for (final s in scene.stars) {
      s.z -= dz;
      if (s.z <= 0.06) _respawn(s);
    }
    for (final g in scene.galaxies) {
      g.fx += g.vx * dt * (1 + scene.speed * 0.35);
      g.fy += g.vy * dt;
      g.rotation += g.spin * dt;
      // Wrap around with a margin of one sprite width.
      if (scene.width > 0) {
        final m = g.size / scene.width + 0.05;
        if (g.vx > 0 && g.fx > 1 + m) g.fx = -m;
        if (g.vx < 0 && g.fx < -m) g.fx = 1 + m;
      }
      if (g.fy < -0.3) g.fy = 1.3;
      if (g.fy > 1.3) g.fy = -0.3;
    }
  }

  /// One spiral galaxy sprite: a bright soft core and two
  /// logarithmic spiral arms of points, alpha fading outward,
  /// tinted per galaxy. Drawn once, cached as an image.
  Future<ui.Image> _makeGalaxyImage(Color tint, int seed) async {
    const double dim = 256;
    final rnd = math.Random(seed);
    final recorder = ui.PictureRecorder();
    final canvas = Canvas(recorder);
    const center = Offset(dim / 2, dim / 2);

    final glow = Paint()
      ..shader = ui.Gradient.radial(center, dim * 0.30, [
        tint.withAlpha(64),
        tint.withAlpha(0),
      ]);
    canvas.drawCircle(center, dim * 0.30, glow);

    final paint = Paint()..strokeCap = StrokeCap.round;
    const arms = 2;
    const points = 850;
    for (var a = 0; a < arms; a++) {
      final armOffset = a * math.pi;
      for (var i = 0; i < points; i++) {
        final t = math.pow(rnd.nextDouble(), 0.72).toDouble() *
            math.pi *
            2.6;
        final r = 3 * math.exp(0.45 * t);
        if (r > 122) continue;
        final jitter = 1.5 + r * 0.07;
        final px = center.dx +
            r * math.cos(t + armOffset) +
            (rnd.nextDouble() * 2 - 1) * jitter;
        final py = center.dy +
            r * math.sin(t + armOffset) * 0.82 +
            (rnd.nextDouble() * 2 - 1) * jitter;
        final fade = (1 - r / 128).clamp(0.0, 1.0);
        final alpha =
            (fade * 165 * (0.35 + rnd.nextDouble() * 0.65)).round();
        if (alpha <= 2) continue;
        paint.color = Color.lerp(tint, Colors.white, rnd.nextDouble() * 0.55)!
            .withAlpha(alpha);
        paint.strokeWidth =
            (1 + rnd.nextDouble() * 2.1) * (1.15 - r / 170);
        canvas.drawCircle(Offset(px, py), paint.strokeWidth / 2, paint);
      }
    }
    final picture = recorder.endRecording();
    final img = await picture.toImage(dim.toInt(), dim.toInt());
    picture.dispose();
    return img;
  }

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        _scene.width = constraints.maxWidth;
        _scene.height = constraints.maxHeight;
        return RepaintBoundary(
          child: CustomPaint(
            painter: _SpacePainter(_scene, repaint: _frame),
            size: Size.infinite,
          ),
        );
      },
    );
  }
}

class _SpacePainter extends CustomPainter {
  _SpacePainter(this.scene, {required Listenable repaint})
      : super(repaint: repaint);

  final _Scene scene;
  final Paint _fill = Paint();
  final Paint _stroke = Paint()
    ..strokeCap = StrokeCap.round;
  final Paint _imagePaint = Paint()..filterQuality = FilterQuality.medium;

  @override
  void paint(Canvas canvas, Size size) {
    _paintNebula(canvas, size);
    _paintGalaxies(canvas, size);
    _paintStars(canvas, size);
  }

  void _paintNebula(Canvas canvas, Size size) {
    if (scene.nebulaW != size.width || scene.nebulaH != size.height) {
      scene.nebulaW = size.width;
      scene.nebulaH = size.height;
      final w = size.width;
      final h = size.height;
      scene.nebulaShaders[0] = ui.Gradient.radial(
        Offset(w * 0.16, h * 0.20),
        w * 0.55,
        const [Color(0x2E5E35B1), Color(0x005E35B1)],
      );
      scene.nebulaShaders[1] = ui.Gradient.radial(
        Offset(w * 0.86, h * 0.78),
        w * 0.50,
        const [Color(0x241565C0), Color(0x001565C0)],
      );
      scene.nebulaShaders[2] = ui.Gradient.radial(
        Offset(w * 0.50, h * 0.95),
        w * 0.46,
        const [Color(0x1A7B1FA2), Color(0x007B1FA2)],
      );
    }
    final rect = Offset.zero & size;
    for (final shader in scene.nebulaShaders) {
      if (shader == null) continue;
      _fill.shader = shader;
      canvas.drawRect(rect, _fill);
    }
    _fill.shader = null;
  }

  void _paintGalaxies(Canvas canvas, Size size) {
    final alpha = scene.galaxyAlpha.clamp(0.0, 1.0);
    if (alpha < 0.01) return;
    _imagePaint.colorFilter = ColorFilter.mode(
      Color.fromRGBO(255, 255, 255, alpha),
      BlendMode.modulate,
    );
    for (final g in scene.galaxies) {
      final img = g.image;
      if (img == null) continue;
      canvas.save();
      canvas.translate(g.fx * size.width, g.fy * size.height);
      canvas.rotate(g.rotation);
      canvas.drawImageRect(
        img,
        Rect.fromLTWH(0, 0, img.width.toDouble(), img.height.toDouble()),
        Rect.fromCenter(
            center: Offset.zero, width: g.size, height: g.size),
        _imagePaint,
      );
      canvas.restore();
    }
  }

  void _paintStars(Canvas canvas, Size size) {
    final cx = size.width / 2;
    final cy = size.height / 2;
    final scaleR = (size.height / 1080).clamp(0.7, 1.4);
    final speed = scene.speed;
    // Matches the per-frame z travel in _advance: streaks run
    // from where the star was last frame to where it is now.
    final dz = speed * 0.016 * 0.5;
    final warp = speed > 0.4;
    for (final s in scene.stars) {
      final sx = cx + (s.x / s.z) * cx;
      final sy = cy + (s.y / s.z) * cy;
      if (sx < -48 || sx > size.width + 48 || sy < -48 || sy > size.height + 48) {
        continue;
      }
      final twinkle =
          0.75 + 0.25 * math.sin(scene.time * s.twinkleSpeed + s.twinklePhase);
      final bright = (1.18 - s.z).clamp(0.22, 1.0) * twinkle;
      final color = _SpaceBackgroundState._starColors[s.colorIndex]
          .withAlpha((bright * 255).round().clamp(0, 255));
      final radius = ((1.1 - s.z) * 1.9 + 0.35) * scaleR;
      if (warp) {
        final pz = s.z + dz;
        final px = cx + (s.x / pz) * cx;
        final py = cy + (s.y / pz) * cy;
        _stroke.color = color;
        _stroke.strokeWidth = radius * 1.15;
        canvas.drawLine(Offset(px, py), Offset(sx, sy), _stroke);
      } else {
        _fill.color = color;
        canvas.drawCircle(Offset(sx, sy), radius, _fill);
      }
    }
  }

  @override
  bool shouldRepaint(_SpacePainter oldDelegate) =>
      !identical(scene, oldDelegate.scene);
}
