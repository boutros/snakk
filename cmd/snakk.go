package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/nsf/termbox-go"
)

type chatLine struct {
	Color      string
	Meta       bool
	TimeStamp  time.Time
	Author     string
	Message    string
	UserNew    int
	UserChange int
	UserLeft   int
	UserNick   string
}

func reader(c *websocket.Conn) {
	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			break
		}
		var line chatLine
		err = json.Unmarshal(message, &line)
		if err == nil {
			history = append(history, line)
			drawAll()
		}
	}
	history = append(history,
		chatLine{
			Color:     "red",
			Message:   "server disconnected",
			TimeStamp: time.Now(),
			Author:    "**",
		})
	drawAll()
}

func tbprint(x, y int, fg, bg termbox.Attribute, msg string) {
	for _, c := range msg {
		termbox.SetCell(x, y, c, fg, bg)
		x++
	}
}

// like tbprint, but split into multiple line if msg is longer than w
func tbprintm(x, y int, fg, bg termbox.Attribute, msg string, w int) {
	yn := y
	xold := x
	for _, c := range msg {
		termbox.SetCell(x, yn, c, fg, bg)
		x++
		if x >= w {
			yn++
			x = xold
		}
	}
}

func vLine(x, y, y2 int) {
	for ly := y; ly < y2; y++ {
		termbox.SetCell(x, ly, '│', termbox.ColorDefault, termbox.ColorDefault)
	}
}

func hLine(x, y, x2 int) {
	for lx := x; lx < x2; lx++ {
		termbox.SetCell(lx, y, '─', termbox.ColorDefault, termbox.ColorDefault)
	}
}

func drawAll() {
	termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)
	w, h := termbox.Size()
	const cd = termbox.ColorDefault

	i := len(history)
	for y := h - 3; y > 0; y-- {
		i--
		if i < 0 {
			break
		}
		line := history[i]
		c := cd
		if line.Color == "green" {
			c = termbox.ColorGreen
		}
		if line.Color == "red" {
			c = termbox.ColorRed
		}

		numChars := utf8.RuneCountInString(line.Message)
		numLines := 1
		if w > 19 && numChars > (w-19) {
			numLines = numChars / (w - 19)
			if numChars%w > 0 {
				numLines++
			}
		}
		if numLines > 1 {
			y = y - numLines + 1
		}

		tbprint(0, y, c, cd, line.TimeStamp.Format("[03:04]"))
		tbprint(8, y, c, cd, line.Author)
		tbprintm(19, y, c, cd, line.Message, w)
	}

	hLine(0, h-2, w)
	//vLine(w-10, 1, h-2)

	termbox.Flush()
}

var history []chatLine

func main() {

	if len(os.Args) != 2 {
		fmt.Println("usage: snakk <hostname>\nEx: snakk snakkis.deichman.no")
		os.Exit(1)
	}
	host := os.Args[1]
	fmt.Printf("connecting to host %v ...\n", host)

	dialer := websocket.DefaultDialer
	ws, _, err := dialer.Dial("ws://"+host+"/ws", http.Header{})
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()
	go reader(ws)

	err = termbox.Init()
	if err != nil {
		log.Fatal(err)
	}
	defer termbox.Close()
	drawAll()

loop:
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyEsc:
				break loop
			}
		case termbox.EventResize:
			drawAll()
		case termbox.EventError:
			panic(ev.Err)
		}
		/*
			err = ws.WriteMessage(websocket.TextMessage, []byte(msg))
			if err != nil {
				break loop
			}
		*/

	}
}
