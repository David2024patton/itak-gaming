// CD-ROM controller, per the psx-spx CD-ROM drive
// chapter: registers at 0x1F801800-0x1F801803, the
// full documented command set, subchannel Q, and
// CD-DA / XA-ADPCM audio output for the SPU.
//
// Timing model (a model, stated plainly, built from
// the magnitudes in the psx-spx response timings
// chapter):
//
//   - Commands are written instantly. The FIRST
//     response (INT3 ack, or INT5 error) is delivered
//     immediately on the command write.
//   - A command issued while another command's
//     completion (second response) is still pending
//     is queued, one deep, and executed when that
//     completion fires in Advance; this is the
//     doc's busy rule, simplified. Getstat is exempt
//     (the doc has software poll it during seeks).
//   - All completion work is scheduled in CPU cycles
//     and processed by Advance(cycles), which the
//     system calls every CPU step.
//   - Seek delay, in cycles at 33,868,800 Hz:
//     seekDelay = settle + 900 per sector of
//     distance (the distance term capped at
//     6,000,000), where settle is 3,000,000 cycles
//     when the drive starts from a stopped (parked)
//     state, the state after disc load, reset, Stop
//     completion, or Init standby, and 120,000
//     cycles when the drive is already active. So
//     the first sector of a read from a stopped
//     state lands at 3,000,000 + the distance term
//     + one sector period.
//   - ReadN/ReadS: first sector after seekDelay
//     plus exactly one sector period; subsequent
//     sectors one sector period apart. A sector
//     period is 33,868,800/75 = 451,584 cycles at
//     single speed, half that at double speed
//     (Setmode bit 7). Each delivered sector raises
//     INT1 with a stat response byte.
//   - Play: playback starts after seekDelay (the
//     spin-up/settle model above). Pause second
//     response after 5 sector periods when active
//     (the doc's measured average is about that),
//     or 7,666 cycles when already paused. Stop
//     second response after 13,856,714 cycles when
//     spinning (the doc's measured average) or
//     7,547 when stopped. MotorOn second response
//     after the spin-up delay. Init second response
//     after 81,038 cycles. GetID second response
//     after 18,944 cycles. GetQ second response
//     after one sector period. ReadTOC second
//     response after 33,868,800 cycles (the doc
//     says about 1 second). Reset (1Ch) completes
//     silently after 0x400000 cycles (the doc's
//     stated software delay), holding the busy
//     queue for that long.
//   - Forward/Backward scan (a model of the doc's
//     fixed skipping rate): the play position moves
//     10 sectors per sector period, audio muted.
//
// Setfilter simplification (stated per the task):
// when the Setmode XA-filter bit is on, a Mode 2
// sector whose subheader file/channel does not
// match the filter is skipped entirely, for both
// data delivery and XA decode. The doc's two-pass
// data delivery quirk is not modeled.
//
// The interrupt model is unchanged from PS1-2: a
// type is recorded in the flag register when it
// occurs, and OnIRQ fires on the 0-to-1 transition
// of (flag AND enable). Writing the flag register
// acknowledges the written bits; writing bit 6
// resets the parameter/response FIFOs and flags.

package cd

import "fmt"

// IRQ types recorded in the flag register.
const (
	irqDataReady = 1
	irqComplete  = 2
	irqAck       = 3
	irqDataEnd   = 4
	irqError     = 5
)

// Timing model constants, in CPU cycles.
const (
	cpuClockHz          = 33868800
	sectorPeriod1x      = cpuClockHz / 75 // 451,584
	spinUpSettleCycles  = 3000000         // settle/spin-up from a stopped state
	seekBaseCycles      = 120000
	seekPerSectorCycles = 900
	seekTermCapCycles   = 6000000
	pauseSecondIdle     = 7666
	stopSecondSpinning  = 13856714
	stopSecondStopped   = 7547
	initSecondDelay     = 81038
	getIDSecondDelay    = 18944
	readTOCSecondDelay  = cpuClockHz // about 1 second
	resetDoneDelay      = 0x400000
	scanSectorsPerStep  = 10
)

// sectorPeriodCycles returns the current sector
// period for a Setmode byte.
func sectorPeriodCycles(mode uint8) int {
	if mode&0x80 != 0 {
		return sectorPeriod1x / 2
	}
	return sectorPeriod1x
}

// seekDelayCycles is the seek model: a per-sector
// distance term (capped), on top of a 3,000,000
// cycle settle when the drive starts from a
// stopped (parked) state, or a 120,000 cycle base
// when the drive is already active.
func seekDelayCycles(dist int, stopped bool) int {
	if dist < 0 {
		dist = -dist
	}
	term := dist * seekPerSectorCycles
	if term > seekTermCapCycles {
		term = seekTermCapCycles
	}
	if stopped {
		return spinUpSettleCycles + term
	}
	return seekBaseCycles + term
}

// Event kinds for scheduled completion work.
const (
	evSector = iota + 1
	evSeek
	evPlayStart
	evPauseSecond
	evStopSecond
	evMotorSecond
	evInitSecond
	evTOCSecond
	evSessionSecond
	evSessionError
	evSecondResp // precomputed bytes + IRQ (GetID, GetQ)
	evResetDone  // silent completion, busy hold only
)

type cdEvent struct {
	at     int64
	kind   uint8
	irq    uint8
	flag   uint8 // evSeek: 0=SeekL, 1=SeekP
	target int
	resp   []byte
}

