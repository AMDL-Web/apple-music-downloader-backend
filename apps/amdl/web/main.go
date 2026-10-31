//go:build js && wasm

// Command web is amdl's TUI for the website: the real interface over the
// in-memory demo backend, compiled to WebAssembly and drawn by the page's
// xterm.js (apps/amdl/web/build makes the page). The page calls
// amdlStart(cols, rows) once its terminal is open, passes what the person
// types to amdlInput(text) and each new size to amdlResize(cols, rows), and
// receives the TUI's output through amdlOutput(bytes) and its end through
// amdlExit().
package main

import (
	"context"
	"syscall/js"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"amdl/apps/amdl/internal/tui"
	"amdl/internal/buildinfo"
	"amdl/internal/client"
	"amdl/internal/demo"
)

// input is what the person typed, for the program to read. The page's
// callbacks must not block, so they queue onto ch and drop past its capacity.
type input struct {
	ch  chan []byte
	buf []byte
}

func (in *input) Read(p []byte) (int, error) {
	if len(in.buf) == 0 {
		in.buf = <-in.ch
	}
	n := copy(p, in.buf)
	in.buf = in.buf[n:]
	return n, nil
}

// output hands the program's bytes to the page, which writes them to xterm.js
// as they are: a write can end inside a UTF-8 sequence.
type output struct{ fn js.Value }

func (o output) Write(p []byte) (int, error) {
	a := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(a, p)
	o.fn.Invoke(a)
	return len(p), nil
}

func main() {
	ctx := context.Background()
	backend := demo.New(time.Now)
	backend.Start(ctx)
	c, err := client.NewWith(demo.Address, backend.Transport())
	if err != nil {
		panic(err)
	}
	in := &input{ch: make(chan []byte, 256)}
	start := make(chan [2]int, 1)
	resize := make(chan tea.WindowSizeMsg, 16)
	js.Global().Set("amdlStart", js.FuncOf(func(_ js.Value, a []js.Value) any {
		select {
		case start <- [2]int{a[0].Int(), a[1].Int()}:
		default:
		}
		return nil
	}))
	js.Global().Set("amdlInput", js.FuncOf(func(_ js.Value, a []js.Value) any {
		select {
		case in.ch <- []byte(a[0].String()):
		default:
		}
		return nil
	}))
	js.Global().Set("amdlResize", js.FuncOf(func(_ js.Value, a []js.Value) any {
		select {
		case resize <- tea.WindowSizeMsg{Width: a[0].Int(), Height: a[1].Int()}:
		default:
		}
		return nil
	}))

	size := <-start
	p := tui.New(ctx, tui.Options{Client: c, Target: "demo", Version: buildinfo.Version},
		tea.WithInput(in), tea.WithOutput(output{js.Global().Get("amdlOutput")}),
		tea.WithWindowSize(size[0], size[1]), tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}))
	go func() {
		for msg := range resize {
			p.Send(msg)
		}
	}()
	_, _ = p.Run() // the page offers a new session either way
	js.Global().Call("amdlExit")
}
