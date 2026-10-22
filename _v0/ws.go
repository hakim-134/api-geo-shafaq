package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
	maxMsgSize = 4096
	sendBuffer = 32
)

// inMsg is every message a browser can send.
type inMsg struct {
	T        string    `json:"t"` // create | join | settings | start | pano | pin | guess | next | leave
	Name     string    `json:"name"`
	Code     string    `json:"code"`
	Token    string    `json:"token"`
	Settings *Settings `json:"settings"`
	Round    int       `json:"round"`
	Pos      *LatLon   `json:"pos"`
	Hint     bool      `json:"hint"`
}

// client is one websocket connection.
type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func (c *client) queue(msg []byte) {
	select {
	case c.send <- msg:
	case <-c.done:
	default:
		c.close() // too slow to keep up; it will reconnect and get a fresh state
	}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *client) writeLoop() {
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *client) sendError(err error) {
	msg, _ := json.Marshal(map[string]string{"t": "error", "code": err.Error()})
	c.queue(msg)
}

// ServeWS runs one player's connection. up decides which pages may connect (see Origins).
func (h *Hub) ServeWS(up *websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.serveWS(up, w, r) }
}

func (h *Hub) serveWS(up *websocket.Upgrader, w http.ResponseWriter, r *http.Request) {
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the HTTP error
	}
	c := &client{conn: conn, send: make(chan []byte, sendBuffer), done: make(chan struct{})}
	go c.writeLoop()
	defer c.close()

	conn.SetReadLimit(maxMsgSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })

	var room *Room
	var me *Player
	defer func() {
		if room != nil {
			room.disconnect(me, c)
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m inMsg
		if json.Unmarshal(data, &m) != nil {
			c.sendError(errBadInput)
			continue
		}

		if room == nil {
			room, me, err = h.enter(c, m)
			if err != nil {
				// close code 4000 + reason tells the browser not to reconnect
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(4000, err.Error()), time.Now().Add(writeWait))
				return
			}
			continue
		}

		switch m.T {
		case "settings":
			if m.Settings == nil {
				err = errBadInput
			} else {
				err = room.setSettings(me, *m.Settings)
			}
		case "start":
			err = room.start(me)
		case "pano":
			if m.Pos == nil {
				err = errBadInput
			} else {
				err = room.setPanorama(me, m.Round, *m.Pos)
			}
		case "pin":
			err = room.pin(me, m.Pos, m.Hint)
		case "guess":
			err = room.guess(me, m.Pos, m.Hint)
		case "next":
			err = room.next(me)
		case "leave":
			room.leave(me)
			return
		default:
			err = errBadInput
		}
		// wrong-phase messages are normal (double clicks, late timers); don't bother the browser
		if err != nil && !errors.Is(err, errWrongPhase) {
			c.sendError(err)
		}
	}
}

// enter handles the first message of a connection: create a room or join one.
func (h *Hub) enter(c *client, m inMsg) (*Room, *Player, error) {
	switch m.T {
	case "create":
		if m.Settings == nil || !m.Settings.valid() {
			return nil, nil, errBadInput
		}
		if _, err := cleanName(m.Name); err != nil {
			return nil, nil, err
		}
		room, err := h.create(*m.Settings)
		if err != nil {
			return nil, nil, err
		}
		p, err := room.join(c, m.Name, "")
		if err != nil {
			return nil, nil, err
		}
		log.Printf("room %s created (%s, %s, %d rounds)", room.Code, m.Settings.Area, m.Settings.Diff, m.Settings.Rounds)
		return room, p, nil
	case "join":
		room := h.get(m.Code)
		if room == nil {
			return nil, nil, errNoRoom
		}
		p, err := room.join(c, m.Name, m.Token)
		if err == nil {
			log.Printf("room %s: %s joined", room.Code, p.ID)
		}
		return room, p, err
	}
	return nil, nil, errBadInput
}