// Controller is the CD-ROM controller.
type Controller struct {
	disc Reader

	index    int
	params   []byte
	response []byte
	data     []byte

	irqEnable uint8
	irqFlag   uint8
	irqHot    bool

	mode      uint8
	statError bool
	seekError bool
	idError   bool

	motorOn   bool
	stopped   bool // parked state: disc load, reset, Stop done, Init standby
	seeking   bool
	streaming bool
	playing   bool
	muted     bool

	pendingLBA    int  // Setloc target
	setlocPending bool // Setloc not yet consumed
	nextLBA       int  // next sector to stream
	curLBA        int  // head position (last seek/delivered sector)
	resumeLBA     int  // most recently delivered sector

	filterFile    uint8
	filterChannel uint8
	session       uint8

	// Host-side diagnostic: command bytes rejected
	// as invalid, kept across Reset, never
	// serialized.
	unknownCmds []uint8
	unknownCmdN int

	events []cdEvent
	now    int64

	// Queued command under the busy rule.
	hasQueued    bool
	queuedCmd    uint8
	queuedParams []byte

	// Play (CD-DA) state.
	playLBA      int
	playOff      int // sample frame within the sector, 0..588
	playBuf      []byte
	playBufLBA   int
	playBufValid bool
	scanDir      int // 0, +1 forward, -1 backward
	scanAcc      int

	// XA-ADPCM state.
	xaActive    bool
	xaQueue     [][2]int16
	xaHoldL     int16
	xaHoldR     int16
	xaHoldValid bool
	xaNeedNext  bool
	xaAcc       int
	xaRate      int
	xaStereo    bool
	xaBits8     bool
	xaOldL      int
	xaOlderL    int
	xaOldR      int
	xaOlderR    int
	xaOldM      int
	xaOlderM    int

	// Last delivered sector header/subheader, for
	// GetlocL: amm, ass, asect, mode, file, channel,
	// submode, codinginfo.
	lastHdr [8]byte

	scexTotal   uint8
	scexSuccess uint8

	// OnIRQ, when set, is called on the 0-to-1
	// transition of (irqFlag AND irqEnable).
	OnIRQ func()
}

// NewController returns a controller with no disc.
func NewController() *Controller { return &Controller{xaRate: 37800, stopped: true} }

// SetDisc attaches the disc image sectors are
// streamed from (nil detaches). Attaching a disc
// spins the motor up, as at door close.
func (c *Controller) SetDisc(r Reader) {
	c.disc = r
	if r == nil {
		c.motorOn = false
		c.streaming = false
		c.playing = false
		return
	}
	c.motorOn = true
	c.stopped = true
	c.scexTotal, c.scexSuccess = 0, 0
	for _, t := range r.Tracks() {
		if t.Mode == TrackData {
			c.scexTotal, c.scexSuccess = 1, 1
			break
		}
	}
	c.updateLastHdrSynth(c.curLBA)
}

// Reset returns the controller to power-on state.
// The attached disc is kept.
func (c *Controller) Reset() {
	disc := c.disc
	onIRQ := c.OnIRQ
	unk, unkN := c.unknownCmds, c.unknownCmdN
	*c = Controller{disc: disc, OnIRQ: onIRQ, xaRate: 37800, stopped: true,
		unknownCmds: unk, unknownCmdN: unkN}
	if disc != nil {
		c.SetDisc(disc)
	}
}

// UnknownCommands returns the distinct command bytes
// this controller has rejected as invalid (INT5
// code 40h), in first-seen order. It is a host-side
// diagnostic: the log survives Reset and is not part
// of the serialized state.
func (c *Controller) UnknownCommands() []uint8 {
	out := make([]uint8, len(c.unknownCmds))
	copy(out, c.unknownCmds)
	return out
}

// UnknownCommandCount returns the total number of
// invalid commands received, repeats included.
func (c *Controller) UnknownCommandCount() int { return c.unknownCmdN }

func (c *Controller) noteUnknownCmd(cmd uint8) {
	c.unknownCmdN++
	for _, u := range c.unknownCmds {
		if u == cmd {
			return
		}
	}
	c.unknownCmds = append(c.unknownCmds, cmd)
}

// DataLen returns the number of bytes waiting in the
// data FIFO.
func (c *Controller) DataLen() int { return len(c.data) }

// Streaming reports whether a read stream is active.
func (c *Controller) Streaming() bool { return c.streaming }

// Mode returns the stored Setmode byte.
func (c *Controller) Mode() uint8 { return c.mode }

// statusByte builds the documented stat byte:
//
//	7 Play, 6 Seek, 5 Read (only one of these three)
//	4 ShellOpen, 3 IdError, 2 SeekError,
//	1 Spindle Motor, 0 Error
func (c *Controller) statusByte() uint8 {
	var b uint8
	if c.statError {
		b |= 0x01
	}
	if c.motorOn {
		b |= 0x02
	}
	if c.seekError {
		b |= 0x04
	}
	if c.idError {
		b |= 0x08
	}
	if c.disc == nil {
		b |= 0x10
	}
	switch {
	case c.playing:
		b |= 0x80
	case c.seeking:
		b |= 0x40
	case c.streaming:
		b |= 0x20
	}
	return b
}

