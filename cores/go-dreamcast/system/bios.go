package system

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// BIOS HLE for DC-4. There is no Sega BIOS in this core and
// there never will be; the boot ROM window keeps its DC-1 NOP
// sled plus one page of handler stubs written by us (bus
// physical 0x800, eight stubs of eight bytes: RTS, NOP, NOP,
// NOP). BootDisc points the documented syscall vector slots in
// low RAM at those stubs, and the machine intercepts execution
// whenever the program counter reaches a stub, services the
// call in Go, and returns through PR. The stub's own RTS is a
// safety net for execution arriving by any other path.
//
// The vector layout and calling conventions follow the public
// Dreamcast development documentation of the BIOS syscall
// interface (the same interface Flycast's reios and KallistiOS
// implement):
//
//	0x8C0000B0  system / sysinfo calls, function in R7
//	0x8C0000B4  ROM font calls, function in R1
//	0x8C0000B8  flashrom calls, function in R7
//	0x8C0000BC  GD-ROM calls (R6 = 0) and misc calls (R6 = -1),
//	            function in R7
//	0x8C0000C0  GD-ROM calls, alternate entry (same handler)
//	0x8C0000E0  system boot calls, function in R4
//	0x8C0010F0  GD-ROM direct entry (same handler as 0xBC)
//
// Handlers preserve every register except R0 (the return value)
// and memory they are documented to write; a call is charged one
// step in the machine's flat cycle model. Anything outside the
// implemented subset is recorded (see UnimplementedBIOSCalls)
// and returns 0xFFFFFFFF, the documented error shape; it never
// crashes the machine.
//
// The GD-ROM subset is the documented asynchronous request
// model, executed synchronously at EXEC_SERVER time:
//
//	functions (R7, R6 = 0):
//	  0 REQ_CMD      R4 = command, R5 = pointer to 4 parameter
//	                 words; returns a request id, or 0 while a
//	                 request is still busy
//	  1 GET_CMD_STAT R4 = request id, R5 = pointer to a status
//	                 array; returns the status (0 busy, 1
//	                 complete, 2 error) and writes the status
//	                 word followed by the first three result
//	                 words into the array
//	  2 EXEC_SERVER  executes the queued request now
//	  3 INIT_SYSTEM  (re)initializes the GD system, drops any
//	                 pending request; returns 0
//	  4 GET_DRV_STAT R4 = pointer to 4 words; writes the drive
//	                 status and the disc format
//	  7 RESET        drops any pending request; returns 0
//
//	commands (the R4 of REQ_CMD):
//	 16 PIOREAD      params FAD, sector count, buffer, 0
//	 17 DMAREAD      same layout as PIOREAD in this HLE
//	 19 GETTOC2      params session, buffer, 0, 0
//	 20 PLAY         completes at once (no CD audio model)
//	 21 PLAY2        completes at once
//	 22 PAUSE        completes at once
//	 23 RELEASE      completes at once
//	 24 INIT         initializes the drive; completes
//	 29 GET_VERSION  params buffer, 0, 0, 0; writes this HLE's
//	                 own version banner, NUL padded to 32 bytes
//	 30 GETSCD       params buffer, format, 0, 0; writes a zeroed
//	                 96 byte subcode block (data disc, stopped)
//
// Sector addresses are FADs: the absolute LBA plus 150, exactly
// as the hardware GD interface numbers them. A read result
// word 0 is the number of bytes transferred.
//
// The misc interface (R6 = -1) implements function 0 (INIT,
// returns 0); every other misc function is logged. SYSINFO
// implements function 0 (init, returns 0) and function 2 (reads
// the 24 byte console id block, zeroed here because no flash
// sysinfo partition is modeled). The system boot vector
// implements function 0 (exit to the BIOS menu: the CPU halts;
// there is no menu, so the machine idles on its last frame).
// The font and flashrom vectors are logged stubs: the public
// boot documentation does not put them on the IP boot path.

