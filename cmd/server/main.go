package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"nhooyr.io/websocket"
)

func handleWebSocket(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("Failed to upgrade connection: %v", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "")

	log.Println("New WebSocket connection established")

	ctx := context.Background()
	for {
		_, message, err := conn.Read(ctx)
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}
		log.Printf("Received message: %s", message)

		err = conn.Write(ctx, websocket.MessageText, []byte("Echo: "+string(message)))
		if err != nil {
			log.Printf("WebSocket write error: %v", err)
			break
		}
	}
}

func main() {
	r := gin.Default()

	r.LoadHTMLGlob("web/templates/*")

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"service": "whotalkie",
		})
	})

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "dashboard.html", gin.H{
			"title": "WhoTalkie Dashboard",
		})
	})

	r.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "WhoTalkie PTT Server",
			"version": "0.1.0",
		})
	})

	r.GET("/ws", handleWebSocket)

	log.Println("Starting WhoTalkie server on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}