// StatusRegister returns the value read from
// 0x1F801800.
func (c *Controller) StatusRegister() uint8 {
	var b uint8 = uint8(c.index & 3)
	if len(c.params) == 0 {
		b |= 0x08 // PRMEMPT: parameter FIFO empty
	}
	if len(c.params) < 16 {
		b |= 0x10 // PRMWRDY: parameter FIFO not full
	}
	if len(c.response) > 0 {
		b |= 0x20 // RSLRRDY: response ready
	}
	if len(c.data) > 0 {
		b |= 0x40 // DRQSTS: data FIFO not empty
	}
	return b
}

func (c *Controller) raiseIRQ(typ uint8) {
	c.irqFlag |= typ
	hot := c.irqFlag&c.irqEnable != 0
	if hot && !c.irqHot && c.OnIRQ != nil {
		c.OnIRQ()
	}
	c.irqHot = hot
}

func (c *Controller) pushResp(bs ...byte) { c.response = append(c.response, bs...) }

// errorResp delivers the documented error form: a
// stat byte with the error bit set, then the error
// code byte, with INT5.
func (c *Controller) errorResp(code uint8) {
	c.pushResp(c.statusByte()|0x01, code)
	c.statError = true
	c.raiseIRQ(irqError)
}

func (c *Controller) ackResp(bs ...byte) {
	c.pushResp(bs...)
	c.raiseIRQ(irqAck)
}

// ReadReg reads one of the four controller
// registers (offset 0-3).
func (c *Controller) ReadReg(off uint32) uint8 {
	switch off & 3 {
	case 0:
		return c.StatusRegister()
	case 1:
		if len(c.response) == 0 {
			return 0
		}
		v := c.response[0]
		c.response = c.response[1:]
		return v
	case 2:
		return c.popDataByte()
	case 3:
		if c.index == 1 {
			return c.irqFlag
		}
		return c.irqEnable
	}
	return 0
}

// WriteReg writes one of the four controller
// registers.
func (c *Controller) WriteReg(off uint32, v uint8) {
	switch off & 3 {
	case 0:
		c.index = int(v & 3)
	case 1:
		if c.index == 0 {
			c.command(v)
		}
	case 2:
		if c.index == 0 && len(c.params) < 16 {
			c.params = append(c.params, v)
		}
	case 3:
		if c.index == 1 {
			if v&0x40 != 0 {
				c.params = nil
				c.response = nil
				c.irqFlag = 0
				c.irqHot = false
			} else {
				c.irqFlag &^= v & 0x1F
				c.irqHot = c.irqFlag&c.irqEnable != 0
			}
		} else {
			c.irqEnable = v & 0x1F
			hot := c.irqFlag&c.irqEnable != 0
			if hot && !c.irqHot && c.OnIRQ != nil {
				c.OnIRQ()
			}
			c.irqHot = hot
		}
	}
}

// Advance processes scheduled completion work for
// the given number of CPU cycles.
func (c *Controller) Advance(cycles int) {
	if cycles <= 0 {
		return
	}
	end := c.now + int64(cycles)
	for i := 0; i < 1000000; i++ {
		idx := -1
		for j := range c.events {
			if c.events[j].at <= end && (idx < 0 || c.events[j].at < c.events[idx].at) {
				idx = j
			}
		}
		if idx < 0 {
			break
		}
		ev := c.events[idx]
		c.events = append(c.events[:idx], c.events[idx+1:]...)
		c.now = ev.at
		c.fireEvent(ev)
		if ev.kind != evSector && c.hasQueued {
			cmd, params := c.queuedCmd, c.queuedParams
			c.hasQueued, c.queuedCmd, c.queuedParams = false, 0, nil
			c.params = params
			c.execCommand(cmd)
		}
	}
	c.now = end
	if c.scanDir != 0 && c.playing {
		c.scanAcc += cycles
		period := sectorPeriodCycles(c.mode)
		for c.scanAcc >= period && c.scanDir != 0 {
			c.scanAcc -= period
			c.scanStep()
		}
	}
}

func (c *Controller) schedule(at int64, ev cdEvent) {
	ev.at = at
	c.events = append(c.events, ev)
}

func (c *Controller) cancelEvents(kinds ...uint8) {
	kept := c.events[:0]
	for _, ev := range c.events {
		drop := false
		for _, k := range kinds {
			if ev.kind == k {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, ev)
		}
	}
	c.events = kept
}

// pendingCompletion reports whether a command's
// second response (or silent completion) is still
// outstanding. Sector deliveries do not count.
func (c *Controller) pendingCompletion() bool {
	for _, ev := range c.events {
		if ev.kind != evSector {
			return true
		}
	}
	return false
}

func (c *Controller) fireEvent(ev cdEvent) {
	switch ev.kind {
	case evSector:
		c.deliverSector()
	case evSeek:
		c.seeking = false
		if ev.flag == 0 {
			if tr, ok := c.trackAt(ev.target); ok && tr.Mode == TrackAudio {
				// SeekL on an audio track: the doc's
				// second response is a seek error and
				// the motor stops.
				c.seekError = true
				c.motorOn = false
				c.pushResp(c.statusByte()|0x04, 0x04)
				c.raiseIRQ(irqError)
				return
			}
		}
		c.curLBA = ev.target
		c.resumeLBA = ev.target
		c.updateLastHdrSynth(ev.target)
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqComplete)
	case evPlayStart:
		c.seeking = false
		c.playing = true
		c.playLBA = ev.target
		c.playOff = 0
		c.playBufValid = false
		c.curLBA = ev.target
		c.resumeLBA = ev.target
	case evPauseSecond, evInitSecond, evTOCSecond, evSessionSecond:
		if ev.kind == evSessionSecond {
			c.session = 1
			c.curLBA = 0
		}
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqComplete)
	case evStopSecond:
		c.motorOn = false
		c.stopped = true
		c.curLBA = 0
		c.playLBA = 0
		c.playOff = 0
		c.playBufValid = false
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqComplete)
	case evMotorSecond:
		c.motorOn = true
		c.stopped = false
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqComplete)
	case evSessionError:
		c.seekError = true
		c.motorOn = false
		c.pushResp(c.statusByte()|0x04, 0x40)
		c.raiseIRQ(irqError)
	case evSecondResp:
		c.pushResp(ev.resp...)
		c.raiseIRQ(ev.irq)
	case evResetDone:
		// Silent: only held the busy queue.
	}
}