// BIOS stub ids in the handler stub page (bus physical 0x800,
// eight bytes per stub). The vector slots in RAM hold the P2
// (uncached) aliases, the way the real BIOS is reached.
const (
	stubGD       = 0 // GD-ROM and misc dispatcher
	stubSysinfo  = 1
	stubFont     = 2
	stubFlash    = 3
	stubSystem   = 4
	stubGDEntry  = 5 // GD-ROM direct entry slot (0x8C0010F0)
	stubCatchAll = 6
	stubCount    = 8

	biosStubPhysBase = 0x800
)

// biosStubAddr returns the P2 virtual address of a stub.
func biosStubAddr(id int) uint32 {
	return 0xA0000000 | uint32(biosStubPhysBase+id*8)
}

// GD-ROM function numbers (R7 with R6 = 0).
const (
	gdFuncReqCmd     = 0
	gdFuncGetCmdStat = 1
	gdFuncExecServer = 2
	gdFuncInitSystem = 3
	gdFuncGetDrvStat = 4
	gdFuncReset      = 7
)

// GD-ROM command numbers (the R4 of REQ_CMD).
const (
	gdCmdPIORead    = 16
	gdCmdDMARead    = 17
	gdCmdGetTOC     = 18
	gdCmdGetTOC2    = 19
	gdCmdPlay       = 20
	gdCmdPlay2      = 21
	gdCmdPause      = 22
	gdCmdRelease    = 23
	gdCmdInit       = 24
	gdCmdGetVersion = 29
	gdCmdGetSCD     = 30
)

// GD request status values, returned by GET_CMD_STAT.
const (
	gdStatBusy     = 0
	gdStatComplete = 1
	gdStatError    = 2
)

// gdMaxReadSectors bounds one read request; a real request
// never approaches it, and the bound keeps a garbage parameter
// block from spinning the HLE.
const gdMaxReadSectors = 8192

// gdRequest is the one outstanding asynchronous GD request.
type gdRequest struct {
	active bool
	id     uint32
	cmd    int32
	params [4]uint32
	status int32
	result [4]uint32
}

// biosState is the HLE BIOS runtime state. It is part of the
// save state (version 4) so a restore mid-boot keeps the GD
// request queue and the diagnostics.
type biosState struct {
	booted     bool
	gdInit     bool
	exitToMenu bool
	nextID     uint32
	req        gdRequest
	unimpl     []BIOSCall
	unimplOver int
}

// BIOSCall records one unimplemented BIOS call shape: the
// vector it arrived on, the function number, and how many
// times it was hit.
type BIOSCall struct {
	Vector   string
	Function int32
	Count    int
}

// maxBIOSUnimpl caps the recorded unimplemented call shapes.
const maxBIOSUnimpl = 8

// Vector ids for the serialized form of a BIOSCall.
const (
	biosVecGD = iota
	biosVecGDCmd
	biosVecSysinfo
	biosVecFont
	biosVecFlash
	biosVecSystem
	biosVecMisc
	biosVecStub
)

var biosVecNames = []string{
	"GD 0x8C0000BC", "GD command", "SYSINFO 0x8C0000B0",
	"FONT 0x8C0000B4", "FLASHROM 0x8C0000B8", "SYSTEM 0x8C0000E0",
	"GD misc (R6=-1)", "BIOS stub",
}

func biosVecID(name string) int {
	for i, n := range biosVecNames {
		if n == name {
			return i
		}
	}
	return biosVecStub
}

// UnimplementedBIOSCalls returns a copy of the recorded
// unimplemented BIOS call shapes, in the order first seen.
func (m *Machine) UnimplementedBIOSCalls() []BIOSCall {
	out := make([]BIOSCall, len(m.bios.unimpl))
	copy(out, m.bios.unimpl)
	return out
}

// BIOSDiagnostics is the unload-time summary of the BIOS HLE:
// whether the machine booted, and every unimplemented call
// shape it met. The libretro shell logs it at unload, the same
// pattern as the PS1 core.
type BIOSDiagnostics struct {
	Booted   bool
	Calls    []BIOSCall
	Overflow int
}

