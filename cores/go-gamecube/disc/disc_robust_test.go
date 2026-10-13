package disc

import "testing"

// Robustness gates for corrupt images (GCN-4 item 4): every
// malformed structure must return an error, never panic.
// The parser uses bounds checks only, no recover: a panic
// in any of these cases fails the test run outright.

// corruptGC returns a copy of the synthetic GC image with
// one header field smashed by the caller's edit.
func corruptGC(edit func(b []byte)) []byte {
	b := append([]byte(nil), syntheticGC()...)
	edit(b)
	return b
}

func TestRobustTruncatedHeaderAtBI2(t *testing.T) {
	// Exactly 0x440 bytes: the header fields fit but the
	// bi2 block (through 0x45C) does not. Before the GCN-4
	// hardening this read past the end of the slice.
	full := syntheticGC()
	if _, err := Parse(full[:0x440]); err != ErrTruncated {
		t.Fatalf("0x440 byte image err = %v, want ErrTruncated", err)
	}
	if _, err := Parse(full[:0x45F]); err != ErrTruncated {
		t.Fatalf("0x45F byte image err = %v, want ErrTruncated", err)
	}
	img, err := Parse(full[:0x460])
	if err != nil {
		t.Fatalf("0x460 byte image err = %v, want success", err)
	}
	if img.DOLOffset == 0 {
		t.Fatal("0x460 byte image lost its DOL offset")
	}
}

func TestRobustWildDOLOffset(t *testing.T) {
	img, err := Parse(corruptGC(func(b []byte) {
		put32(b, 0x420, 0xFFFFFFF0) // DOL offset near 4 GiB
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.DOL(); err != ErrTruncated {
		t.Fatalf("DOL err = %v, want ErrTruncated", err)
	}
}

func TestRobustWildSectionFields(t *testing.T) {
	// A section whose offset plus size wraps 32 bits must
	// not pass the length check.
	img, err := Parse(corruptGC(func(b []byte) {
		dol := uint32(0x5000)          // the synthetic image's DOL offset
		put32(b, dol+0x00, 0xFFFFF000) // text0 offset
		put32(b, dol+0x90, 0x2000)     // text0 size: sum wraps
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.DOL(); err != ErrTruncated {
		t.Fatalf("DOL err = %v, want ErrTruncated", err)
	}
}

func TestRobustWildFSTOffset(t *testing.T) {
	img, err := Parse(corruptGC(func(b []byte) {
		put32(b, 0x424, 0xFFFFFF00) // FST offset near 4 GiB
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.Files(); err != ErrTruncated {
		t.Fatalf("Files err = %v, want ErrTruncated", err)
	}
}

func TestRobustWildFSTCount(t *testing.T) {
	// Entry counts whose table size overflows 32 bits, and
	// counts that simply exceed the image, both error.
	for _, count := range []uint32{0x15555556, 0x40000000, 100000} {
		img, err := Parse(corruptGC(func(b []byte) {
			fst := be32(b, 0x424)
			put32(b, fst+8, count) // root size field is the count
		}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := img.Files(); err != ErrTruncated {
			t.Fatalf("count %#x: Files err = %v, want ErrTruncated", count, err)
		}
	}
}

func TestRobustWildFileExtent(t *testing.T) {
	// A file entry whose offset plus size wraps must fail
	// extraction, not slice out of bounds.
	img, err := Parse(corruptGC(func(b []byte) {
		fst := be32(b, 0x424)
		// File entry 2 in the synthetic FST: smash its
		// offset to near 4 GiB while its size stays real.
		put32(b, fst+2*12+4, 0xFFFFFFF0)
	}))
	if err != nil {
		t.Fatal(err)
	}
	files, err := img.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("synthetic FST lost its file")
	}
	if _, err := img.Extract(files[0].Path); err != ErrTruncated {
		t.Fatalf("Extract err = %v, want ErrTruncated", err)
	}
}

func TestRobustOverlappingDOLSections(t *testing.T) {
	// Overlapping sections are structurally loadable (the
	// loader writes them in order); the requirement is
	// only that nothing panics and the parse is exact.
	img, err := Parse(syntheticGC())
	if err != nil {
		t.Fatal(err)
	}
	dol, err := img.DOL()
	if err != nil {
		t.Fatal(err)
	}
	if len(dol.Sections) == 0 {
		t.Fatal("synthetic DOL has no sections")
	}
	b := corruptGC(func(b []byte) {
		// Point data section 0 at the text section address.
		d := be32(b, 0x420)
		put32(b, d+0x64, be32(b, d+0x48))
		put32(b, d+0x1C, be32(b, d+0x00))
		put32(b, d+0xAC, be32(b, d+0x90))
	})
	img2, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img2.DOL(); err != nil {
		t.Fatalf("overlapping sections DOL err = %v", err)
	}
}