func (c *Controller) scanStep() {
	total := c.totalSectors()
	c.playLBA += c.scanDir * scanSectorsPerStep
	c.playOff = 0
	c.playBufValid = false
	if c.scanDir > 0 && c.playLBA >= total {
		c.playLBA = total
		c.scanDir = 0
		c.playing = false
		c.motorOn = false
		c.pushResp(c.statusByte())
		c.raiseIRQ(irqDataEnd)
		return
	}
	if c.scanDir < 0 && c.playLBA <= 0 {
		// Backward switches to Play at the begin of
		// track 1, per the doc.
		c.playLBA = 0
		c.scanDir = 0
	}
	if c.playLBA < 0 {
		c.playLBA = 0
	}
	c.curLBA = c.playLBA
}

// stopActivities aborts streaming, playing and
// scanning, and cancels their scheduled work. The
// motor and the data FIFO are left alone.
func (c *Controller) stopActivities() {
	c.streaming = false
	c.playing = false
	c.seeking = false
	c.scanDir = 0
	c.xaActive = false
	c.cancelEvents(evSector, evPlayStart)
}

func bcdVal(b uint8) int { return int(b>>4)*10 + int(b&0x0F) }

func bcdValid(b uint8) bool { return b>>4 <= 9 && b&0x0F <= 9 }

func toBCD(n int) uint8 {
	if n < 0 {
		n = 0
	}
	if n > 99 {
		n = 99
	}
	return uint8(n/10)<<4 | uint8(n%10)
}

func (c *Controller) tracks() []Track {
	if c.disc == nil {
		return nil
	}
	return c.disc.Tracks()
}

func (c *Controller) trackAt(lba int) (Track, bool) {
	for _, t := range c.tracks() {
		if lba >= t.StartLBA && lba < t.StartLBA+t.Length {
			return t, true
		}
	}
	return Track{}, false
}

func (c *Controller) totalSectors() int {
	total := 0
	for _, t := range c.tracks() {
		if end := t.StartLBA + t.Length; end > total {
			total = end
		}
	}
	return total
}

// headPos is the position GetlocP/GetQ report: the
// play position while playing, else the head.
func (c *Controller) headPos() int {
	if c.playing {
		return c.playLBA
	}
	return c.curLBA
}

func (c *Controller) updateLastHdrSynth(lba int) {
	m, s, f := LBAToMSF(lba)
	modeByte := uint8(0x02)
	if tr, ok := c.trackAt(lba); ok && tr.SectorSize == 2048 && tr.UserOffset == 0 {
		modeByte = 0x01
	}
	c.lastHdr = [8]byte{toBCD(m), toBCD(s), toBCD(f), modeByte, 0, 0, 0x08, 0}
}

// needDiscCmd lists the commands marked E in the
// doc's summary: they answer INT5 with code 80h
// when no disc is inserted.
func needDiscCmd(cmd uint8) bool {
	switch cmd {
	case 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
		0x0B, 0x0C, 0x0D, 0x10, 0x11, 0x13, 0x14, 0x15,
		0x16, 0x1A, 0x1B, 0x1D:
		return true
	}
	return false
}

func (c *Controller) command(cmd uint8) {
	if cmd != 0x01 && c.pendingCompletion() {
		// Busy rule: hold the command until the
		// pending completion fires in Advance.
		if !c.hasQueued {
			c.hasQueued = true
			c.queuedCmd = cmd
			c.queuedParams = append([]byte(nil), c.params...)
			c.params = nil
		}
		return
	}
	c.execCommand(cmd)
}

