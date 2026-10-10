// mapexport draws the bot's map (what it surveyed, its finds, its flights) into one HTML page.
//
//	go run ./cmd/mapexport                         # data/places.json -> data/map.html
//	go run ./cmd/mapexport -in data/places.json -out map.html
//	go run ./cmd/mapexport -xaero <xaero/minimap/Multiplayer_server>   # also home and finds as Xaero
//	                                                                    # waypoints (the game closed)
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/alteralto/persona-agent/internal/explore"
	"github.com/alteralto/persona-agent/internal/worldmap"
)

func main() {
	in := flag.String("in", "data/places.json", "the agent's places file")
	out := flag.String("out", "data/map.html", "the page to write")
	xaero := flag.String("xaero", "", "a server's folder of Xaero waypoints to put home and the finds into")
	flag.Parse()
	p, err := explore.LoadPlaces(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	page, err := worldmap.Render(p, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, page, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
	if *xaero != "" {
		n, err := worldmap.WriteXaero(*xaero, p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("xaero waypoint files written:", n)
	}
}
