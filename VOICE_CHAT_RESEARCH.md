# Voice Chat Research Spike

Date: 2026-10-12. Status: research only, no code written.
Scope: two player voice chat during netplay (waiting room and in game),
building on the text chat that already shipped.

## How our stack constrains this (read first)

Facts from our own tree, because they decide most of the design:

* The lobby server relays JSON envelopes verbatim between the two
  players and never parses them (netplay/PROTOCOL.md section 3).
  Section 3.1 already reserves `{"type":"offer"}`, `{"type":"answer"}`
  and `{"type":"ice"}` envelopes over that socket, with the guest as
  offerer and the host as answerer. Envelope cap is 256 KB; an SDP
  body is a few KB, so size is a non issue.
* The WebSocket is owned by Go. `netsession.reader()` in
  go_backend/internal/netsession/step.go unmarshals every envelope
  and dispatches on `type`; unknown types are dropped. Kotlin never
  touches the socket.
* Text chat already proved the pattern for Kotlin facing session
  data: go_backend/internal/netsession/chat.go stores messages in
  the session, and go_backend/mobile/netplay.go exposes
  `NetplaySendChat` plus `NetplayChatJSON(afterSeq)` over the
  gomobile bridge. EmulatorActivity polls that JSON from its room
  ticker and chat ticker and renders programmatic views.
* Game audio is ours, not the system's: EmulatorActivity.startAudio()
  builds an AudioTrack with USAGE_GAME and CONTENT_TYPE_MUSIC and
  feeds it from `Mobile.drainAudio()` on the game loop thread.
* The app builds armeabi-v7a only (abiFilters in
  flutter_ui/android/app/build.gradle, for the Chromecast HD) with
  minSdk 24. Any native library we add must ship a 32 bit ARM build.
* Repositories are already google() plus mavenCentral() in
  flutter_ui/android/build.gradle. Nothing else is configured.
* The manifest has no RECORD_AUDIO permission today.
* Remote keys today (EmulatorActivity.mapButtonRaw): DPAD_CENTER and
  ENTER are ButtonB, KEYCODE_ASSIST and KEYCODE_VOICE_ASSIST are
  ButtonA, MEDIA_PLAY_PAUSE is Start, and the gamepad thumb stick
  clicks (KEYCODE_BUTTON_THUMB_L / THUMB_R) are not mapped at all.

## 1. The WebRTC library

### Google's own artifact is a dead end

`org.webrtc:google-webrtc` stopped at version 1.0.32006 in August
2020. It was only ever published to Google's Bintray repo and
JCenter, both shut down years ago, so it does not resolve from
google() or mavenCentral() in a fresh Gradle build today. Anyone
still referencing it is pinned to a 2020 WebRTC with six years of
unpatched media stack bugs. Do not use it.

Source: https://mvnrepository.com/artifact/org.webrtc/google-webrtc/1.0.32006
(artifact record shows the Bintray repo and the Aug 2020 date).

### Use io.github.webrtc-sdk:android from Maven Central

The maintained successor is the webrtc-sdk community build:

* Coordinates: `io.github.webrtc-sdk:android`, published on Maven
  Central, so our existing mavenCentral() resolves it with no repo
  changes. Current builds track upstream WebRTC milestones (POMs
  and the project README show the 144 and 150 milestone lines;
  pin the newest at build time).
* License: BSD 3-Clause, stated in the Maven POM. That is the
  upstream WebRTC license, permissive, fine next to our MIT repo.
  One catch found in a third party audit of the AAR: the AAR itself
  contains no license or notice file, so we should ship the WebRTC
  BSD notice in our own notices file when we redistribute the APK.
* Contents: the AAR bundles native libs for arm64-v8a, armeabi-v7a,
  x86 and x86_64, and declares minSdk 21. The armeabi-v7a slice is
  roughly 7 MB uncompressed; because our build already sets
  abiFilters to armeabi-v7a, only that slice lands in the APK.
* API compatibility: it keeps the upstream `org.webrtc` package, so
  all upstream API documentation and examples apply unchanged.