func (c *Controller) execCommand(cmd uint8) {
	params := c.params
	c.params = nil
	if c.disc == nil && needDiscCmd(cmd) {
		// GetID has its own documented no-disc
		// sequence, below.
		if cmd != 0x1A {
			c.errorResp(0x80)
			return
		}
	}
	switch cmd {
	case 0x01: // Getstat
		c.ackResp(c.statusByte())
	case 0x02: // Setloc
		if len(params) < 3 {
			c.errorResp(0x20)
			return
		}
		if !bcdValid(params[0]) || !bcdValid(params[1]) || !bcdValid(params[2]) ||
			bcdVal(params[1]) > 59 || bcdVal(params[2]) > 74 {
			c.errorResp(0x10)
			return
		}
		lba := MSFToSector(bcdVal(params[0]), bcdVal(params[1]), bcdVal(params[2])) - 150
		if lba < 0 {
			lba = 0
		}
		c.pendingLBA = lba
		c.setlocPending = true
		c.statError = false
		c.ackResp(c.statusByte())
	case 0x03: // Play
		c.cmdPlay(params)
	case 0x04, 0x05: // Forward / Backward
		if !c.playing {
			c.errorResp(0x80)
			return
		}
		if cmd == 0x04 {
			c.scanDir = 1
		} else {
			c.scanDir = -1
		}
		c.ackResp(c.statusByte())
	case 0x06, 0x1B: // ReadN / ReadS
		c.ackResp(c.statusByte())
		c.startRead()
	case 0x07: // MotorOn
		if c.motorOn {
			// The doc: this code normally means wrong
			// parameter count, here it means the
			// motor is already on.
			c.errorResp(0x20)
			return
		}
		c.ackResp(c.statusByte())
		c.schedule(c.now+spinUpSettleCycles, cdEvent{kind: evMotorSecond})
	case 0x08: // Stop
		snap := c.statusByte() &^ 0xE0 // doc: first response already has the read/play/seek bits cleared
		wasSpinning := c.motorOn || c.streaming || c.playing
		c.stopActivities()
		c.ackResp(snap)
		delay := int64(stopSecondStopped)
		if wasSpinning {
			delay = stopSecondSpinning
		}
		c.schedule(c.now+delay, cdEvent{kind: evStopSecond})
	case 0x09: // Pause
		snap := c.statusByte() // doc: first response still shows the old read/play bit
		active := c.streaming || c.playing
		c.stopActivities()
		c.ackResp(snap)
		delay := int64(pauseSecondIdle)
		if active {
			delay = int64(5 * sectorPeriodCycles(c.mode))
		}
		c.schedule(c.now+delay, cdEvent{kind: evPauseSecond})
	case 0x0A: // Init
		snap := c.statusByte()
		c.stopActivities()
		c.mode = 0x20
		c.motorOn = true
		c.stopped = true
		c.statError = false
		c.seekError = false
		c.ackResp(snap)
		c.schedule(c.now+initSecondDelay, cdEvent{kind: evInitSecond})
	case 0x0B: // Mute
		c.muted = true
		c.ackResp(c.statusByte())
	case 0x0C: // Demute
		c.muted = false
		c.ackResp(c.statusByte())
	case 0x0D: // Setfilter
		if len(params) < 2 {
			c.errorResp(0x20)
			return
		}
		c.filterFile, c.filterChannel = params[0], params[1]
		c.ackResp(c.statusByte())
	case 0x0E: // Setmode
		if len(params) < 1 {
			c.errorResp(0x20)
			return
		}
		c.mode = params[0]
		c.ackResp(c.statusByte())
	case 0x0F: // Getparam
		c.ackResp(c.statusByte(), c.mode, 0, c.filterFile, c.filterChannel)
	case 0x10: // GetlocL
		if c.seeking {
			c.errorResp(0x80)
			return
		}
		if tr, ok := c.trackAt(c.curLBA); ok && tr.Mode == TrackAudio {
			c.errorResp(0x80)
			return
		}
		c.ackResp(c.lastHdr[:]...)
	case 0x11: // GetlocP
		c.ackResp(c.positionBytes()...)
	case 0x12: // SetSession
		if len(params) < 1 {
			c.errorResp(0x20)
			return
		}
		if params[0] == 0 {
			c.errorResp(0x10)
			return
		}
		c.ackResp(c.statusByte())
		delay := int64(seekDelayCycles(c.curLBA, c.stopped))
		if params[0] == 1 {
			c.schedule(c.now+delay, cdEvent{kind: evSessionSecond})
		} else {
			// Single-session model: session 2 and up
			// seek-error, per the doc's
			// non-multisession case (simplified to
			// one INT5 second response).
			c.schedule(c.now+delay, cdEvent{kind: evSessionError})
		}
	case 0x13: // GetTN
		trs := c.tracks()
		if len(trs) == 0 {
			c.errorResp(0x80)
			return
		}
		c.ackResp(c.statusByte(), toBCD(trs[0].Number), toBCD(trs[len(trs)-1].Number))
	case 0x14: // GetTD
		if len(params) < 1 {
			c.errorResp(0x20)
			return
		}
		if !bcdValid(params[0]) {
			c.errorResp(0x10)
			return
		}
		n := bcdVal(params[0])
		var lba int
		if n == 0 {
			lba = c.totalSectors() // doc: track 0 is the end of the last track
		} else {
			found := false
			for _, t := range c.tracks() {
				if t.Number == n {
					lba, found = t.DataLBA, true
				}
			}
			if !found {
				c.errorResp(0x10)
				return
			}
		}
		m, s, _ := LBAToMSF(lba)
		c.ackResp(c.statusByte(), toBCD(m), toBCD(s))
	case 0x15, 0x16: // SeekL / SeekP
		snap := c.statusByte()
		target := c.pendingLBA
		c.setlocPending = false
		c.stopActivities()
		c.ackResp(snap)
		delay := seekDelayCycles(target-c.curLBA, c.stopped)
		c.motorOn = true
		c.stopped = false
		c.seeking = true
		c.seekError = false
		flag := uint8(0)
		if cmd == 0x16 {
			flag = 1
		}
		c.schedule(c.now+int64(delay), cdEvent{kind: evSeek, target: target, flag: flag})
	case 0x19: // Test
		c.cmdTest(params)
	case 0x1A: // GetID
		c.cmdGetID()
	case 0x1C: // Reset
		c.stopActivities()
		c.statError = false
		c.seekError = false
		c.ackResp(c.statusByte())
		c.schedule(c.now+resetDoneDelay, cdEvent{kind: evResetDone})
	case 0x1D: // GetQ
		c.cmdGetQ(params)
	case 0x1E: // ReadTOC
		c.ackResp(c.statusByte())
		c.schedule(c.now+readTOCSecondDelay, cdEvent{kind: evTOCSecond})
	default:
		// Includes 00h, 17h, 18h, 1Fh (VideoCD is a
		// SCPH-5903 variant, not this controller),
		// the secret unlock family while locked, and
		// every unused opcode: the doc's answer is
		// INT5 with code 40h, invalid command.
		c.noteUnknownCmd(cmd)
		c.errorResp(0x40)
	}
}