// BIOSDiagnostics returns the machine's BIOS HLE diagnostics.
func (m *Machine) BIOSDiagnostics() BIOSDiagnostics {
	return BIOSDiagnostics{
		Booted:   m.bios.booted,
		Calls:    m.UnimplementedBIOSCalls(),
		Overflow: m.bios.unimplOver,
	}
}

// String renders the diagnostics summary.
func (d BIOSDiagnostics) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "GoDC BIOS HLE: booted=%v", d.Booted)
	if len(d.Calls) == 0 {
		sb.WriteString(", no unimplemented BIOS calls")
		return sb.String()
	}
	sb.WriteString(", unimplemented BIOS calls:")
	for _, c := range d.Calls {
		fmt.Fprintf(&sb, " [%s fn %d x%d]", c.Vector, c.Function, c.Count)
	}
	if d.Overflow > 0 {
		fmt.Fprintf(&sb, " (+%d more shapes)", d.Overflow)
	}
	return sb.String()
}

// Booted reports whether BootDisc has run on this machine.
func (m *Machine) Booted() bool { return m.bios.booted }

// recordUnimpl notes an unimplemented BIOS call shape.
func (m *Machine) recordUnimpl(vecID int, fn int32) {
	name := biosVecNames[vecID]
	for i := range m.bios.unimpl {
		if m.bios.unimpl[i].Vector == name && m.bios.unimpl[i].Function == fn {
			m.bios.unimpl[i].Count++
			return
		}
	}
	if len(m.bios.unimpl) >= maxBIOSUnimpl {
		m.bios.unimplOver++
		return
	}
	m.bios.unimpl = append(m.bios.unimpl, BIOSCall{Vector: name, Function: fn, Count: 1})
}

// stepBIOS services a BIOS syscall if the CPU is sitting on a
// handler stub, returning true when it did. The return is the
// documented shape of a BIOS call: PC becomes PR.
func (m *Machine) stepBIOS() bool {
	pc := m.CPU.PC
	switch pc >> 29 {
	case 0, 4, 5: // P0 (MMU off), P1, P2
	default:
		return false
	}
	phys := pc & 0x1FFFFFFF
	if phys < biosStubPhysBase || phys >= biosStubPhysBase+stubCount*8 {
		return false
	}
	off := phys - biosStubPhysBase
	if off&7 != 0 {
		return false
	}
	m.execBIOSStub(int(off >> 3))
	m.CPU.PC = m.CPU.PR
	return true
}

// execBIOSStub runs one stub's handler with the CPU registers
// as its arguments.
func (m *Machine) execBIOSStub(id int) {
	switch id {
	case stubGD, stubGDEntry:
		m.gdDispatch()
	case stubSysinfo:
		m.sysinfoDispatch()
	case stubFont:
		m.recordUnimpl(biosVecFont, int32(m.CPU.R[1]))
		m.CPU.R[0] = 0xFFFFFFFF
	case stubFlash:
		m.recordUnimpl(biosVecFlash, int32(m.CPU.R[7]))
		m.CPU.R[0] = 0xFFFFFFFF
	case stubSystem:
		m.systemDispatch()
	default:
		m.recordUnimpl(biosVecStub, int32(id))
		m.CPU.R[0] = 0xFFFFFFFF
	}
}

// gdDispatch routes the GD-ROM / misc vector by R6 and R7.
func (m *Machine) gdDispatch() {
	fn := int32(m.CPU.R[7])
	switch sel := int32(m.CPU.R[6]); {
	case sel == -1:
		// The misc interface. Function 0 is INIT.
		if fn == 0 {
			m.CPU.R[0] = 0
			return
		}
		m.recordUnimpl(biosVecMisc, fn)
		m.CPU.R[0] = 0xFFFFFFFF
	case sel == 0:
		switch fn {
		case gdFuncReqCmd:
			m.gdReqCmd()
		case gdFuncGetCmdStat:
			m.gdGetCmdStat()
		case gdFuncExecServer:
			m.gdExecServer()
		case gdFuncInitSystem:
			m.bios.gdInit = true
			m.bios.req = gdRequest{}
			m.CPU.R[0] = 0
		case gdFuncGetDrvStat:
			m.gdGetDrvStat()
		case gdFuncReset:
			m.bios.req = gdRequest{}
			m.CPU.R[0] = 0
		default:
			// Known but unimplemented functions (G1 DMA end,
			// read abort, change data type, set PIO callback)
			// and every unknown function land here.
			m.recordUnimpl(biosVecGD, fn)
			m.CPU.R[0] = gdStatError
		}
	default:
		m.recordUnimpl(biosVecGD, fn)
		m.CPU.R[0] = 0xFFFFFFFF
	}
}