Sources:
https://central.sonatype.com/artifact/io.github.webrtc-sdk/android
https://github.com/webrtc-sdk/android/blob/HEAD/README.md
https://github.com/shady-2096/meo-mic/blob/HEAD/adr/0008-android-webrtc-distribution.md
(AAR contents, ARMv7 size, minSdk and license findings.)

### Alternatives, briefly

* `org.jitsi:webrtc`: Jitsi's builds of the same upstream code, also
  on Maven Central, kept current for Jitsi Meet. A credible fallback
  if the webrtc-sdk line ever stalls.
* Building libwebrtc from source: always possible, always a last
  resort. Hours of depot_tools and GN work per milestone and a big
  burden on the build server. Not justified for audio only.
* Signal's `org.whispersystems:webrtc-android` is ancient (M77) and
  GPLv3. Reject on both counts.

## 2. Audio only PeerConnection in Kotlin, and where signaling lives

### Minimal shape

One small Kotlin class (call it VoiceChat.kt) next to
EmulatorActivity, roughly:

1. One time init: `PeerConnectionFactory.initialize(...)` with
   default options, then build the factory with a
   `JavaAudioDeviceModule` (see section 3 for its settings) and no
   video factories at all. Audio only means no EGL, no capturers,
   no surface views.
2. ICE config: one STUN server entry (section 4), no TURN in v1.
3. `factory.createPeerConnection(rtcConfig, observer)` with a
   single local audio track:
   `createAudioSource(MediaConstraints())` then
   `createAudioTrack("voice", source)`, added with `addTrack`.
   Incoming audio needs no code beyond the track existing;
   libwebrtc plays remote audio through the audio device module
   automatically.
4. Offer and answer: the guest creates the offer
   (`pc.createOffer` then `setLocalDescription`), serializes
   `{"type":"offer","sdp":...}` and hands it to the bridge. The host
   sets it as remote description, creates the answer, and replies
   with `{"type":"answer","sdp":...}`. This is exactly the exchange
   PROTOCOL.md 3.1 already describes, roles included, which also
   removes any glare question: only the guest ever offers.
5. ICE trickle: `onIceCandidate` in Kotlin serializes each candidate
   into `{"type":"ice","candidate":...,"sdpMid":...,"sdpMLineIndex":...}`;
   the reverse path calls `pc.addIceCandidate`. Buffer incoming
   candidates until the remote description is set.
6. Mute and push to talk are one call: `audioTrack.setEnabled(...)`.
   No renegotiation needed.

Signaling messages are tiny and infrequent (one offer, one answer,
a burst of ICE candidates in the first second). A polling bridge at
the cadence the room and chat tickers already use is plenty fast;
nothing here needs sub second delivery beyond what chat already
tolerates.

### Recommendation: signaling rides through Go, media stays in Kotlin

Put a `signal.go` in internal/netsession that is a deliberate copy
of chat.go's shape: the reader switch gains cases for `offer`,
`answer` and `ice` that append the raw payloads to a small capped
queue; `Session.SendSignal(rawJSON)` writes an envelope out;
`SignalSince(afterSeq)` drains it with the same cursor pattern as
`ChatSince`. The gomobile bridge gains `NetplaySendSignal(json)` and
`NetplaySignalJSON(afterSeq)`, mirroring the chat pair one for one.
Kotlin polls, parses each payload, and feeds the PeerConnection.
Voice starts when the waiting room forms (so players can talk
before Ready, like the text chat) and stays up for the whole
session; the envelopes ride the same socket before, during and
after the lockstep game because the socket outlives every phase.

Why not let Kotlin open its own second WebSocket to the lobby
server and keep Go out of it: the server caps concurrent sockets
per IP (10), the token and lobby lifecycle already live in the Go
session, a second socket can die or be reaped independently of the
game, and we would maintain two connection states for one room.
The Go queue costs a few dozen lines and reuses a proven shape.

### Echo cancellation and processing defaults

libwebrtc's built in audio processing (AEC3 echo canceller, noise
suppression, automatic gain control) runs on the capture path by
default; it is not something we have to switch on. On top of that
the JavaAudioDeviceModule builder offers
`setUseHardwareAcousticEchoCanceler` and
`setUseHardwareNoiseSuppressor`, which engage the device's own
effects when the audio HAL advertises them, and
`setAudioSource(...)`, where MediaRecorder.AudioSource
.VOICE_COMMUNICATION is the right pick because Android attaches
its voice path processing to that source. Recommendation: ship the
defaults first (VOICE_COMMUNICATION source, hardware effects
allowed). The builder flags exist because some device HALs ship
broken hardware AEC; if the Chromecast or a future box misbehaves,
flipping those two flags to false forces the software path, which
is the known workaround in the field.
## 3. Microphones on Android TV: the honest picture