func (c *Controller) cmdPlay(params []byte) {
	target := c.curLBA
	if c.playing {
		target = c.playLBA
	}
	switch {
	case len(params) >= 1 && params[0] != 0:
		if !bcdValid(params[0]) {
			c.errorResp(0x10)
			return
		}
		n := bcdVal(params[0])
		found := false
		for _, t := range c.tracks() {
			if t.Number == n {
				target, found = t.DataLBA, true
			}
		}
		if !found {
			// Doc: parameters past the last track
			// restart the begin of the current track.
			if tr, ok := c.trackAt(target); ok {
				target = tr.DataLBA
			}
		}
	case c.setlocPending:
		target = c.pendingLBA
		c.setlocPending = false
	}
	snap := c.statusByte()
	c.stopActivities()
	c.ackResp(snap)
	delay := seekDelayCycles(target-c.curLBA, c.stopped)
	c.motorOn = true
	c.stopped = false
	c.seeking = true
	c.playLBA = target
	c.playOff = 0
	c.playBufValid = false
	c.schedule(c.now+int64(delay), cdEvent{kind: evPlayStart, target: target})
}

// startRead begins or continues a ReadN/ReadS
// stream, per the doc's Setloc/Read rules.
func (c *Controller) startRead() {
	if c.streaming && !c.setlocPending {
		return // already reading: just continues
	}
	target := c.resumeLBA
	if c.setlocPending {
		target = c.pendingLBA
		c.setlocPending = false
	} else if !c.streaming && c.curLBA != 0 {
		target = c.resumeLBA
	}
	c.playing = false
	c.scanDir = 0
	c.xaActive = false
	c.cancelEvents(evPlayStart)
	c.nextLBA = target
	c.streaming = true
	c.seekError = false
	delay := seekDelayCycles(target-c.curLBA, c.stopped) + sectorPeriodCycles(c.mode)
	c.motorOn = true
	c.stopped = false
	c.seeking = true
	c.cancelEvents(evSector)
	c.schedule(c.now+int64(delay), cdEvent{kind: evSector})
}

func (c *Controller) scheduleNextSector() {
	if c.streaming {
		c.schedule(c.now+int64(sectorPeriodCycles(c.mode)), cdEvent{kind: evSector})
	}
}

// deliverSector runs on an evSector event: one
// sector period of a ReadN/ReadS stream.
func (c *Controller) deliverSector() {
	if !c.streaming || c.disc == nil {
		return
	}
	if c.nextLBA >= c.totalSectors() {
		c.streaming = false
		c.seeking = false
		return
	}
	lba := c.nextLBA
	if c.tryXAOrFilter(lba) {
		// Consumed as XA audio, or skipped by the
		// filter: no data delivery, no INT1.
		c.curLBA = lba
		c.resumeLBA = lba
		c.nextLBA = lba + 1
		c.seeking = false
		c.scheduleNextSector()
		return
	}
	user, err := c.disc.ReadSector(lba, false)
	if err != nil {
		c.streaming = false
		c.seeking = false
		c.pushResp(c.statusByte()|0x01, 0x04)
		c.statError = true
		c.raiseIRQ(irqError)
		return
	}
	data := user
	if c.mode&0x20 != 0 {
		// Setmode sector size bit: whole sector
		// except the sync bytes.
		if raw, rerr := c.disc.ReadSector(lba, true); rerr == nil && len(raw) >= 2352 {
			data = raw[12:]
		}
	}
	if len(c.data)+len(data) <= dataCap {
		c.data = append(c.data, data...)
	}
	c.curLBA = lba
	c.resumeLBA = lba
	c.nextLBA = lba + 1
	c.seeking = false
	c.pushResp(c.statusByte())
	c.raiseIRQ(irqDataReady)
	c.scheduleNextSector()
}

// popDataByte removes one byte from the data FIFO.
// Refills are timing driven (Advance), not drain
// driven, in this model.
func (c *Controller) popDataByte() uint8 {
	if len(c.data) == 0 {
		return 0
	}
	v := c.data[0]
	c.data = c.data[1:]
	return v
}

// ReadWord removes one little-endian word from the
// data FIFO for the CDROM DMA channel. ok is false
// when fewer than 4 bytes are available.
func (c *Controller) ReadWord() (uint32, bool) {
	if len(c.data) < 4 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 4; i++ {
		v |= uint32(c.popDataByte()) << (8 * i)
	}
	return v, true
}

