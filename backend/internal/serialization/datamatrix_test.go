package serialization_test

import (
	"bytes"
	"testing"

	"github.com/boombuler/barcode/datamatrix"

	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// The GS1 DataMatrix Guideline's example: (01)09501101020917(17)190508(10)ABCD1234(21)10.
func TestGS1DataMatrixContentFollowsTheGuidelineExample(t *testing.T) {
	p := gs1.Pack{GTIN: "09501101020917", Expiry: "190508", Lot: "ABCD1234", Serial: "10"}
	want := []byte{datamatrix.FNC1}
	want = append(want, "0109501101020917"+"17190508"+"10ABCD1234"...)
	want = append(want, datamatrix.FNC1)
	want = append(want, "2110"...)
	if got := serialization.GS1DataMatrixContent(p); !bytes.Equal([]byte(got), want) {
		t.Fatalf("content % x\nwant        % x", got, want)
	}
	// The ASCII group separator never reaches the symbol.
	if bytes.IndexByte([]byte(serialization.GS1DataMatrixContent(p)), 0x1d) >= 0 {
		t.Fatal("group separator left in the symbol content")
	}
}

func TestGS1SymbolDiffersFromAPlainOne(t *testing.T) {
	p := gs1.Pack{GTIN: "09501101020917", Serial: "10"}
	gs1Code, err := datamatrix.Encode(serialization.GS1DataMatrixContent(p))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := datamatrix.Encode(gs1.ElementString(p.AIs()))
	if err != nil {
		t.Fatal(err)
	}
	same := gs1Code.Bounds() == plain.Bounds()
	if same {
		b := gs1Code.Bounds()
		for x := 0; x < b.Dx() && same; x++ {
			for y := 0; y < b.Dy(); y++ {
				if gs1Code.At(x, y) != plain.At(x, y) {
					same = false
					break
				}
			}
		}
	}
	if same {
		t.Fatal("the GS1 symbol is identical to a plain Data Matrix symbol: no FNC1 start")
	}
}
