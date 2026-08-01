package main

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade error:", err)
		return
	}
	defer conn.Close()

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			break
		}

		log.Printf("received: %s", message)

		if err := conn.WriteMessage(messageType, message); err != nil {
			log.Println("write error:", err)
			break
		}
	}
}

func main() {
	addr := ":8080"
	http.HandleFunc("/", echoHandler)

	log.Printf("websocket echo server listening on %s/", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("server error:", err)
	}
}