func (c *Controller) cmdGetID() {
	c.ackResp(c.statusByte())
	if c.disc == nil {
		// Doc's no-disc second response.
		resp := []byte{0x08, 0x40, 0, 0, 0, 0, 0, 0}
		c.schedule(c.now+getIDSecondDelay, cdEvent{kind: evSecondResp, resp: resp, irq: irqError})
		c.idError = true
		return
	}
	allAudio := len(c.tracks()) > 0
	for _, t := range c.tracks() {
		if t.Mode != TrackAudio {
			allAudio = false
		}
	}
	if allAudio {
		resp := []byte{0x0A, 0x90, 0, 0, 0, 0, 0, 0}
		c.schedule(c.now+getIDSecondDelay, cdEvent{kind: evSecondResp, resp: resp, irq: irqError})
		c.idError = true
		return
	}
	// Licensed Mode2 data disc model: INT2 with the
	// SCEA region string, matching the Test 22h
	// region reported by this controller.
	resp := []byte{0x02, 0x00, 0x20, 0x00, 'S', 'C', 'E', 'A'}
	c.schedule(c.now+getIDSecondDelay, cdEvent{kind: evSecondResp, resp: resp, irq: irqComplete})
	c.idError = false
}

// positionBytes builds the GetlocP response: track,
// index, track-relative MSF, absolute MSF, all BCD.
func (c *Controller) positionBytes() []byte {
	pos := c.headPos()
	trackNum, index := 0xAA, 1
	rel := pos
	if tr, ok := c.trackAt(pos); ok {
		trackNum = tr.Number
		if pos >= tr.DataLBA {
			rel = pos - tr.DataLBA
			index = 1
		} else {
			rel = pos - tr.StartLBA
			index = 0
		}
	}
	rm, rs, rf := SectorToMSF(rel)
	am, as, af := LBAToMSF(pos)
	return []byte{
		toBCD(trackNum), toBCD(index),
		toBCD(rm), toBCD(rs), toBCD(rf),
		toBCD(am), toBCD(as), toBCD(af),
	}
}

// Serialize layout: a 256-byte header, then bounded
// sections for the parameter FIFO, the response
// FIFO, the data FIFO, the play sector buffer, the
// XA sample queue, and the pending events.
const (
	serHdrSize   = 256
	serParamsCap = 16
	serRespCap   = 512
	dataCap      = 32768
	serPlayCap   = 2352
	xaQueueCap   = 8192 // sample frames
	serEventSize = 80
	serEventCap  = 8

	serParamsOff = serHdrSize
	serRespOff   = serParamsOff + serParamsCap
	serDataOff   = serRespOff + serRespCap
	serPlayOff   = serDataOff + dataCap
	serXAOff     = serPlayOff + serPlayCap
	serEventsOff = serXAOff + xaQueueCap*4
)

