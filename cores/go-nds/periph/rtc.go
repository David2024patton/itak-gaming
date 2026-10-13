package periph

// RTC is the real-time clock stub on the ARM7 IO page at
// 0x04000138. The real chip is a bit-serial device behind a
// 4-bit port; this slice implements a deliberately simplified
// byte model, documented as such: reads cycle through a 7-byte
// BCD block (second, minute, hour, weekday, day, month, year)
// and any write resets the read index. The clock starts at
// the fixed epoch 2000-01-01 00:00:00 and advances one second
// per 60 frames; each elapsed second raises the ARM7 RTC
// interrupt once.
type RTC struct {
	frames uint64
	idx    uint8
}

var daysInMonth = [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

func leap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// Time returns the current clock as (year since 2000, month,
// day, weekday 0=Sunday, hour, minute, second).
func (r *RTC) Time() (int, int, int, int, int, int, int) {
	sec := r.frames / 60
	year, days := 0, int(sec/86400)
	for {
		dy := 365
		if leap(2000 + year) {
			dy = 366
		}
		if days < dy {
			break
		}
		days -= dy
		year++
	}
	month := 0
	for {
		dm := daysInMonth[month]
		if month == 1 && leap(2000+year) {
			dm = 29
		}
		if days < dm {
			break
		}
		days -= dm
		month++
	}
	// 2000-01-01 was a Saturday (weekday 6).
	weekday := (6 + int(sec/86400)) % 7
	sod := int(sec % 86400)
	return year, month + 1, days + 1, weekday, sod / 3600, (sod / 60) % 60, sod % 60
}

func bcd(v int) byte { return byte(v/10)<<4 | byte(v%10) }

// read returns the next byte of the BCD block.
func (r *RTC) read() byte {
	year, month, day, weekday, hour, min, sec := r.Time()
	block := [7]byte{
		bcd(sec), bcd(min), bcd(hour), bcd(weekday),
		bcd(day), bcd(month), bcd(year),
	}
	b := block[r.idx%7]
	r.idx++
	return b
}

// write resets the read index (any write; the serial command
// protocol is not modeled in this slice).
func (r *RTC) write() { r.idx = 0 }

// tick advances the clock by one frame and reports whether a
// second elapsed (the system raises the RTC IRQ then).
func (r *RTC) tick() bool {
	r.frames++
	return r.frames%60 == 0
}
