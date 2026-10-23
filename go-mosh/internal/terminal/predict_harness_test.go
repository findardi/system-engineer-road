package terminal

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// runPredictScript drives a PredictionEngine like the C++ oracle that produced
// testdata/predict/*.out, and returns the frames it painted.
func runPredictScript(script string) string {
	var out strings.Builder
	var emu *Emulator
	var local *Framebuffer
	pe := NewPredictionEngine()
	pe.Mode = PredictAlways
	pe.now = func() int64 { return 1 << 30 } // mosh's clock is frozen in the oracle
	var sent uint64
	sc := bufio.NewScanner(strings.NewReader(script))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		arg := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		num := func() uint64 { n, _ := strconv.ParseUint(arg(1), 10, 64); return n }
		switch f[0] {
		case "size":
			w, _ := strconv.Atoi(arg(1))
			h, _ := strconv.Atoi(arg(2))
			emu = NewEmulator(w, h)
			local = emu.Framebuffer().Clone()
		case "host":
			b, _ := hex.DecodeString(arg(1))
			emu.Write(b)
		case "key":
			b, _ := hex.DecodeString(arg(1))
			for _, c := range b {
				pe.SetFrameSent(sent)
				pe.NewUserByte(c, local)
			}
		case "sent":
			sent = num()
		case "acked":
			pe.SetFrameAcked(num())
		case "late":
			pe.SetFrameLateAcked(num())
		case "interval":
			pe.SetSendInterval(int(num()))
		case "overwrite":
			pe.Overwrite = arg(1) == "1"
		case "pref":
			pe.Mode = map[string]PredictionMode{"always": PredictAlways, "adaptive": PredictAdaptive, "experimental": PredictExperimental}[arg(1)]
		case "reset":
			pe.Reset()
		case "paint":
			fb := emu.Framebuffer().Clone()
			pe.Cull(fb)
			pe.Apply(fb)
			local = fb
			out.WriteString(dumpFrame(fb))
		}
	}
	return out.String()
}

func dumpFrame(fb *Framebuffer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "frame cursor %d %d\n", fb.DS.cursorRow, fb.DS.cursorCol)
	blank := Renditions{}.SGR()
	for y, row := range fb.rows {
		var line strings.Builder
		for x := range row.cells {
			c := &row.cells[x]
			sgr := c.renditions.SGR()
			if c.empty() && sgr == blank && !c.wide {
				continue
			}
			fmt.Fprintf(&line, " %d:", x)
			if c.empty() {
				line.WriteString("_")
			} else {
				line.WriteString(hex.EncodeToString([]byte(c.grapheme())))
			}
			if sgr != blank {
				line.WriteString("/" + sgr[2:])
			}
			if c.wide {
				line.WriteString("/W")
			}
		}
		if line.Len() > 0 {
			fmt.Fprintf(&b, "row %d%s\n", y, line.String())
		}
	}
	return b.String()
}
