package main

import (
	"log"

	"github.com/thedivinez/grandaviator/server"
)

func main() {
	if server, err := server.NewServer(); err != nil {
		log.Fatal("failed to create new server: ", err)
	} else {
		if err := server.Start(); err != nil {
			log.Fatal("failed run app: ", err)
		}
	}
}