### The remote control mic is not available to us

This is the finding that shapes the feature. On Google TV devices
like our Chromecast HD, the Voice Remote's microphone streams over
Bluetooth to the system's voice interaction service. Google's own
support docs describe the button purely as the Assistant trigger:
press and hold the microphone button and Google Assistant listens.
The remote mic does not appear as an audio input device that apps
can enumerate, and no AudioSource value selects it. Third party
projects that capture this remote's audio do it by talking to the
remote's Bluetooth protocol directly from another computer, which
tells you where the audio actually terminates: the system voice
service, not the app audio stack.

Our own activity does receive KEYCODE_ASSIST and KEYCODE_VOICE_ASSIST
key events on our box (they are mapped to ButtonA today), so the
press reaches us. The audio does not. Plan on the remote mic being
Assistant only; voice chat needs a real audio input device.

Sources:
https://support.google.com/chromecast/answer/7582438?hl=en
https://github.com/kaliscuit/remotebuddy/blob/HEAD/README.en.md

### USB microphone: the reliable v1 input

A USB Audio Class mic or headset dongle plugged into the TV (on
the Chromecast, through the usual powered USB hub) enumerates as a
normal input device (AudioDeviceInfo TYPE_USB_HEADSET or
TYPE_USB_DEVICE) and standard capture works once RECORD_AUDIO is
granted. WebRTC's audio module records from the system default
communication input, which a plugged in USB mic normally becomes.
Two cautions: enumerate inputs at runtime and show the chosen
input's name in the voice settings, so "no mic found" is a visible
state rather than silent failure; and TV class hardware varies
(at least one popular TV box line has a broken USB audio HAL), so
verify on the actual Death Star Chromecast before promising
anything. That verification is step zero of the build.

### Bluetooth headset (SCO): best effort only

TV boxes pair Bluetooth headsets for output (A2DP) readily. The
microphone half needs the SCO / HFP input path, and support on
Android TV devices is inconsistent to absent; many boxes simply
never expose a TYPE_BLUETOOTH_SCO input. If one appears, the
standard capture path picks it up with no special code. Treat it
as a pleasant surprise, never a v1 dependency.

### RECORD_AUDIO permission flow on TV

RECORD_AUDIO is a runtime (dangerous) permission. The request
dialog works on Android TV and is D-pad navigable; there is no
missing permission UI problem on stock Google TV. Rules for us:

* Declare the permission in the manifest (it is absent today), and
  mark the microphone feature not required
  (`android.hardware.microphone` required false) so the app keeps
  installing on mic less boxes, consistent with how the manifest
  already treats touchscreen and leanback.
* Ask lazily, the first time the player turns voice on in the
  waiting room or settings, never at app start. Denial degrades to
  text chat plus listening, with one line of explanation.
* Capture only ever runs while EmulatorActivity is in the
  foreground inside a session, so Android's background microphone
  restrictions (no capture from background apps on modern Android)
  never come into play. Expect the system mic indicator while
  voice is live; that is correct behavior, not a bug.

### Mixing voice with game audio, and the echo question

No manual mixing is needed. Game audio keeps its own AudioTrack
(USAGE_GAME). WebRTC plays the partner's voice through its audio
device module's own track (voice communication attributes) and
AudioFlinger mixes the two streams. Volume keys keep controlling
the TV as today.

Two real interactions to watch:

* The Java audio module puts the device into MODE_IN_COMMUNICATION
  while voice runs. On a TV there is no earpiece to misroute to,
  so this is mostly harmless, but if a Bluetooth SCO device is
  connected it can try to pull all audio toward the headset. The
  module builder accepts custom AudioAttributes for its playout,
  which is the lever if routing misbehaves on real hardware.
