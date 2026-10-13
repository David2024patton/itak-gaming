package kernel

// sceAudio handlers (PSP-4). NIDs and semantics come from the
// published pspsdk tables and the documented behavior of the
// PSP audio driver (see audio.go for the channel model and the
// README for the coverage table, including the families that
// deliberately stay on the unknown-NID path: sceAudioOutput2
// and sceAudioSRCCh share one hardware channel whose documented
// behavior includes arbitrary input sample rates with sample
// rate conversion and a two-descriptor DMA queue, which is not
// the simple documented semantics this slice implements).
//
// Argument and validation orders mirror the documented driver
// behavior the open test literature records: the classic output
// calls check the volume before the channel, reserve checks the
// sample count before the format, and sceAudioSetChannelDataLen
// checks its length before the reservation.

func hleAudioInit(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

func hleAudioEnd(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

func hleAudioSetFrequency(k *Kernel, th *Thread) syscallAction {
	// The documented call accepts 44,100 and 48,000. This
	// core's output stream is fixed at 44,100 Hz (see the
	// README), so the value is validated and accepted and the
	// rate does not change.
	switch k.arg(th, 0) {
	case 44100, 48000:
		k.ret(th, 0)
	default:
		k.ret(th, ErrAudioInvalidFrequency)
	}
	return actNone
}

func hleAudioOutput(k *Kernel, th *Thread) syscallAction {
	vol := int32(k.arg(th, 1))
	if vol > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	k.ret(th, k.audioEnqueue(&k.audioChans[ch], k.arg(th, 2), vol, vol))
	return actNone
}

func hleAudioOutputBlocking(k *Kernel, th *Thread) syscallAction {
	vol := int32(k.arg(th, 1))
	if vol > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	return k.audioOutputBlocking(th, int(ch), k.arg(th, 2), vol, vol)
}

func hleAudioOutputPanned(k *Kernel, th *Thread) syscallAction {
	lv := int32(k.arg(th, 1))
	rv := int32(k.arg(th, 2))
	if lv > 0xFFFF || rv > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	k.ret(th, k.audioEnqueue(&k.audioChans[ch], k.arg(th, 3), lv, rv))
	return actNone
}

func hleAudioOutputPannedBlocking(k *Kernel, th *Thread) syscallAction {
	lv := int32(k.arg(th, 1))
	rv := int32(k.arg(th, 2))
	// The panned blocking call ORs the volume pair before its
	// range check, so a negative volume fails here where the
	// other calls read it as "leave unchanged".
	if uint32(lv|rv) > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	return k.audioOutputBlocking(th, int(ch), k.arg(th, 3), lv, rv)
}

// audioFreeChannel finds a channel for a -1 reserve: the search
// runs downward from channel 7, and a channel counts as free
// only when nothing is reserved into it (sample count 0) and no
// buffer is still playing out.
func (k *Kernel) audioFreeChannel() int {
	for i := audioChannels - 1; i >= 0; i-- {
		if k.audioChans[i].sampleCount == 0 && k.audioChans[i].addr == 0 {
			return i
		}
	}
	return -1
}

func audioValidSampleCount(n uint32) bool {
	return n != 0 && n&63 == 0 && n <= audioSampleMax
}

func audioValidFormat(f uint32) bool {
	return f == audioFormatStereo || f == audioFormatMono
}

func hleAudioChReserve(k *Kernel, th *Thread) syscallAction {
	ch := int32(k.arg(th, 0))
	if ch < 0 {
		ch = int32(k.audioFreeChannel())
		if ch < 0 {
			k.ret(th, ErrAudioNoChannels)
			return actNone
		}
	}
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	c := &k.audioChans[ch]
	if c.reserved {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	sampleCount := k.arg(th, 1)
	if !audioValidSampleCount(sampleCount) {
		k.ret(th, ErrAudioSampleNotAligned)
		return actNone
	}
	format := k.arg(th, 2)
	if !audioValidFormat(format) {
		k.ret(th, ErrAudioInvalidFormat)
		return actNone
	}
	c.reserved = true
	c.sampleCount = sampleCount
	c.format = format
	c.leftVol = 0
	c.rightVol = 0
	k.ret(th, uint32(ch))
	return actNone
}

func hleAudioChRelease(k *Kernel, th *Thread) syscallAction {
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	c := &k.audioChans[ch]
	if !c.reserved {
		k.ret(th, ErrAudioChannelNotReserved)
		return actNone
	}
	if c.waiter != nil {
		k.ret(th, ErrAudioChannelBusy)
		return actNone
	}
	// Only the reservation goes away; a buffer in flight keeps
	// playing (see audio.go).
	c.reserved = false
	c.sampleCount = 0
	k.ret(th, 0)
	return actNone
}

func hleAudioGetChannelRestLen(k *Kernel, th *Thread) syscallAction {
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	k.ret(th, uint32(k.audioRestLen(&k.audioChans[ch])))
	return actNone
}

func hleAudioGetChannelRestLength(k *Kernel, th *Thread) syscallAction {
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	c := &k.audioChans[ch]
	// Unlike RestLen, this one reads zero when no buffer is
	// really playing: after a null-pointer output the two
	// disagree by exactly the recorded rest length.
	rest := int32(0)
	if c.addr != 0 {
		rest = int32(c.remaining)
	}
	if c.waiter != nil {
		rest += int32(c.sampleCount)
	}
	k.ret(th, uint32(rest))
	return actNone
}

func hleAudioSetChannelDataLen(k *Kernel, th *Thread) syscallAction {
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	len := k.arg(th, 1)
	if !audioValidSampleCount(len) {
		k.ret(th, ErrAudioSampleNotAligned)
		return actNone
	}
	c := &k.audioChans[ch]
	if c.waiter != nil {
		k.ret(th, ErrAudioChannelBusy)
		return actNone
	}
	if !c.reserved {
		k.ret(th, ErrAudioChannelNotInit)
		return actNone
	}
	c.sampleCount = len
	k.ret(th, 0)
	return actNone
}

func hleAudioChangeChannelConfig(k *Kernel, th *Thread) syscallAction {
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	c := &k.audioChans[ch]
	if c.waiter != nil || c.addr != 0 {
		k.ret(th, ErrAudioChannelBusy)
		return actNone
	}
	if !c.reserved {
		k.ret(th, ErrAudioChannelNotReserved)
		return actNone
	}
	format := k.arg(th, 1)
	if !audioValidFormat(format) {
		k.ret(th, ErrAudioInvalidFormat)
		return actNone
	}
	c.format = format
	k.ret(th, 0)
	return actNone
}

func hleAudioChangeChannelVolume(k *Kernel, th *Thread) syscallAction {
	lv := int32(k.arg(th, 1))
	rv := int32(k.arg(th, 2))
	if lv > 0xFFFF || rv > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := k.arg(th, 0)
	if ch >= audioChannels {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	// No reservation check: the volumes land on the channel
	// either way, and a negative side leaves that side alone.
	c := &k.audioChans[ch]
	if lv >= 0 {
		c.leftVol = lv
	}
	if rv >= 0 {
		c.rightVol = rv
	}
	k.ret(th, 0)
	return actNone
}

func hleAudioOneshotOutput(k *Kernel, th *Thread) syscallAction {
	// (ch, sampleCount, format, leftvol, rightvol, buf);
	// the last two arguments ride the stack.
	lv := int32(k.arg(th, 3))
	rv := int32(k.stackArg(th, 4))
	buf := k.stackArg(th, 5)
	if uint32(lv) > 0xFFFF || uint32(rv) > 0xFFFF {
		k.ret(th, ErrAudioInvalidVolume)
		return actNone
	}
	ch := int32(k.arg(th, 0))
	if ch < 0 {
		ch = int32(k.audioFreeChannel())
		if ch < 0 {
			k.ret(th, ErrAudioNoChannels)
			return actNone
		}
	} else if ch >= audioChannels || k.audioChans[ch].reserved {
		k.ret(th, ErrAudioInvalidChannel)
		return actNone
	}
	sampleCount := int32(k.arg(th, 1))
	if sampleCount <= 0 {
		k.ret(th, ErrAudioSampleNotAligned)
		return actNone
	}
	format := k.arg(th, 2)
	if !audioValidFormat(format) {
		k.ret(th, ErrAudioInvalidFormat)
		return actNone
	}
	// No reservation and no busy check: a second oneshot
	// replaces the buffer in flight. The channel's sample
	// count stays 0, which is what frees the channel for a
	// -1 reserve once this buffer runs out.
	c := &k.audioChans[ch]
	c.format = format
	c.leftVol = lv
	c.rightVol = rv
	c.remaining = uint32(sampleCount)
	c.addr = buf
	k.ret(th, uint32(ch))
	return actNone
}

// --- sceAudioInput: the documented silence stub -----------------
//
// This core has no microphone source. The published semantics
// of the input calls are a sample count of mono 16-bit input;
// with no device attached the honest model is a silent one:
// init succeeds, reads fill the buffer with zeros and report
// every requested sample delivered, and nothing ever pends.

func hleAudioInputInit(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

func hleAudioInput(k *Kernel, th *Thread) syscallAction {
	samples := k.arg(th, 0)
	buf := k.arg(th, 2)
	if buf == 0 {
		k.ret(th, 0xFFFFFFFF)
		return actNone
	}
	for i := uint32(0); i < samples; i++ {
		k.mem.Write16(buf+2*i, 0)
	}
	k.ret(th, samples)
	return actNone
}

func hleAudioGetInputLength(k *Kernel, th *Thread) syscallAction {
	k.ret(th, 0)
	return actNone
}

// --- sceAudioRouting: stored mode pair --------------------------

func hleAudioRoutingSetMode(k *Kernel, th *Thread) syscallAction {
	prev := k.audioRoutingMode
	k.audioRoutingMode = int32(k.arg(th, 0))
	k.ret(th, uint32(prev))
	return actNone
}

func hleAudioRoutingGetMode(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(k.audioRoutingMode))
	return actNone
}

func hleAudioRoutingSetVolumeMode(k *Kernel, th *Thread) syscallAction {
	prev := k.audioRoutingVolMode
	k.audioRoutingVolMode = int32(k.arg(th, 0))
	k.ret(th, uint32(prev))
	return actNone
}

func hleAudioRoutingGetVolumeMode(k *Kernel, th *Thread) syscallAction {
	k.ret(th, uint32(k.audioRoutingVolMode))
	return actNone
}