// guestPA translates a guest virtual address to the
// physical address the bus routes on. The machine runs with
// the MMU off, so the P0, P1, and P2 areas all mask down to
// the physical address.
func guestPA(va uint32) uint32 { return va & 0x1FFFFFFF }

// gdReqCmd queues a command from the parameter block at R5.
func (m *Machine) gdReqCmd() {
	if m.bios.req.active && m.bios.req.status == gdStatBusy {
		m.CPU.R[0] = 0
		return
	}
	ptr := guestPA(m.CPU.R[5])
	var params [4]uint32
	for i := range params {
		params[i] = m.Bus.Read32(ptr + uint32(4*i))
	}
	m.bios.nextID++
	m.bios.req = gdRequest{
		active: true,
		id:     m.bios.nextID,
		cmd:    int32(m.CPU.R[4]),
		params: params,
		status: gdStatBusy,
	}
	m.CPU.R[0] = m.bios.req.id
}

// gdExecServer executes the queued request synchronously.
func (m *Machine) gdExecServer() {
	m.CPU.R[0] = 0
	req := &m.bios.req
	if !req.active || req.status != gdStatBusy {
		return
	}
	req.status, req.result = m.execGDCommand(req.cmd, req.params)
}

// gdGetCmdStat reports the queued request's status.
func (m *Machine) gdGetCmdStat() {
	id := m.CPU.R[4]
	ptr := guestPA(m.CPU.R[5])
	req := &m.bios.req
	if !req.active || req.id != id {
		if ptr != 0 {
			m.Bus.Write32(ptr, gdStatError)
		}
		m.CPU.R[0] = gdStatError
		return
	}
	if ptr != 0 {
		m.Bus.Write32(ptr, uint32(req.status))
		for i := 0; i < 3; i++ {
			m.Bus.Write32(ptr+uint32(4*(i+1)), req.result[i])
		}
	}
	m.CPU.R[0] = uint32(req.status)
}

// gdGetDrvStat writes the drive status block at R4: the drive
// status (2 standby when a disc is present, 7 empty when not,
// in the published drive status numbering), the disc format
// (0 GD-ROM), and two zero words.
func (m *Machine) gdGetDrvStat() {
	ptr := guestPA(m.CPU.R[4])
	stat, format := uint32(7), uint32(0)
	if m.Disc != nil {
		stat = 2
	}
	m.Bus.Write32(ptr, stat)
	m.Bus.Write32(ptr+4, format)
	m.Bus.Write32(ptr+8, 0)
	m.Bus.Write32(ptr+12, 0)
	m.CPU.R[0] = 0
}

// execGDCommand runs one GD command now.
func (m *Machine) execGDCommand(cmd int32, params [4]uint32) (int32, [4]uint32) {
	var zero [4]uint32
	switch cmd {
	case gdCmdInit:
		m.bios.gdInit = true
		return gdStatComplete, zero
	case gdCmdPIORead, gdCmdDMARead:
		n, ok := m.gdReadSectors(params[0], params[1], params[2])
		if !ok {
			return gdStatError, zero
		}
		return gdStatComplete, [4]uint32{n, 0, 0, 0}
	case gdCmdGetTOC2:
		if !m.gdWriteTOC(params[0], params[1]) {
			return gdStatError, zero
		}
		return gdStatComplete, zero
	case gdCmdGetVersion:
		// This HLE's own version banner; not a Sega string.
		banner := "ITAK GD HLE 1.000"
		buf := make([]byte, 32)
		copy(buf, banner)
		m.writeGuest(params[0], buf)
		return gdStatComplete, zero
	case gdCmdGetSCD:
		// A zeroed 96 byte subcode block: a data disc at rest.
		m.writeGuest(params[0], make([]byte, 96))
		return gdStatComplete, zero
	case gdCmdPlay, gdCmdPlay2, gdCmdPause, gdCmdRelease:
		// CD audio is not modeled; the commands complete.
		return gdStatComplete, zero
	default:
		m.recordUnimpl(biosVecGDCmd, cmd)
		return gdStatError, zero
	}
}