* Echo: WebRTC's echo canceller uses its own rendered signal (the
  partner's voice) as its reference. Our game audio is rendered by
  a different track, so the canceller does not know it exists and
  cannot subtract it. Game sound coming out of TV speakers will
  reach a room mic as ordinary background sound. Noise suppression
  dulls it; it does not remove it. The practical defenses are the
  ones speakerphone users have always had: a mic near the player
  (USB mic or headset) instead of across the room, reasonable TV
  volume, and push to talk (section 5). This is the strongest
  argument for making push to talk the default.

## 4. NAT: STUN first, TURN only on evidence

Recommendation: agree with the staged plan. Ship v1 with STUN
only (public STUN, for example Google's stun.l.google.com:19302),
log the ICE connection state and the winning candidate pair type
on both boxes, and run the real Kentucky to Missouri test. Add
TURN only if that test actually fails.

Why this is the right order:

* Both endpoints are ordinary home broadband routers, one per
  household, each player directly on their home network. Consumer
  NAT is overwhelmingly cone style (full, restricted or port
  restricted), and ICE connects cone to cone through server
  reflexive candidates alone. Symmetric NAT, the kind that defeats
  STUN, is mostly a carrier grade NAT and enterprise firewall
  phenomenon, not a home router one.
* Voice is the most forgiving WebRTC workload: one low bitrate
  Opus stream, no video, tolerant of the extra milliseconds a
  relay would add anyway.
* Skipping TURN keeps v1 free of new server surface. TURN is not
  just a package install; it is a public relay that needs
  credential hygiene and abuse limits (below).
* The failure, if it exists, is cheap to detect: ICE state goes to
  failed or stays disconnected on one or both boxes, and the
  candidate log shows no srflx pair. We will know in one session
  whether these two specific households need a relay, instead of
  guessing for households in general.

The TURN fallback, scoped so it can be added in an evening:
coturn on the same Ubuntu server that hosts the lobby. Install via
apt (`apt install coturn`) or the official coturn/coturn Docker
image with host networking (TURN needs its full UDP relay range,
which is why host networking is the norm). Ports: 3478 UDP and TCP
for STUN and TURN, 5349 for TURN over TLS if wanted, and the relay
range 49152 to 65535 UDP, which for two households can be pinned
much narrower with min-port and max-port. Credentials: use the
time limited model, coturn's `use-auth-secret` with a
`static-auth-secret`; the lobby server mints usernames of the form
expiry:player with an HMAC-SHA1 password, valid for hours, so no
long lived password ever ships inside the app. Lock it down with
quotas and denied-peer-ip ranges for private networks, because an
open TURN server on the public internet gets found and abused as
free bandwidth. The clients then get one extra iceServers entry
with those minted credentials, delivered through the lobby exactly
like other session setup data. If even TURN failed (it would mean
UDP is blocked outright on one side), the last resort is relaying
voice over the lobby WebSocket itself; that is a server change and
explicitly out of scope unless reality demands it.

## 5. Push to talk: which buttons actually exist

Constraints from our own key map and from Android TV behavior:

* The remote's mic button sends KEYCODE_ASSIST / KEYCODE_VOICE_ASSIST.
  On our box the key event does reach the activity (it is ButtonA
  today). But holding it is the system gesture for summoning the
  Assistant, OEM behavior varies, and the same key is a gameplay
  button (A). Using it as hold to talk would fight the Assistant,
  fire A in game, and behave differently on the Missouri box if its
  remote differs. Not viable as the primary.
* KEYCODE_VOICE_INPUT exists on some remotes but is delivered even
  less consistently than ASSIST. Same verdict.
* Every face button, shoulder and the D-pad are gameplay inputs in
  the 16 button mask; stealing one breaks games that use the full
  pad.
* The gamepad stick clicks, KEYCODE_BUTTON_THUMB_L (L3) and
  KEYCODE_BUTTON_THUMB_R (R3), are the exception: mapButtonRaw does
  not map them at all, libretro cores in our set do not consume
  them, and a stick click is holdable for minutes without
  interfering with play.

Recommended scheme:

* Default: push to talk, hold R3 on a gamepad (L3 accepted too).
  Implemented at the existing onKeyDown / onKeyUp choke point:
  when voice is on, THUMB_R down calls setEnabled(true) on the
  audio track, up calls setEnabled(false). Latency is a non issue
  because the track and connection stay live; only the gate moves.
* Remote only players (no gamepad): a voice mode setting with
  three values: Push to talk (R3), Toggle (tap the remote mic /
  ASSIST key once to open the mic, tap again to close; a tap is a
  key event we already receive, and a tap does not summon the
  Assistant the way a hold does), and Open mic.
* Open mic offered for headset and USB mic users who want it, off
  by default, because of the echo picture in section 3.
* A mute state indicator lives in the waiting room and pause menu
  (Mic live / Mic muted / No mic found), plus a brief on screen
  flash when the mic opens in game, so nobody guesses whether they
  are transmitting.

## 6. Effort estimate and top risks

Rough sizes, in the order we would build them:

* Step zero hardware check (S): enumerate audio inputs on the
  Death Star with a USB mic attached; confirm capture works before
  any WebRTC code. Half a day including the write up.
* Gradle plus manifest (S): one dependency line
  (io.github.webrtc-sdk:android), RECORD_AUDIO, microphone feature
  not required. An hour, plus one pipeline build to prove the
  server resolves Maven Central and the v7a slice links. Note:
  release builds are not minified today; if minify is ever
  enabled, add the standard org.webrtc keep rules.
* Go signaling queue (S): signal.go beside chat.go (about 120
  lines), three reader cases, two bridge functions in
  mobile/netplay.go, plus unit tests mirroring the chat tests.
  Well under a day.
* Kotlin VoiceChat module (L): the real work. Factory and audio
  module setup, one PeerConnection, offer / answer / ICE glue to
  the bridge, connection state reporting, teardown tied to
  session end. About 400 to 600 lines, two to three days
  including on device debugging.
* UI surfaces (M): waiting room voice status row, mic request
  prompt, input device name. Pause menu voice entry with mode and
  mute state. Settings (Dart side prefs read by Kotlin, the
  established pattern): voice on or off, mode (PTT / toggle /
  open), read by the activity at session start. One to two days
  across Kotlin and Dart.
* NAT field test (S): one real Kentucky to Missouri session with
  ICE logging on both ends. The coturn fallback, only if
  triggered, is about half a day of server work plus a small
  lobby server addition to mint credentials.

Top three risks:

1. Input hardware reality. The remote mic is Assistant only, so
   voice chat silently depends on each household having a USB mic
   or a headset the box can hear. If the Missouri box has neither,
   the feature ships to an audience of one. This is why the
   hardware check is step zero, not a launch day surprise.
2. Audio path fights on cheap TV hardware. Broken or absent
   hardware echo cancellation, MODE_IN_COMMUNICATION rerouting
   surprises, and game audio bleed into a room mic can make voice
   sound bad in ways no amount of signaling code fixes. Push to
   talk by default and the builder flags for forcing software
   processing are the mitigations; both are planned, neither is
   proven on our box yet.
3. One of the two households has hostile NAT after all (double
   NAT, an ISP router doing symmetric NAT, or UDP filtering).
   Probability is low, impact is total for that pair, and the
   fix (coturn) is scoped and costed above. We accept the risk
   knowingly by testing the real pair early.

## Recommended v1 scope

1. Kotlin VoiceChat module on io.github.webrtc-sdk:android (Maven Central, BSD 3-Clause), audio only, one PeerConnection per session.
2. Signaling through Go: signal.go modeled on chat.go, bridge pair NetplaySendSignal / NetplaySignalJSON; guest offers, host answers, per PROTOCOL.md 3.1.
3. Voice lives from waiting room formation to session end; text chat untouched.
4. Input: USB mic as the supported path, input enumerated at runtime and named in settings; Bluetooth SCO only if the box exposes it; remote mic documented as Assistant only.
5. RECORD_AUDIO requested lazily on first voice enable; denial degrades to listen plus text.
6. Push to talk by default (hold R3 or L3), ASSIST tap toggles for remote only players, open mic as a setting; mic state shown in waiting room and pause menu.
7. STUN only (public STUN), with ICE state and candidate logging on both boxes from day one.
8. Ship gate: the real Kentucky to Missouri session. coturn on the Ubuntu server is pre scoped as the immediate follow up if, and only if, that test fails.
