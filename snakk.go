package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	log "gopkg.in/inconshreveable/log15.v2"
)

type user struct {
	Nick string
	ID   int
}

type chatLine struct {
	Color      string
	Meta       bool // if true, line will not be stored in chatHistory
	TimeStamp  time.Time
	Author     string
	Message    string
	UserNew    int    // user id of new user
	UserChange int    // user id of user who changed nick
	UserLeft   int    // user id of user who left
	UserNick   string // new user's nick or changed nick
}

type command struct {
	Cmd  string
	Desc string
}

type userMsg struct {
	msg []byte
	c   *conn
}

var (
	chatHistory *Stack
	nextID      int
	h           chatHub
	cfg         *Config
	status      = registerMetrics()
	l           = log.New()
	funcMap     = template.FuncMap{
		"timeFormat": func(t time.Time) string {
			return t.Format("15:04")
		}}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFiles("data/page.html"))
	commands  = []command{
		{Cmd: "nick", Desc: "'/nick &lt;nickname&gt;', sets your nickname."},
		{Cmd: "help", Desc: "'/help', shows the list of commands. '/help &lt;command&gt;', shows command usage."},
		{Cmd: "me", Desc: "'/me &lt;action&gt;', sends action to the chatroom (actions are written in 3rd person)."},
		{Cmd: "uptime", Desc: "'/uptime', displays how long server has been running."}}
)

type Config struct {
	ServePort           int
	LogLevel            string
	LogFile             string
	ChatHistoryNumLines int
	UseBasicAuth        bool
	BasicAuthUsername   string
	BasicAuthPassword   string
}

func loadConfig(filename string) (*Config, error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	c := Config{}
	err = json.Unmarshal(b, &c)
	if err != nil {
		return nil, err
	}

	return &c, nil
}

func chatRoomHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Host    string
		Users   []user
		History []chatLine
	}{
		Host:    r.Host,
		History: chatHistory.All(),
	}
	err := templates.ExecuteTemplate(w, "page.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func usersHandler(w http.ResponseWriter, r *http.Request) {
	h.sendUsers <- true
	users := <-h.getUsers

	b, err := json.Marshal(users)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	b, err := json.Marshal(status.Export())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func serveFile(filename string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filename)
	}
}

type conn struct {
	ws   *websocket.Conn
	send chan chatLine
	user *user
}

func (c *conn) reader() {
	defer func() {
		h.unregister <- c
		c.ws.Close()
	}()

	for {
		_, message, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		h.incoming <- userMsg{msg: message, c: c}
	}
}

func (c *conn) writer() {
	for message := range c.send {
		message.TimeStamp = time.Now()
		err := c.ws.WriteJSON(message)
		if err != nil {
			break
		}
	}
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	ws, err := websocket.Upgrade(w, r, nil, 1024, 1024)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		l.Error("websocket upgrade error", log.Ctx{"details": err.Error()})
		return
	}

	c := &conn{send: make(chan chatLine, 1), ws: ws, user: &user{}}
	h.register <- c

	//status.ClientsConnected.Inc(1)
	go c.writer()
	c.reader()
	//status.ClientsConnected.Dec(1)
}

type chatHub struct {
	connections map[*conn]bool
	incoming    chan userMsg
	broadcast   chan chatLine
	register    chan *conn
	unregister  chan *conn
	sendUsers   chan bool
	getUsers    chan []user
}

func newChatHub() chatHub {
	return chatHub{
		incoming:    make(chan userMsg),
		broadcast:   make(chan chatLine, 1),
		register:    make(chan *conn),
		unregister:  make(chan *conn),
		connections: make(map[*conn]bool),
		sendUsers:   make(chan bool),
		getUsers:    make(chan []user),
	}
}

func cleanNick(s string) string {
	var b bytes.Buffer
	for i, r := range s {
		if i == 10 {
			break
		}
		if unicode.IsSpace(r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FSM functions for message handling
type stateFn func(chatHub, *userMsg) stateFn

func startState(h chatHub, m *userMsg) stateFn {
	m.msg = bytes.TrimSpace(m.msg)
	if m.c.user.Nick == "" && !bytes.HasPrefix(m.msg, []byte("/nick")) {
		return noNickState
	}
	if bytes.HasPrefix(m.msg, []byte("/")) {
		return cmdState
	}
	return bcastState
}

func cmdState(h chatHub, m *userMsg) stateFn {
	b := bytes.SplitN(m.msg[1:len(m.msg)], []byte(" "), 2)
	cmd := bytes.ToLower(b[0])
	rest := []byte("")
	if len(b) > 1 {
		rest = b[1]
	}
	switch string(cmd) {
	case "nick":
		if len(rest) == 0 {
			return emptyNickState
		}
		nick := cleanNick(string(rest))
		nickTaken := false
		for c := range h.connections {
			if c.user.Nick == nick {
				nickTaken = true
				break
			}
		}
		if nickTaken && m.c.user.Nick != nick {
			return nickTakenState
		}
		oldNick := m.c.user.Nick
		m.c.user.Nick = nick
		m.c.send <- chatLine{
			Color:      "green",
			Author:     "**",
			Meta:       true,
			UserChange: m.c.user.ID,
			UserNick:   m.c.user.Nick,
			Message:    "You are now known as " + m.c.user.Nick}
		msg := chatLine{
			Color:    "green",
			Author:   "*",
			Meta:     true,
			UserNick: m.c.user.Nick}
		if oldNick != "" {
			msg.UserChange = m.c.user.ID
			msg.Message = fmt.Sprintf("%s is now known as %s", oldNick, nick)
		} else {
			msg.UserNew = m.c.user.ID
			msg.Message = fmt.Sprintf("%s has joined the chat", nick)
		}
		for c := range h.connections {
			if c == m.c {
				continue
			}
			select {
			case c.send <- msg:
			default:
				close(c.send)
				delete(h.connections, c)
			}
		}
		return nil
	case "uptime":
		return uptimeState
	case "me":
		m.msg = rest
		return meState
	case "help":
		if len(bytes.TrimSpace(rest)) == 0 {
			return helpHelpState
		}
		cleanedCmd := string(bytes.TrimSpace(rest))
		validCmd := false
		for _, c := range commands {
			if cleanedCmd == c.Cmd {
				m.c.send <- chatLine{
					Color:   "green",
					Author:  "**",
					Meta:    true,
					Message: c.Desc}
				validCmd = true
				return nil
			}
		}
		if !validCmd {
			m.c.send <- chatLine{
				Color:   "red",
				Author:  "**",
				Meta:    true,
				Message: "Unknown command."}
		}
	default:
		return unknownCmdState
	}
	return nil
}

func bcastState(h chatHub, m *userMsg) stateFn {
	h.broadcast <- chatLine{Message: string(m.msg), Author: m.c.user.Nick}

	return nil
}

func noNickState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "red",
		Author:  "**",
		Meta:    true,
		Message: "You must choose a nickname before you can join the chat."}
	return nil
}

func emptyNickState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "red",
		Author:  "**",
		Meta:    true,
		Message: "Your nickname cannot be empty."}
	return nil
}

