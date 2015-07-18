// API to see events fly by.
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"sync"
	"time"
)

type ApiServer struct {
	bus    *ApplicationBus
	server *http.Server
	auth   Authenticator

	// Remember the last event for each type. Already JSON prepared
	eventChannel   AppEventChannel
	lastEvents     map[AppEventType]*JsonAppEvent
	lastEventsLock sync.Mutex
}

// Similar to AppEvent, but json serialization hints and timestamp being
// a pointer to be able to omit it.
type JsonAppEvent struct {
	// An event is historic, if it had been recorded prior to the API
	// conneect
	IsHistoricEvent bool `json:",omitempty"`

	Timestamp time.Time    `json:"timestamp"`
	Ev        AppEventType `json:"type"`
	Target    Target       `json:"target"`
	Source    string       `json:"source"`
	Msg       string       `json:"msg"`
	Value     int          `json:"value,omitempty"`
	Timeout   *time.Time   `json:"timeout,omitempty"`
}

func JsonEventFromAppEvent(event *AppEvent) *JsonAppEvent {
	jev := &JsonAppEvent{
		Timestamp: event.Timestamp,
		Ev:        event.Ev,
		Target:    event.Target,
		Source:    event.Source,
		Msg:       event.Msg,
		Value:     event.Value,
	}
	if !event.Timeout.IsZero() {
		jev.Timeout = &event.Timeout
	}
	return jev
}

func NewApiServer(bus *ApplicationBus, auth Authenticator, port int) *ApiServer {
	newObject := &ApiServer{
		bus: bus,
		server: &http.Server{
			Addr: fmt.Sprintf(":%d", port),
			// JSON events listeners should be kept open for a while
			WriteTimeout: 3600 * time.Second,
		},
		auth:         auth,
		eventChannel: make(AppEventChannel),
		lastEvents:   make(map[AppEventType]*JsonAppEvent),
	}
	newObject.server.Handler = newObject
	bus.Subscribe(newObject.eventChannel)
	go newObject.collectLastEvents()
	return newObject
}

func (a *ApiServer) Run() {
	a.server.ListenAndServe()
}

func (a *ApiServer) collectLastEvents() {
	for {
		ev := <-a.eventChannel
		// Remember the last event of each type.
		a.lastEventsLock.Lock()
		jsonified := JsonEventFromAppEvent(ev)
		jsonified.IsHistoricEvent = true
		a.lastEvents[ev.Ev] = jsonified
		a.lastEventsLock.Unlock()
	}
}

func (a *ApiServer) getHistory() []*JsonAppEvent {
	result := EventList{}
	a.lastEventsLock.Lock()
	for _, ev := range a.lastEvents {
		result = append(result, ev)
	}
	a.lastEventsLock.Unlock()
	sort.Sort(result) // Show old events first
	return result
}

func flushResponse(out http.ResponseWriter) {
	if f, ok := out.(http.Flusher); ok {
		f.Flush()
	}
}

func (event *JsonAppEvent) writeJSONEvent(out http.ResponseWriter, jsonp_callback string) bool {
	json, err := json.Marshal(event)
	if err != nil {
		// Funny event, let's just ignore.
		return true
	}
	if jsonp_callback != "" {
		out.Write([]byte(jsonp_callback + "("))
	}
	_, err = out.Write(json)
	if err != nil {
		return false
	}
	if jsonp_callback != "" {
		out.Write([]byte(");"))
	}
	out.Write([]byte("\n"))
	flushResponse(out)
	return true
}

func (a *ApiServer) ServeHTTP(out http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "POST" {
		out.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if req.URL.Path == "/" {
		UIHandler(out, req)
		return
	} else if req.URL.Path == "/useradd" {
		a.UseraddHandler(out, req)
		return
	} else if req.URL.Path != "/api/events" {
		out.WriteHeader(http.StatusNotFound)
		out.Write([]byte("Nothing to see here. " +
			"The cool stuff is happening at /api/events"))
		return
	}

	req.ParseForm()
	cb := req.Form.Get("callback")
	if cb == "" {
		out.Header()["Content-Type"] = []string{"application/json"}
	} else {
		out.Header()["Content-Type"] = []string{"application/javascript"}
	}

	// Make browsers happy.
	allowOrigin := req.Header.Get("Origin")
	if allowOrigin == "" {
		allowOrigin = "*"
	}
	out.Header()["Access-Control-Allow-Origin"] = []string{allowOrigin}

	for _, event := range a.getHistory() {
		if !event.writeJSONEvent(out, cb) {
			break
		}
	}
	flushResponse(out)

	// TODO: for JSONP, do we essentially have to close the connection after
	// we emit an event, otherwise the browser never knows when things
	// finish ?
	appEvents := make(AppEventChannel, 3)
	a.bus.Subscribe(appEvents)
	for {
		event := <-appEvents
		if !JsonEventFromAppEvent(event).writeJSONEvent(out, cb) {
			break
		}
	}
	a.bus.Unsubscribe(appEvents)
}

func UIHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found", 404)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	rootTempl := template.Must(template.ParseFiles("static/index.htm"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rootTempl.Execute(w, r.Host)
}

func (a *ApiServer) UseraddHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/useradd" {
		http.Error(w, "Not found", 404)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		fmt.Fprintf(w, "invalid form %s", err)
	}
	// Look up onetime pass
	passkey := r.Form.Get("passkey")
	if passkey == "" {
		fmt.Fprintf(w, "Must submit passkey\n")
		return
	}
	onetime, err := a.auth.GetOnetime(passkey)
	if err != nil {
		fmt.Fprintf(w, "Failed, %s\n", err)
		return
	}

	// Validate input fields
	name := r.Form.Get("name")
	if name == "" {
		fmt.Fprintf(w, "Must submit name\n")
		return
	}
	contactInfo := r.Form.Get("contactInfo")
	if contactInfo == "" {
		fmt.Fprintf(w, "Must submit email\n")
		return
	}
	levelStr := r.Form.Get("userType")
	if levelStr == "" {
		fmt.Fprintf(w, "Must submit user type\n")
		return
	}
	if !isValidLevel(levelStr) {
		fmt.Fprintf(w, "level [%s] is not valid\n", levelStr)
		return
	}
	level := Level(levelStr)

	// Update User
	rfid := onetime.rfid
	user := a.auth.FindUser(rfid)
	if user == nil {
		fmt.Fprintf(w, "passkey [%s] not mapped to user, try again\n", onetime)
		return
	}
	updateUser := func(u *User) bool {
		u.ContactInfo = contactInfo
		u.Name = name
		u.UserLevel = level
		// Write user without code expiration
		u.ValidFrom = time.Time{}
		u.ValidTo = time.Time{}
		return true
	}
	a.auth.UpdateUser(onetime.authUserCode, onetime.rfid, updateUser)
	fmt.Fprintf(w, "Successfully added %s as a %s\n", r.Form["contactInfo"], r.Form["userType"])
}
