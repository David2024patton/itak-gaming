// Settings screen D-pad tests (2026-11-04 focus + popup rework).
//
// Test 1 is the regression test for the shipped bug: on 1.105 the
// rows were not focus nodes at all, so an UP press moved the manual
// highlight once and then framework traversal threw real focus to
// the AppBar back button, after which the remote was dead until a
// RIGHT press rescued it. Here the rows ARE focus nodes, so focus
// itself must walk the whole list in both directions.
//
// Tests 2-4 cover the popup rework: activating a row opens its
// popup with a default choice focused, and closing the popup
// returns focus to the row that opened it.
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:itak_gaming/main.dart';

String? focusLabel() => FocusManager.instance.primaryFocus?.debugLabel;

Future<void> key(WidgetTester tester, LogicalKeyboardKey k) async {
  await tester.sendKeyEvent(k);
  // Let the 180 ms ensure-visible scroll animation finish so the
  // next traversal step sees settled geometry.
  await tester.pump(const Duration(milliseconds: 220));
}

/// Text of the currently focused TextButton, if focus is on one.
String? focusedButtonText(WidgetTester tester) {
  final ctx = FocusManager.instance.primaryFocus?.context;
  if (ctx == null) return null;
  final btn = ctx.findAncestorWidgetOfExactType<TextButton>();
  if (btn == null) return null;
  final child = btn.child;
  return child is Text ? child.data : null;
}

Future<void> openSettings(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1920, 1080);
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(const MaterialApp(
    home: Scaffold(body: Center(child: Text('shell'))),
  ));
  tester
      .state<NavigatorState>(find.byType(Navigator))
      .push(MaterialPageRoute<void>(builder: (_) => const SettingsScreen()));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('D-pad walks the whole settings list and back',
      (tester) async {
    await openSettings(tester);
    expect(focusLabel(), 'settings-row-0');

    for (var i = 1; i <= 12; i++) {
      await key(tester, LogicalKeyboardKey.arrowDown);
      expect(focusLabel(), 'settings-row-$i', reason: 'after $i DOWNs');
    }
    // Bottom of the list: DOWN stays on the Back row.
    await key(tester, LogicalKeyboardKey.arrowDown);
    expect(focusLabel(), 'settings-row-12');

    // The shipped bug: this UP fled to the AppBar back button.
    await key(tester, LogicalKeyboardKey.arrowUp);
    expect(focusLabel(), 'settings-row-11');
    for (var i = 10; i >= 0; i--) {
      await key(tester, LogicalKeyboardKey.arrowUp);
      expect(focusLabel(), 'settings-row-$i', reason: 'walking back up');
    }

    // UP from the top row reaches the header back button on
    // purpose, and DOWN from it returns to row 0.
    await key(tester, LogicalKeyboardKey.arrowUp);
    final pf = FocusManager.instance.primaryFocus;
    expect(pf, isNotNull);
    expect(focusLabel(), isNot(startsWith('settings-row')));
    expect(pf!.context!.findAncestorWidgetOfExactType<BackButton>(),
        isNotNull);
    await key(tester, LogicalKeyboardKey.arrowDown);
    expect(focusLabel(), 'settings-row-0');
  });

  testWidgets('Toggle row opens its popup, applies, refocuses the row',
      (tester) async {
    await openSettings(tester);
    expect(focusLabel(), 'settings-row-0');

    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(find.text('Swap A and B buttons'), findsWidgets);
    expect(find.text('Now: Off'), findsOneWidget);
    // Default focus sits on the current value's button.
    expect(focusedButtonText(tester), 'Off');

    await key(tester, LogicalKeyboardKey.arrowRight);
    expect(focusedButtonText(tester), 'On');
    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(focusLabel(), 'settings-row-0');

    // Reopening proves the pick was applied: it now reads On, and
    // the default focus moved to the On button.
    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.text('Now: On'), findsOneWidget);
    expect(focusedButtonText(tester), 'On');
    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(focusLabel(), 'settings-row-0');
  });

  testWidgets('Sound test popup: buttons walkable, Close refocuses row',
      (tester) async {
    await openSettings(tester);
    for (var i = 0; i < 4; i++) {
      await key(tester, LogicalKeyboardKey.arrowDown);
    }
    expect(focusLabel(), 'settings-row-4');

    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(find.text('Sound test'), findsWidgets);
    // Scope to the dialog: 'Send debug report now' is also row 10's
    // label in the list behind the popup.
    final inDialog = find.descendant(
        of: find.byType(AlertDialog), matching: find.byType(TextButton));
    expect(
        find.descendant(
            of: find.byType(AlertDialog),
            matching: find.text('Measure sound delay')),
        findsOneWidget);
    expect(
        find.descendant(
            of: find.byType(AlertDialog),
            matching: find.text('Send debug report now')),
        findsOneWidget);
    expect(inDialog, findsNWidgets(3));
    expect(focusedButtonText(tester), 'Measure sound delay');

    await key(tester, LogicalKeyboardKey.arrowRight);
    expect(focusedButtonText(tester), 'Send debug report now');
    await key(tester, LogicalKeyboardKey.arrowRight);
    expect(focusedButtonText(tester), 'Close');
    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(focusLabel(), 'settings-row-4');
  });

  testWidgets('Logs popup opens with Close focused and refocuses row',
      (tester) async {
    await openSettings(tester);
    for (var i = 0; i < 6; i++) {
      await key(tester, LogicalKeyboardKey.arrowDown);
    }
    expect(focusLabel(), 'settings-row-6');

    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(find.text('Logs'), findsWidgets);
    expect(focusedButtonText(tester), 'Close');

    await key(tester, LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(focusLabel(), 'settings-row-6');
  });
}
