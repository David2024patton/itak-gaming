package kernel

// The sceAudio output engine (PSP-4). The model follows the
// published documentation for the PSP audio driver (the pspsdk
// pspaudio.h constants and the behavior the open PSP test
// literature records):
//
//   - 8 hardware channels. A channel is reserved with a fixed
//     per-call sample count (the buffer length, 64..65472 frames,
//     a multiple of 64) and a format (stereo 0, mono 0x10).
//   - One buffer per channel, no queue behind it. An output call
//     hands the channel a guest buffer address; the mixer reads
//     the samples straight out of guest memory as they are
//     consumed, it never copies on the way in. A null buffer
//     pointer is accepted: the channel records a full buffer of
//     remaining samples but plays nothing, and a later output
//     call succeeds immediately against it.
//   - A non-blocking output on a channel with a buffer in flight
//     fails with SCE_ERROR_AUDIO_CHANNEL_BUSY. A blocking output
//     parks the caller instead (one parked thread per channel; a
//     second blocking caller is turned away BUSY). When the
//     playing buffer runs out, the parked caller's buffer starts
//     playing and the caller wakes with the channel sample count
//     as its result, the same value a successful immediate
//     enqueue returns.
//   - Volumes are per call (panned or single). A negative volume
//     leaves that side unchanged, except in
//     sceAudioOutputPannedBlocking, where the documented check
//     ORs the pair so a negative fails as an invalid volume.
//     Unity gain is 0x8000; mixing applies (sample * vol) >> 15
//     per channel into a wide accumulator and clamps once to
//     int16 at the end.
//   - sceAudioChRelease clears only the reservation: a buffer
//     already in flight keeps playing to the end.
//
// Consumption runs on the kernel cycle clock at exactly 44,100
// stereo frames per second: frames due at a cycle count C is
// floor(C * 44100 / 222 MHz), a pure function of the sysclock,
// so mixing is deterministic and save states resume the stream
// at the exact frame. Mixing itself is lazy: it advances
// whenever the scheduler looks for expired waits (every
// scheduling decision and every timeslice check) and when the
// host drains the output. A guest whose only thread blocks on
// audio output forever cannot deadlock the machine: the parked
// thread's wait deadline is the cycle its channel's buffer runs
// out, so the scheduler fast-forward reaches it, the buffer
// completes, the parked buffer starts, and the cycle repeats
// inside the frame budget.

const (
	audioChannels     = 8
	audioRate         = 44100
	audioVolumeMax    = 0x8000
	audioSampleMin    = 64
	audioSampleMax    = 65472
	audioFormatStereo = 0
	audioFormatMono   = 0x10
	// audioOutCap bounds the undrained output ring, in stereo
	// frames. At the 735 frames a frame budget produces, the cap
	// is over ten seconds of audio; samples past it are dropped
	// (the host is expected to drain every frame).
	audioOutCap = 8192
)

// audioChan is one hardware channel's state.
type audioChan struct {
	reserved    bool
	sampleCount uint32 // frames per output call; 0 after release (and on oneshot-only channels)
	format      uint32
	leftVol     int32
	rightVol    int32

	addr      uint32 // guest address of the playing buffer; 0 = nothing playing
	remaining uint32 // frames left in the playing buffer

	waiter       *Thread // the one parked blocking caller, if any
	waitAddr     uint32
	waitLeftVol  int32
	waitRightVol int32
}

// audioFramesDue is the number of output frames the stream has
// produced by the given kernel cycle count.
func audioFramesDue(cycles uint64) uint64 {
	return cycles * audioRate / ClockHz
}

// audioCycleForFrame is the first kernel cycle count at which
// output frame f has been produced.
func audioCycleForFrame(f uint64) uint64 {
	return (f*ClockHz + audioRate - 1) / audioRate
}

func audioApplyVolume(sample int16, vol int32) int64 {
	return int64(sample) * int64(vol) >> 15
}

