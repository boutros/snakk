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

var (
	chatHistory *FIFO
	nextID      int
	h           chatHub
	cfg         *Config
	l           = log.New()
	funcMap     = template.FuncMap{
		"timeFormat": func(t time.Time) string {
			return t.Format("15:04")
		}}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFiles("data/page.html"))
	commands  = []command{
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
			delete(h.connections, c)
			close(c.send)
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
						nickTaken := false
						for c := range h.connections {
							if c.user.Nick == nick {
								nickTaken = true
								break
							}
						}
						if nickTaken && um.c.user.Nick != nick {
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
						if oldNick != "" {
							msg.UserChange = um.c.user.ID
							msg.Message = fmt.Sprintf("%s is now known as %s", oldNick, nick)
						} else {
							msg.UserNew = um.c.user.ID
							msg.Message = fmt.Sprintf("%s has joined the chat", nick)
						}
						for c := range h.connections {
							if c == um.c {
								continue
							}
							select {
							case c.send <- msg:
							default:
								close(c.send)
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

	chatHistory = newFIFO(cfg.ChatHistoryNumLines)

	h = newChatHub()
	go h.run()

	// routing
	http.HandleFunc("/", chatRoomHandler)
	http.HandleFunc("/users", usersHandler)
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/favicon.ico", serveFile("data/irc.ico"))
	http.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /"))
	})

	l.Info("starting chat server")
	http.ListenAndServe(fmt.Sprintf(":%d", cfg.ServePort), nil)
}