// CtrlStateSize is the serialized controller size.
const CtrlStateSize = serEventsOff + serEventCap*serEventSize

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// Serialize writes the controller state (the disc
// itself is not part of it; the system re-attaches
// the same Reader after a restore).
func (c *Controller) Serialize() []byte {
	out := make([]byte, CtrlStateSize)
	out[0] = uint8(c.index)
	out[1] = c.irqEnable
	out[2] = c.irqFlag
	out[3] = c.mode
	out[4] = boolByte(c.irqHot)
	out[5] = boolByte(c.statError)
	out[6] = boolByte(c.streaming)
	out[7] = boolByte(c.seeking)
	out[8] = boolByte(c.playing)
	out[9] = boolByte(c.motorOn)
	out[10] = boolByte(c.muted)
	out[11] = boolByte(c.idError)
	out[12] = boolByte(c.seekError)
	out[13] = boolByte(c.setlocPending)
	out[14] = boolByte(c.xaActive)
	out[15] = boolByte(c.xaHoldValid)
	out[16] = c.filterFile
	out[17] = c.filterChannel
	out[18] = uint8(int8(c.scanDir) + 1)
	out[19] = boolByte(c.hasQueued)
	out[20] = c.queuedCmd
	out[21] = uint8(len(c.queuedParams))
	out[22] = boolByte(c.playBufValid)
	out[23] = boolByte(c.xaNeedNext)
	out[24] = boolByte(c.xaStereo)
	out[25] = boolByte(c.xaBits8)
	out[26] = c.session
	out[27] = c.scexTotal
	out[28] = c.scexSuccess
	out[29] = boolByte(c.stopped)
	copy(out[160:176], c.queuedParams)
	copy(out[144:152], c.lastHdr[:])
	put32(out[32:], uint32(int32(c.pendingLBA)))
	put32(out[36:], uint32(int32(c.nextLBA)))
	put32(out[40:], uint32(int32(c.curLBA)))
	put32(out[44:], uint32(int32(c.resumeLBA)))
	put32(out[48:], uint32(int32(c.playLBA)))
	put32(out[52:], uint32(int32(c.playOff)))
	put32(out[56:], uint32(int32(c.playBufLBA)))
	put32(out[60:], uint32(int32(c.xaAcc)))
	put32(out[64:], uint32(int32(c.xaRate)))
	put32(out[68:], uint32(c.now))
	put32(out[72:], uint32(c.now>>32))
	put32(out[76:], uint32(int32(c.scanAcc)))
	put32(out[80:], uint32(int32(c.xaOldL)))
	put32(out[84:], uint32(int32(c.xaOlderL)))
	put32(out[88:], uint32(int32(c.xaOldR)))
	put32(out[92:], uint32(int32(c.xaOlderR)))
	put32(out[96:], uint32(int32(c.xaOldM)))
	put32(out[100:], uint32(int32(c.xaOlderM)))
	put32(out[104:], uint32(int32(c.xaHoldL)))
	put32(out[108:], uint32(int32(c.xaHoldR)))
	put32(out[112:], uint32(len(c.params)))
	put32(out[116:], uint32(len(c.response)))
	put32(out[120:], uint32(len(c.data)))
	put32(out[124:], uint32(len(c.xaQueue)))
	put32(out[128:], uint32(len(c.playBuf)))
	put32(out[132:], uint32(len(c.events)))
	copy(out[serParamsOff:], c.params)
	copy(out[serRespOff:], c.response)
	copy(out[serDataOff:], c.data)
	copy(out[serPlayOff:], c.playBuf)
	for i, fr := range c.xaQueue {
		put32(out[serXAOff+i*4:], uint32(uint16(fr[0]))|uint32(uint16(fr[1]))<<16)
	}
	for i, ev := range c.events {
		if i >= serEventCap {
			break
		}
		b := out[serEventsOff+i*serEventSize:]
		b[0] = ev.kind
		b[1] = ev.irq
		b[2] = ev.flag
		b[3] = uint8(len(ev.resp))
		put32(b[4:], uint32(int32(ev.target)))
		put32(b[8:], uint32(ev.at-c.now))
		copy(b[16:64], ev.resp)
	}
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *Controller) Deserialize(in []byte) error {
	if len(in) != CtrlStateSize {
		return fmt.Errorf("cd: controller state size %d, want %d", len(in), CtrlStateSize)
	}
	np := int(get32(in[112:]))
	nr := int(get32(in[116:]))
	nd := int(get32(in[120:]))
	nq := int(get32(in[124:]))
	nb := int(get32(in[128:]))
	ne := int(get32(in[132:]))
	if np > serParamsCap || nr > serRespCap || nd > dataCap || nq > xaQueueCap || nb > serPlayCap || ne > serEventCap {
		return fmt.Errorf("cd: controller state has bad lengths %d/%d/%d/%d/%d/%d", np, nr, nd, nq, nb, ne)
	}
	c.index = int(in[0])
	c.irqEnable = in[1]
	c.irqFlag = in[2]
	c.mode = in[3]
	c.irqHot = in[4] != 0
	c.statError = in[5] != 0
	c.streaming = in[6] != 0
	c.seeking = in[7] != 0
	c.playing = in[8] != 0
	c.motorOn = in[9] != 0
	c.muted = in[10] != 0
	c.idError = in[11] != 0
	c.seekError = in[12] != 0
	c.setlocPending = in[13] != 0
	c.xaActive = in[14] != 0
	c.xaHoldValid = in[15] != 0
	c.filterFile = in[16]
	c.filterChannel = in[17]
	c.scanDir = int(int8(in[18])) - 1
	c.hasQueued = in[19] != 0
	c.queuedCmd = in[20]
	qn := int(in[21])
	if qn > serParamsCap {
		return fmt.Errorf("cd: controller state has bad queued param length %d", qn)
	}
	c.queuedParams = append([]byte(nil), in[160:160+qn]...)
	c.playBufValid = in[22] != 0
	c.xaNeedNext = in[23] != 0
	c.xaStereo = in[24] != 0
	c.xaBits8 = in[25] != 0
	c.session = in[26]
	c.scexTotal = in[27]
	c.scexSuccess = in[28]
	c.stopped = in[29] != 0
	copy(c.lastHdr[:], in[144:152])
	c.pendingLBA = int(int32(get32(in[32:])))
	c.nextLBA = int(int32(get32(in[36:])))
	c.curLBA = int(int32(get32(in[40:])))
	c.resumeLBA = int(int32(get32(in[44:])))
	c.playLBA = int(int32(get32(in[48:])))
	c.playOff = int(int32(get32(in[52:])))
	c.playBufLBA = int(int32(get32(in[56:])))
	c.xaAcc = int(int32(get32(in[60:])))
	c.xaRate = int(int32(get32(in[64:])))
	c.now = int64(uint64(get32(in[68:])) | uint64(get32(in[72:]))<<32)
	c.scanAcc = int(int32(get32(in[76:])))
	c.xaOldL = int(int32(get32(in[80:])))
	c.xaOlderL = int(int32(get32(in[84:])))
	c.xaOldR = int(int32(get32(in[88:])))
	c.xaOlderR = int(int32(get32(in[92:])))
	c.xaOldM = int(int32(get32(in[96:])))
	c.xaOlderM = int(int32(get32(in[100:])))
	c.xaHoldL = int16(int32(get32(in[104:])))
	c.xaHoldR = int16(int32(get32(in[108:])))
	c.params = append([]byte(nil), in[serParamsOff:serParamsOff+np]...)
	c.response = append([]byte(nil), in[serRespOff:serRespOff+nr]...)
	c.data = append([]byte(nil), in[serDataOff:serDataOff+nd]...)
	c.playBuf = append([]byte(nil), in[serPlayOff:serPlayOff+nb]...)
	c.xaQueue = nil
	for i := 0; i < nq; i++ {
		v := get32(in[serXAOff+i*4:])
		c.xaQueue = append(c.xaQueue, [2]int16{int16(v), int16(v >> 16)})
	}
	c.events = nil
	for i := 0; i < ne; i++ {
		b := in[serEventsOff+i*serEventSize:]
		rl := int(b[3])
		if rl > 48 {
			return fmt.Errorf("cd: controller state has bad event response length %d", rl)
		}
		c.events = append(c.events, cdEvent{
			kind:   b[0],
			irq:    b[1],
			flag:   b[2],
			target: int(int32(get32(b[4:]))),
			at:     c.now + int64(get32(b[8:])),
			resp:   append([]byte(nil), b[16:16+rl]...),
		})
	}
	return nil
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