// gdReadSectors reads count 2048 byte sectors starting at the
// given FAD into guest RAM at buf. It returns the byte count.
func (m *Machine) gdReadSectors(fad, count, buf uint32) (uint32, bool) {
	if m.Disc == nil || count > gdMaxReadSectors {
		return 0, false
	}
	lba := int(fad) - 150
	var total uint32
	for i := uint32(0); i < count; i++ {
		sec, err := m.Disc.ReadSector(lba + int(i))
		if err != nil {
			return total, false
		}
		m.writeGuest(buf+total, sec)
		total += uint32(len(sec))
	}
	return total, true
}

// gdSessions derives the session start FADs of the loaded
// disc: a session starts at the first track and at every data
// track that follows an audio track, the standard two session
// GD layout (and the single session MIL-CD layout).
func (m *Machine) gdSessions() []uint32 {
	var starts []uint32
	prevAudio := false
	for i, tr := range m.Disc.Tracks {
		isData := tr.Ctrl&4 != 0
		if i == 0 || (isData && prevAudio) {
			starts = append(starts, uint32(tr.LBA)+150)
		}
		prevAudio = !isData
	}
	return starts
}

// gdWriteTOC writes the TOC table for GETTOC2 at buf:
//
//	word 0        number of sessions
//	word 1        number of tracks
//	words 2..     per session: start FAD, end FAD
//	then          per track: FAD, control byte value
//
// The session parameter selects nothing in this HLE beyond a
// range check; the full table is always written.
func (m *Machine) gdWriteTOC(session, buf uint32) bool {
	if m.Disc == nil {
		return false
	}
	starts := m.gdSessions()
	if int(session) > len(starts) {
		return false
	}
	tracks := m.Disc.Tracks
	// Disc end FAD: the last track's end LBA plus 150.
	endFAD := uint32(0)
	if n := len(tracks); n > 0 {
		last := tracks[n-1]
		endFAD = uint32(last.LBA+int(last.Length)/last.SectorSize) + 150
	}
	words := []uint32{uint32(len(starts)), uint32(len(tracks))}
	for i, s := range starts {
		end := endFAD
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		words = append(words, s, end)
	}
	for _, tr := range tracks {
		words = append(words, uint32(tr.LBA)+150, uint32(tr.Ctrl))
	}
	data := make([]byte, 4*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint32(data[4*i:], w)
	}
	m.writeGuest(buf, data)
	return true
}

// writeGuest copies bytes into guest RAM through the bus.
func (m *Machine) writeGuest(va uint32, data []byte) {
	pa := guestPA(va)
	for i := 0; i+3 < len(data); i += 4 {
		m.Bus.Write32(pa+uint32(i), binary.LittleEndian.Uint32(data[i:]))
	}
	for i := len(data) &^ 3; i < len(data); i++ {
		m.Bus.Write8(pa+uint32(i), data[i])
	}
}

// sysinfoDispatch routes the system / sysinfo vector by R7.
func (m *Machine) sysinfoDispatch() {
	switch fn := int32(m.CPU.R[7]); fn {
	case 0: // SYSINFO_INIT
		m.CPU.R[0] = 0
	case 2: // SYSINFO_ID: the 24 byte console id block. No flash
		// sysinfo partition is modeled, so the block stays zeroed,
		// matching the copy BootDisc leaves at 0x8C000068.
		m.writeGuest(m.CPU.R[4], make([]byte, 24))
		m.CPU.R[0] = 0
	default:
		m.recordUnimpl(biosVecSysinfo, fn)
		m.CPU.R[0] = 0xFFFFFFFF
	}
}

