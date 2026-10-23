import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:video_player/video_player.dart';

import 'main.dart' show Prefs, SystemSelectScreen, appLog;

// ---------------------------------------------------------------------------
// Launch intro (owner, 2026-10-23): the iTaK Gaming intro plays once
// on a cold start, before the home screen. Any remote key (DPAD,
// CENTER, BACK) or a tap skips it, it hands off to home when it
// ends, and any failure at all (missing asset, player error, a
// stalled start) falls through to home instead: the intro can
// never trap or crash the app. Settings -> Intro video (pref
// 'intro_video', default on) turns it off entirely; with it off
// the app opens straight to home as it always did. The gate
// gives its verdict at most once per app process: returning
// home after a game rebuilds this widget, and the intro must
// not replay for that (seen on hardware 2026-10-23); a cold
// start is a new process and plays it again.
// ---------------------------------------------------------------------------

const String kIntroAsset = 'assets/intro/itak_intro.mp4';
const String kIntroPref = 'intro_video';

enum _IntroEnd { done, skipped, error, off }

class IntroGate extends StatefulWidget {
  const IntroGate({super.key});

  @override
  State<IntroGate> createState() => _IntroGateState();
}

class _IntroGateState extends State<IntroGate> {
  /// Set the first time the gate reaches any verdict (played,
  /// skipped, errored, off) in this process. Static on purpose:
  /// State objects are recreated when the home route rebuilds
  /// on the way back from a game, but the process, and this
  /// flag, live on, so the intro plays at most once per launch.
  static bool _verdictGiven = false;

  final FocusNode _focusNode = FocusNode();
  VideoPlayerController? _controller;
  Timer? _watchdog;
  bool _showHome = false;
  bool _finished = false;
  bool _started = false;

  @override
  void initState() {
    super.initState();
    if (_verdictGiven) {
      // The gate already ran once in this process (the player
      // is returning home from a game): go straight home.
      _finished = true;
      _showHome = true;
      return;
    }
    // Absolute backstop: whatever happens below, the app reaches
    // home. Twenty seconds is far beyond the 10 s clip plus any
    // sane start-up stall.
    _watchdog = Timer(const Duration(seconds: 20), () {
      _finish(_IntroEnd.done);
    });
    _run();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _focusNode.requestFocus();
    });
  }

  Future<void> _run() async {
    try {
      final pref = await Prefs.get(kIntroPref);
      if (_finished) return;
      if (pref == '0') {
        _finish(_IntroEnd.off);
        return;
      }
      final controller = VideoPlayerController.asset(kIntroAsset);
      _controller = controller;
      await controller.initialize().timeout(const Duration(seconds: 6));
      if (_finished) return;
      controller.addListener(_onPlayerUpdate);
      await controller.play();
      if (_finished) return;
      _started = true;
      appLog('intro: start');
      if (mounted) setState(() {});
    } catch (e) {
      _finish(_IntroEnd.error, '$e');
    }
  }

  void _onPlayerUpdate() {
    final controller = _controller;
    if (controller == null || _finished) return;
    final value = controller.value;
    if (value.hasError) {
      _finish(_IntroEnd.error, value.errorDescription ?? 'player error');
    } else if (value.isCompleted) {
      _finish(_IntroEnd.done);
    }
  }

  void _finish(_IntroEnd end, [String? detail]) {
    if (_finished) return;
    _finished = true;
    _verdictGiven = true;
    _watchdog?.cancel();
    final controller = _controller;
    _controller = null;
    if (controller != null) {
      controller.removeListener(_onPlayerUpdate);
      // Dispose once the player widget is out of the tree.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        controller.dispose();
      });
    }
    switch (end) {
      case _IntroEnd.done:
        appLog('intro: done');
        break;
      case _IntroEnd.skipped:
        appLog('intro: skipped');
        break;
      case _IntroEnd.error:
        appLog('intro: error ${detail ?? 'unknown'}');
        break;
      case _IntroEnd.off:
        break;
    }
    if (mounted) {
      setState(() => _showHome = true);
    } else {
      _showHome = true;
    }
  }

  void _onKey(KeyEvent event) {
    if (event is KeyDownEvent) _finish(_IntroEnd.skipped);
  }

  @override
  void dispose() {
    _watchdog?.cancel();
    _controller?.removeListener(_onPlayerUpdate);
    _controller?.dispose();
    _focusNode.dispose();
    super.dispose();
  }

  Widget _playerWidget() {
    final controller = _controller;
    if (!_started || controller == null || !controller.value.isInitialized) {
      return const SizedBox.expand();
    }
    return Center(
      child: AspectRatio(
        aspectRatio: controller.value.aspectRatio,
        child: VideoPlayer(controller),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    if (_showHome) return const SystemSelectScreen();
    return PopScope(
      canPop: false,
      child: Scaffold(
        backgroundColor: Colors.black,
        body: KeyboardListener(
          focusNode: _focusNode,
          onKeyEvent: _onKey,
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTap: () => _finish(_IntroEnd.skipped),
            child: Container(
              color: Colors.black,
              child: _playerWidget(),
            ),
          ),
        ),
      ),
    );
  }
}
