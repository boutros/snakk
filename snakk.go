package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	log "github.com/inconshreveable/log15"
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

type users struct {
	users map[string]int
	sync.RWMutex
}

func (all *users) Add(u *user) {
	all.Lock()
	defer all.Unlock()
	all.users[u.Nick] = u.ID
}

func (all *users) Remove(u *user) {
	all.Lock()
	defer all.Unlock()
	delete(all.users, u.Nick)
}

func (all *users) Get(nick string) bool {
	all.RLock()
	defer all.RUnlock()
	_, ok := all.users[nick]
	return ok
}

func (all *users) All() []user {
	r := make([]user, 0)
	all.RLock()
	defer all.RUnlock()
	for k, v := range all.users {
		r = append(r, user{Nick: k, ID: v})
	}
	return r
}

var (
	usersOnline = &users{users: make(map[string]int)}
	nextID      int
	h           chatHub
	cfg         *Config
	l           log.Logger
	templates   = template.Must(template.ParseFiles("data/page.html"))
	commands    = []command{
		{Cmd: "nick", Desc: "'/nick <nickname>', sets your nicname."},
		{Cmd: "help", Desc: "'/help', shows the list of commands. '/help <command>', shows command usage."},
		{Cmd: "me", Desc: "'/me <action>', sends action to the chatroom (actions are written in 3rd person)."},
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
		Host  string
		Users []user
	}{
		Host:  r.Host,
		Users: usersOnline.All(),
	}
	err := templates.ExecuteTemplate(w, "page.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
}

func newChatHub() chatHub {
	return chatHub{
		incoming:    make(chan userMsg),
		broadcast:   make(chan chatLine, 1),
		register:    make(chan *conn),
		unregister:  make(chan *conn),
		connections: make(map[*conn]bool),
	}
}

func cleanNick(s string) string {
	var b bytes.Buffer
	for i, r := range s {
		if i == 10 {
			break
		}
		if unicode.IsSpace(r) {
			b.WriteString("_")
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (h chatHub) run() {
	for {
		select {
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
			usersOnline.Remove(c.user)
			l.Info("client disconnected", log.Ctx{"address": c.ws.RemoteAddr().String()})
			nick := c.user.Nick
			id := c.user.ID
			delete(h.connections, c)
			close(c.send)
			msg := chatLine{
				Color:    "green",
				Meta:     true,
				Author:   "*",
				UserLeft: id,
				Message:  fmt.Sprintf("%s has left the chat", nick)}
			h.broadcast <- msg
		case msg := <-h.broadcast:
			for c := range h.connections {
				select {
				case c.send <- msg:
				default:
					close(c.send)
					usersOnline.Remove(c.user)
					delete(h.connections, c)
				}
			}
		case um := <-h.incoming:
			m := bytes.TrimSpace(um.msg)
			if bytes.HasPrefix(m, []byte("/")) {
				b := bytes.SplitN(m[1:len(m)], []byte(" "), 2)
				cmd := bytes.ToLower(b[0])
				rest := []byte("")
				if len(b) > 1 {
					rest = b[1]
				}
				if um.c.user.Nick == "" && string(cmd) != "nick" {
					um.c.send <- chatLine{
						Color:   "red",
						Author:  "**",
						Meta:    true,
						Message: "You must choose a nicname before you can join the chat."}
					break
				}
				switch string(cmd) {
				case "nick":
					if len(rest) > 0 {
						nick := cleanNick(string(rest))
						if usersOnline.Get(nick) && um.c.user.Nick != nick {
							um.c.send <- chatLine{
								Color:   "red",
								Author:  "**",
								Meta:    true,
								Message: "That nick is allready taken. Choose another one."}
							break
						}
						oldNick := um.c.user.Nick
						um.c.user.Nick = nick
						um.c.send <- chatLine{
							Color:      "green",
							Author:     "**",
							Meta:       true,
							UserChange: um.c.user.ID,
							UserNick:   um.c.user.Nick,
							Message:    "You are now known as " + um.c.user.Nick}
						msg := chatLine{
							Color:    "green",
							Author:   "*",
							Meta:     true,
							UserNick: um.c.user.Nick}
						if usersOnline.Get(oldNick) {
							msg.UserChange = um.c.user.ID
							msg.Message = fmt.Sprintf("%s is now known as %s", oldNick, nick)
						} else {
							msg.UserNew = um.c.user.ID
							msg.Message = fmt.Sprintf("%s has joined the chat", nick)
						}
						usersOnline.Add(um.c.user)
						for c := range h.connections {
							if c == um.c {
								continue
							}
							select {
							case c.send <- msg:
							default:
								close(c.send)
								usersOnline.Remove(c.user)
								delete(h.connections, c)
							}
						}
						break
					}
					um.c.send <- chatLine{
						Color:   "red",
						Author:  "**",
						Meta:    true,
						Message: "Your nickname cannot be empty."}
				case "uptime":
					println("display uptime")
				case "me":
					msg := chatLine{
						Color:   "green",
						Meta:    true,
						Author:  "*",
						Message: fmt.Sprintf("%s %s", um.c.user.Nick, string(rest))}
					h.broadcast <- msg
				case "help":
					println("help")
				default:
					um.c.send <- chatLine{
						Color:   "red",
						Author:  "**",
						Meta:    true,
						Message: "Unknown command"}
				}
			} else {
				if um.c.user.Nick == "" {
					um.c.send <- chatLine{
						Color:   "red",
						Author:  "**",
						Meta:    true,
						Message: "You must choose a nicname before you can join the chat."}
				}
				msg := chatLine{Message: string(m), Author: um.c.user.Nick}
				h.broadcast <- msg
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

	l = log.New()
	//chatHistory := newFIFO(cfg.ChatHistoryNumLines)

	h = newChatHub()
	go h.run()

	// routing
	http.HandleFunc("/", chatRoomHandler)
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/favicon.ico", serveFile("data/irc.ico"))
	http.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /"))
	})

	l.Info("starting chat server")
	http.ListenAndServe(fmt.Sprintf(":%d", cfg.ServePort), nil)
}
