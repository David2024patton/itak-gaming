# Sound test screen: notes for the wiring step

The screen itself is done: `SoundTestActivity` lives in
`flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/SoundTestActivity.kt`
and is registered in `AndroidManifest.xml` as `.SoundTestActivity`,
exported false, landscape, fullscreen theme, label "Sound test".
What remains is one launch hook from Settings, which is a separate
step because it touches MainActivity and main.dart.

## Proposed launch contract

No extras, no arguments, no return value. Either of these works:

1. Preferred, matches how the rest of the app talks to native
   code: add one case to the existing MethodChannel handler in
   MainActivity (channel name `net.itak/emu`), like this:

   - Method name: `soundTest`
   - Handler body: `startActivity(Intent(this, SoundTestActivity::class.java))`,
     then `result.success(null)`.
   - Dart side, later, from the Settings screen:
     `MethodChannel('net.itak/emu').invokeMethod('soundTest')`.

2. Or any Kotlin code inside the app can launch it directly with
   an intent to `net.itak.itak_gaming.SoundTestActivity`. The
   fully qualified class name is
   `net.itak.itak_gaming.SoundTestActivity`.

BACK inside the screen finishes the activity, so the caller needs
no result handling.

## Mic input fallback order (as implemented)

AudioRecord configurations are tried in this order, and the first
one that both initializes and reaches the recording state wins:

1. Source VOICE_RECOGNITION at 44100 Hz
2. Source VOICE_RECOGNITION at 16000 Hz
3. Source MIC at 44100 Hz
4. Source MIC at 16000 Hz

All attempts are mono, PCM 16 bit, with a buffer of the larger of
the platform minimum and 4096 bytes.

The builder never pins a specific device, so recording goes to
the system default input. A USB microphone plugged into the box
becomes that default and is picked up automatically. Bluetooth
SCO is deliberately not forced: a box that exposes a Bluetooth
mic as a normal input device will use it, and a box that does
not will honestly report that no microphone was found, which is
exactly what this screen exists to find out before voice chat
depends on it.

## Speaker path

The jingle is generated PCM, no audio assets: three rising notes
at 440, 554.37 and 659.25 Hz, 0.60 seconds each with 0.08 second
gaps (1.96 seconds total), stereo, peak level 0.62 of full scale.
The AudioTrack uses USAGE_GAME so the test exercises the same
audio path the emulator uses, and a fixed track volume.

## Meter math

- Speaker bar target: the peak absolute sample value of the
  generated chunk most recently written to the AudioTrack,
  divided by 32767.
- Mic bar target: the peak absolute sample value of the most
  recent AudioRecord buffer, divided by 32768.
- Peak percent label: the loudest mic buffer peak seen since the
  test started, as peak times 100 divided by 32768.
- Display easing runs on a 40 ms ticker: the shown level closes
  55 percent of the gap when rising and multiplies by 0.86 when
  falling, so bars punch up fast and settle to zero instead of
  snapping off.

## Permission note

`RECORD_AUDIO` was added to the manifest alongside the activity
entry. Without the declaration the runtime request from the
screen would be denied instantly with no dialog, and the mic
test could never run. The screen still handles denial itself:
it asks with the standard requestPermissions flow and, on
denial, shows plain text saying the box cannot hear you.