// systemDispatch routes the system boot vector by R4.
// Function 0 is the documented exit to the BIOS menu: real
// software never returns from it. This HLE has no menu, so the
// CPU halts and the machine idles on its last frame.
func (m *Machine) systemDispatch() {
	switch fn := int32(m.CPU.R[4]); fn {
	case 0:
		m.bios.exitToMenu = true
		m.CPU.Halted = true
		m.CPU.R[0] = 0
	default:
		m.recordUnimpl(biosVecSystem, fn)
		m.CPU.R[0] = 0xFFFFFFFF
	}
}

// biosStateSize is the serialized size of the BIOS block:
// booted / gdInit / exitToMenu flags and padding (4), nextID
// (4), the GD request (active byte and padding 4, status 4,
// id 4, cmd 4, params 16, result 16 = 48), the unimplemented
// call shapes (count 4, overflow 4, then maxBIOSUnimpl records
// of vector id 4, function 4, count 4, padding 4 = 128).
const biosStateSize = 4 + 4 + 48 + 8 + maxBIOSUnimpl*16

// serializeBIOS writes the BIOS block.
func (m *Machine) serializeBIOS() []byte {
	out := make([]byte, biosStateSize)
	if m.bios.booted {
		out[0] = 1
	}
	if m.bios.gdInit {
		out[1] = 1
	}
	if m.bios.exitToMenu {
		out[2] = 1
	}
	binary.LittleEndian.PutUint32(out[4:], m.bios.nextID)
	req := &m.bios.req
	if req.active {
		out[8] = 1
	}
	binary.LittleEndian.PutUint32(out[12:], uint32(req.status))
	binary.LittleEndian.PutUint32(out[16:], req.id)
	binary.LittleEndian.PutUint32(out[20:], uint32(req.cmd))
	for i, v := range req.params {
		binary.LittleEndian.PutUint32(out[24+4*i:], v)
	}
	for i, v := range req.result {
		binary.LittleEndian.PutUint32(out[40+4*i:], v)
	}
	binary.LittleEndian.PutUint32(out[56:], uint32(len(m.bios.unimpl)))
	binary.LittleEndian.PutUint32(out[60:], uint32(m.bios.unimplOver))
	for i, c := range m.bios.unimpl {
		off := 64 + 16*i
		binary.LittleEndian.PutUint32(out[off:], uint32(biosVecID(c.Vector)))
		binary.LittleEndian.PutUint32(out[off+4:], uint32(c.Function))
		binary.LittleEndian.PutUint32(out[off+8:], uint32(c.Count))
	}
	return out
}

// deserializeBIOS reads the BIOS block back.
func (m *Machine) deserializeBIOS(b []byte) {
	m.bios = biosState{
		booted:     b[0] != 0,
		gdInit:     b[1] != 0,
		exitToMenu: b[2] != 0,
		nextID:     binary.LittleEndian.Uint32(b[4:]),
		req: gdRequest{
			active: b[8] != 0,
			status: int32(binary.LittleEndian.Uint32(b[12:])),
			id:     binary.LittleEndian.Uint32(b[16:]),
			cmd:    int32(binary.LittleEndian.Uint32(b[20:])),
		},
		unimplOver: int(binary.LittleEndian.Uint32(b[60:])),
	}
	for i := range m.bios.req.params {
		m.bios.req.params[i] = binary.LittleEndian.Uint32(b[24+4*i:])
		m.bios.req.result[i] = binary.LittleEndian.Uint32(b[40+4*i:])
	}
	n := int(binary.LittleEndian.Uint32(b[56:]))
	if n > maxBIOSUnimpl {
		n = maxBIOSUnimpl
	}
	for i := 0; i < n; i++ {
		off := 64 + 16*i
		vecID := int(binary.LittleEndian.Uint32(b[off:]))
		if vecID < 0 || vecID >= len(biosVecNames) {
			vecID = biosVecStub
		}
		m.bios.unimpl = append(m.bios.unimpl, BIOSCall{
			Vector:   biosVecNames[vecID],
			Function: int32(binary.LittleEndian.Uint32(b[off+4:])),
			Count:    int(binary.LittleEndian.Uint32(b[off+8:])),
		})
	}
}