func audioClamp(v int64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// audioAdvance mixes every output frame the cycle clock has
// reached since the last advance, appending stereo int16 frames
// to the output ring and completing channel buffers (which can
// wake parked blocking callers).
func (k *Kernel) audioAdvance() {
	due := audioFramesDue(k.cycles)
	for k.audioMixed < due {
		var accL, accR int64
		for i := range k.audioChans {
			c := &k.audioChans[i]
			if c.addr == 0 || c.remaining == 0 {
				continue
			}
			if c.format == audioFormatMono {
				s := int16(k.mem.Read16(c.addr))
				accL += audioApplyVolume(s, c.leftVol)
				accR += audioApplyVolume(s, c.rightVol)
				c.addr += 2
			} else {
				sl := int16(k.mem.Read16(c.addr))
				sr := int16(k.mem.Read16(c.addr + 2))
				accL += audioApplyVolume(sl, c.leftVol)
				accR += audioApplyVolume(sr, c.rightVol)
				c.addr += 4
			}
			c.remaining--
			if c.remaining == 0 {
				k.audioFinishChannel(i)
			}
		}
		if len(k.audioOut)/2 < audioOutCap {
			k.audioOut = append(k.audioOut, audioClamp(accL), audioClamp(accR))
		}
		k.audioMixed++
	}
}

// audioFinishChannel retires a channel's spent buffer: the
// channel goes idle, and a parked blocking caller's buffer takes
// its place, waking the caller with the sample count.
func (k *Kernel) audioFinishChannel(i int) {
	c := &k.audioChans[i]
	c.addr = 0
	c.remaining = 0
	w := c.waiter
	if w == nil {
		return
	}
	addr, lv, rv := c.waitAddr, c.waitLeftVol, c.waitRightVol
	c.waiter = nil
	if w.state != thWait || w.wait != waitAudio {
		return // the waiter left on its own (deleted or cancelled)
	}
	// Enqueue the parked buffer under the same rules as a fresh
	// output call: a null address leaves the channel idle but
	// holding a buffer's worth of rest length.
	c.remaining = c.sampleCount
	if lv >= 0 {
		c.leftVol = lv
	}
	if rv >= 0 {
		c.rightVol = rv
	}
	c.addr = addr
	k.completeWait(w, int32(c.sampleCount))
	k.maybePreempt(w)
}

// audioEnqueue hands a buffer to a channel under the one-buffer
// model and returns the value the output calls return: the
// channel sample count, or an SCE_ERROR_AUDIO code.
func (k *Kernel) audioEnqueue(c *audioChan, addr uint32, lv, rv int32) uint32 {
	if !c.reserved {
		return ErrAudioChannelNotInit
	}
	if c.addr != 0 {
		return ErrAudioChannelBusy
	}
	c.remaining = c.sampleCount
	if lv >= 0 {
		c.leftVol = lv
	}
	if rv >= 0 {
		c.rightVol = rv
	}
	c.addr = addr
	return c.sampleCount
}

// audioOutputBlocking is the shared body of the two blocking
// output calls: enqueue, or park the caller until the playing
// buffer runs out.
func (k *Kernel) audioOutputBlocking(th *Thread, idx int, addr uint32, lv, rv int32) syscallAction {
	c := &k.audioChans[idx]
	if res := k.audioEnqueue(c, addr, lv, rv); res != ErrAudioChannelBusy {
		k.ret(th, res)
		return actNone
	}
	if c.waiter != nil {
		k.ret(th, ErrAudioChannelBusy)
		return actNone
	}
	c.waiter = th
	c.waitAddr = addr
	c.waitLeftVol = lv
	c.waitRightVol = rv
	// The deadline is the cycle at which the playing buffer's
	// last frame is produced; the scheduler fast-forward lands
	// there and audioAdvance completes the wait with the real
	// result (the deadline itself never delivers a timeout:
	// expireWaits has no timeout case for waitAudio).
	finishFrame := k.audioMixed + uint64(c.remaining)
	return k.blockCurrent(th, waitAudio, uint32(idx), audioCycleForFrame(finishFrame))
}

// audioRestLen is sceAudioGetChannelRestLen: frames left in the
// playing buffer, plus a full buffer while a blocking caller is
// parked (its buffer counts as queued behind the playing one).
func (k *Kernel) audioRestLen(c *audioChan) int32 {
	rest := int32(c.remaining)
	if c.waiter != nil {
		rest += int32(c.sampleCount)
	}
	return rest
}

// DrainAudio advances mixing to the current kernel cycle count
// and returns every mixed-but-undrained sample (stereo int16)
// in stream order, clearing the ring.
func (k *Kernel) DrainAudio() []int16 {
	k.audioAdvance()
	out := k.audioOut
	k.audioOut = nil
	return out
}

// AudioMixedFrames returns the total number of output frames
// mixed since boot (735 per frame budget), for hosts and gates
// that account for the stream.
func (k *Kernel) AudioMixedFrames() uint64 { return k.audioMixed }
