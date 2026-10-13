package enginetest_test

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/enginetest"
)

// The fixture disc parses through the cd layer with the
// documented header fields, and the payload in its root
// reads back as the fixture pattern.
func TestBuildDiscParses(t *testing.T) {
	iso := enginetest.BuildDisc()
	img, err := cd.OpenBytes(iso, 2048)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	hdr, err := img.SaturnHeader()
	if err != nil {
		t.Fatalf("SaturnHeader: %v", err)
	}
	if hdr.IPSize != enginetest.IPSize {
		t.Fatalf("IPSize = %#x, want %#x", hdr.IPSize, enginetest.IPSize)
	}
	if hdr.MasterStack != 0x06001000 || hdr.SlaveStack != 0x06000800 {
		t.Fatalf("stacks = %#x/%#x, want 0x06001000/0x06000800", hdr.MasterStack, hdr.SlaveStack)
	}
	entries, err := img.ListRoot()
	if err != nil {
		t.Fatalf("ListRoot: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "PAYLOAD.BIN" || entries[0].Size != enginetest.PayloadSize {
		t.Fatalf("root entries = %v", entries)
	}
	data, err := img.ReadFile(entries[0])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(data, enginetest.Payload()) {
		t.Fatal("payload bytes differ from the fixture pattern")
	}
	// The image is deterministic: rebuilding gives the
	// same bytes.
	if again := enginetest.BuildDisc(); !bytes.Equal(again, iso) {
		t.Fatal("BuildDisc is not deterministic")
	}
	sum := enginetest.PayloadChecksum()
	manual := uint32(0)
	for _, b := range enginetest.Payload() {
		manual += uint32(b)
	}
	if sum != manual {
		t.Fatalf("PayloadChecksum = %#x, manual sum %#x", sum, manual)
	}
}
