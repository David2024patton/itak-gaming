# Settings screen: D-pad focus fix + popup rework (candidate, not shipped)

Tree: ~/itak-scratch-settingsfix (copy of the 1.105 app tree).
Changed files: flutter_ui/lib/main.dart (Settings screen + a new
_LogsPopup class), flutter_ui/android/.../MainActivity.kt (one new
channel case), flutter_ui/test/settings_screen_test.dart (new tests).
Nothing pushed, published, or staged. No TV contact.

## The bug (owner, on 1.105)

In Settings, pressing UP deep in the list threw focus to the circular
back arrow at the top-left of the header band. RIGHT rescued it once,
UP then moved exactly one row, and focus escaped to the back button
again. Vertical navigation inside the list was effectively broken.

## Root cause

SettingsScreen (main.dart) was the only menu screen still using a
manual-cursor design:

- The 13 rows were plain GestureDetector + AnimatedContainer cards.
  None of them was a focusable widget; the highlight was just an int
  (_row) driving a border color.
- The only focus node in the body was one KeyboardListener node
  (_focusNode) whose render box spans the whole body. Its handler
  (_onKey) updated _row on arrow keys -- but KeyboardListener always
  returns KeyEventResult.ignored, so the framework's default
  directional focus traversal ALSO processed every arrow press,
  geometrically, against the only two traversal targets in the route:
  that body-wide node and the AppBar's BackButton (a focusable
  IconButton in the header band, left of the "Settings" title).
- UP from the body-wide rect: the one node above its top edge is the
  BackButton, so real focus jumped there (the highlight moved once via
  _onKey on the same press, which is why it looked like it half
  worked). The BackButton lives in the AppBar subtree, NOT under the
  KeyboardListener, so from then on _onKey never fired again and the
  remote was dead.
- Owner's workaround, decoded: RIGHT from the BackButton traversed
  back into the body-wide node ("somewhere inside the row"), the next
  UP fired _onKey once (one row) AND re-triggered traversal straight
  back to the BackButton. Exactly the reported behavior.
- DOWN mostly survived because nothing focusable sits below the body
  rect, so traversal found no candidate and focus stayed put.

Screens that navigate fine (home grid, game lists, Manage players,
Who's playing) use real per-item focusables (InkWell/Focus with
onFocusChange, 1.98 restructure): every item owns a node with its own
rect, so traversal steps item to item. LogScreen likewise returns
KeyEventResult from a real Focus handler. Settings now follows the
same pattern.

## The fix (focus)

- 13 FocusNodes (_rowFocus, debug labels settings-row-0..12), one per
  row, via a _focusableRow(index, child) wrapper (Focus +
  onFocusChange). When focus lands on a row, _row follows it and
  _ensureRowVisible scrolls it into view: highlight and focus are the
  same thing by construction.
- _onKey no longer handles UP/DOWN (traversal owns them) and no
  longer cycles values on LEFT/RIGHT. It only activates the focused
  row on enter/select/space.
- _focusNode.skipTraversal = true, and focus starts on row 0's node:
  the body-wide listener node can never be a traversal target again,
  but still receives key events as an ancestor of the focused row.
- Resulting geometry: UP/DOWN step card to card through the whole
  list and stop at the ends (the old modulo wrap-around is gone,
  matching the rest of the app). UP from row 0 reaches the header
  back button deliberately; DOWN from it returns to row 0.

## The popup rework (owner: rows pop up like a box, AlphaFlux style)

Activation (CENTER or tap) on a row opens an AlertDialog in the app's
established popup style (showDialog, barrier black 0.8, card
0xFF262640, green 0xFF00E676 affirmative, BACK closes), with a default
choice autofocused. Closing any popup returns focus to the
originating row (_activateRow -> _restoreRowFocus).

- Toggle rows (0 Swap, 1 Box art, 2 Auto-save, 3 Friend alerts,
  7 Space background, 8 Intro video, 9 Debug reports): popup titled
  with the row label, content "Now: On/Off", actions Off / On with
  the current value green + bold and autofocused; picking applies
  state + pref (same writes the old _adjust made) and closes.
- Sound test (4): popup carrying the DEBUG card from the native
  SoundTestActivity: "Measure sound delay" button + readout text,
  "Send debug report now" button + status text. The measurement runs
  through ONE new channel case, "measureSoundDelay" in
  MainActivity.kt, whose body is the same algorithm and result
  wording as SoundTestActivity.measureDelay (EmuAudioOutput track,
  220 Hz tone, AudioTimestamp median), minus its UI posts; it runs
  ~3 s on a worker thread. The report button uses the Dart
  DebugChute.sendReport (immediate send with a real sent/failed
  result in the popup). The speaker/mic level-bar tests are NOT in
  the popup; SoundTestActivity still exists in the APK but Settings
  no longer launches it (the Dart 'soundTest' call is gone).
- App updates (5): popup shows the installed build and a "Check for
  updates" button; the button closes the popup and runs the
  pre-existing _openUpdates flow byte-for-byte (its own up-to-date /
  offer / failure dialogs unchanged).
- Logs (6): _LogsPopup, a scrollable popup box (780x420) fed by the
  same readLog channel as LogScreen, opening scrolled to the bottom,
  with Page up / Page down (+-320, same step as LogScreen), Refresh,
  and Close (autofocused). LogScreen itself is untouched and still in
  the tree.
- Send report (10): one popup runs the whole flow in place: the
  Debug-reports-off gate message (same words), else confirm (Send
  autofocused) -> "Sending..." -> "Report sent" / "Report failed"
  with the same result strings the old dialogs used.
- Install code (11): read-only popup showing the FULL install code
  (the row keeps its 8-character short form).
- Back (12): plain exit row, no popup, unchanged.

## Verification (no TV)

- flutter analyze: exactly the 2 pre-existing use_build_context_
  synchronously infos (same two spots, shifted line numbers). No new
  issues, test file included.
- flutter test (test/settings_screen_test.dart, 1920x1080 surface,
  Settings pushed over a dummy home so the AppBar back button
  exists): 4/4 passed.
  1. Focus starts at row 0; 12 DOWNs walk rows 1..12; DOWN stays at
     12; UP returns row by row to 0 (the shipped bug's exact path);
     UP from row 0 lands on the AppBar BackButton; DOWN returns.
  2. Toggle popup: opens on row 0, reads "Now: Off", default focus
     on the Off button; RIGHT + OK applies On (proven by reopening:
     "Now: On", focus on On); closing restores focus to row 0.
  3. Sound popup: opens on row 4; buttons walk Measure -> Send ->
     Close by RIGHT; Close restores focus to row 4. (The measure
     button itself is not pressed in tests; its native side cannot
     run in a widget test.)
  4. Logs popup: opens on row 6 with Close focused; Close restores
     focus to row 6.
- flutter build apk --release --target-platform android-arm:
  success, fresh 33,902,981-byte APK (Gradle compiled the Kotlin
  channel addition).

## Not verified / needs a human or the TV

- On-device D-pad feel and the popup focus visuals on the real TV
  (the standard post-ship TV proof still owed, with the owner).
- The measureSoundDelay channel end-to-end on hardware: it compiles
  and mirrors the proven native measurement, but has not run on a
  real box.
- BACK key closing each popup is the platform default for dialog
  routes (same as every other dialog in the app); not separately
  proven in tests.
