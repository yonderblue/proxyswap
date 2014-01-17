package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func handler(w http.ResponseWriter, r *http.Request) {
	time.Sleep(time.Second * 1)
	fmt.Fprintf(w, "Hi there good looking ! Time is %v\n", time.Now())
	w.Header().Set("Connection", "close") //in case the request was using http keep alive, still close it so our swap will not take a long time
}

func main() {
	http.HandleFunc("/", handler)
	if err := http.ListenAndServe(":8881", nil); err != nil {
		log.Panicln(err)
	}
}