func nickTakenState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "red",
		Author:  "**",
		Meta:    true,
		Message: "That nick is allready taken. Choose another one."}
	return nil
}

func uptimeState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "green",
		Author:  "**",
		Meta:    true,
		Message: fmt.Sprintf("The server has been running for %s.", status.Export().UpTime)}
	return nil
}

func meState(h chatHub, m *userMsg) stateFn {
	msg := chatLine{
		Color:   "green",
		Meta:    true,
		Author:  "*",
		Message: fmt.Sprintf("%s %s", m.c.user.Nick, string(m.msg))}
	h.broadcast <- msg
	return nil
}

func unknownCmdState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "red",
		Author:  "**",
		Meta:    true,
		Message: "Unknown command"}
	return nil
}

func helpHelpState(h chatHub, m *userMsg) stateFn {
	m.c.send <- chatLine{
		Color:   "green",
		Author:  "**",
		Meta:    true,
		Message: "Available commands: nick, me, help, uptime. Type /help &lt;command&gt; for usage information."}
	return nil
}

func (h chatHub) run() {
	for {
		select {
		case <-h.sendUsers:
			users := []user{}
			for c := range h.connections {
				if c.user.Nick != "" {
					users = append(users, *c.user)
				}
			}
			h.getUsers <- users
		case c := <-h.register:
			h.connections[c] = true
			nextID = nextID + 1
			c.user.ID = nextID
			l.Info("client connected", log.Ctx{"address": c.ws.RemoteAddr().String()})
			c.send <- chatLine{
				Color:   "green",
				Author:  "**",
				Meta:    true,
				Message: "Welcome to snakk!"}
			c.send <- chatLine{
				Color:   "green",
				Author:  "**",
				Meta:    true,
				Message: "Enter your nickname to join the chat."}
		case c := <-h.unregister:
			l.Info("client disconnected", log.Ctx{"address": c.ws.RemoteAddr().String()})
			nick := c.user.Nick
			id := c.user.ID
			if _, ok := h.connections[c]; ok {
				delete(h.connections, c)
				close(c.send)
			}
			if nick != "" {
				msg := chatLine{
					Color:    "green",
					Meta:     true,
					Author:   "*",
					UserLeft: id,
					Message:  fmt.Sprintf("%s has left the chat", nick)}
				h.broadcast <- msg
			}
		case msg := <-h.broadcast:
			msg.TimeStamp = time.Now()
			for c := range h.connections {
				select {
				case c.send <- msg:
				default:
					close(c.send)
					delete(h.connections, c)
				}
			}
			if !msg.Meta {
				chatHistory.Push(msg)
			}
		case um := <-h.incoming:
			for state := startState; state != nil; {
				state = state(h, &um)
			}

		}
	}
}

func main() {
	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Error("failed to load config file", log.Ctx{"cause": err.Error()})
		os.Exit(1)
	}

	// trap ^C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		h.broadcast <- chatLine{
			Color:   "red",
			Author:  "*",
			Meta:    true,
			Message: "Server is shutting down, sorry!"}
		l.Info("recieved interuption signal; shutting down server")
		os.Exit(0)
	}()

	level, err := log.LvlFromString(cfg.LogLevel)
	if err != nil {
		level = log.LvlInfo
	}

	l.SetHandler(log.MultiHandler(
		log.LvlFilterHandler(level, log.Must.FileHandler(cfg.LogFile, log.LogfmtFormat())),
		log.StreamHandler(os.Stdout, log.TerminalFormat())))

	// investigate crashes, possibly due to mod_proxy_wstunnel bug
	defer func() {
		if err := recover(); err != nil {
			l.Error("we're going down:(", log.Ctx{"error": err, "stack": string(debug.Stack())})
			os.Exit(1)
		}
	}()

	chatHistory = newStack(cfg.ChatHistoryNumLines)

	h = newChatHub()
	go h.run()

	// routing
	http.HandleFunc("/", chatRoomHandler)
	http.HandleFunc("/users", usersHandler)
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/.status", statusHandler)
	http.HandleFunc("/favicon.ico", serveFile("data/irc.ico"))
	http.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /"))
	})

	l.Info("starting chat server")
	err = http.ListenAndServe(fmt.Sprintf(":%d", cfg.ServePort), nil)
	if err != nil {
		l.Error("http server crashed", log.Ctx{"cause": err.Error()})
	}
}
