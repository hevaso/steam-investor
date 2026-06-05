package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
)

type ChartPoint struct {
	Price float64 `json:"price"`
	Ema   float64 `json:"ema"`
}

type MarketTickResponse struct {
	Price  float64 `json:"price"`
	Ema    float64 `json:"ema"`
	Signal string  `json:"signal"`
}

var rawHistory = []float64{
	1.338, 1.341, 1.336, 1.320, 1.396, 1.444, 1.484, 1.390, 1.372, 1.394,
}

var currentAssetPrices = map[string][]ChartPoint{
	"dead-hand": {},
	"valkyrie":  {},
}

func calculateInitialEma(prices []float64) []ChartPoint {
	var points []ChartPoint
	if len(prices) == 0 {
		return points
	}
	ema := prices[0]
	points = append(points, ChartPoint{Price: prices[0], Ema: ema})
	alpha := 2.0 / (20.0 + 1.0)
	for i := 1; i < len(prices); i++ {
		ema = (prices[i] * alpha) + (ema * (1.0 - alpha))
		points = append(points, ChartPoint{Price: prices[i], Ema: ema})
	}
	return points
}

func enableCORS(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	(*w).Header().Set("Access-Control-Allow-Headers", "Content-Type")
	(*w).Header().Set("Content-Type", "application/json")
}

func tickHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(&w)
	if r.Method == "OPTIONS" {
		return
	}

	asset := r.URL.Query().Get("asset")
	if asset == "" {
		asset = "dead-hand"
	}

	points := currentAssetPrices[asset]
	if len(points) == 0 {
		multiplier := 1.0
		if asset == "valkyrie" {
			multiplier = 5.0
		}
		var initialPrices []float64
		for _, p := range rawHistory {
			initialPrices = append(initialPrices, p*multiplier)
		}
		points = calculateInitialEma(initialPrices)
		currentAssetPrices[asset] = points
	}

	lastPoint := points[len(points)-1]
	nextPrice := lastPoint.Price

	change := (rand.Float64() - 0.49) * 0.05
	if asset == "valkyrie" {
		change = (rand.Float64() - 0.49) * 0.25
	}
	nextPrice += change
	if nextPrice < 0.5 {
		nextPrice = 0.5
	}

	alpha := 2.0 / (20.0 + 1.0)
	nextEma := (nextPrice * alpha) + (lastPoint.Ema * (1.0 - alpha))

	nextPrice = math.Round(nextPrice*100) / 100
	nextEma = math.Round(nextEma*100) / 100

	points = append(points, ChartPoint{Price: nextPrice, Ema: nextEma})
	if len(points) > 50 {
		points = points[1:]
	}
	currentAssetPrices[asset] = points

	signal := "WAIT"
	if nextPrice > nextEma {
		signal = "BUY"
	} else if nextPrice < nextEma {
		signal = "SELL"
	}

	response := MarketTickResponse{
		Price:  nextPrice,
		Ema:    nextEma,
		Signal: signal,
	}

	json.NewEncoder(w).Encode(response)
}

func historyHandler(w http.ResponseWriter, r *http.Request) {
	enableCORS(&w)
	if r.Method == "OPTIONS" {
		return
	}

	asset := r.URL.Query().Get("asset")
	if asset == "" {
		asset = "dead-hand"
	}

	points := currentAssetPrices[asset]
	json.NewEncoder(w).Encode(points)
}

func main() {
	rand.Seed(time.Now().UnixNano())
	http.HandleFunc("/api/tick", tickHandler)
	http.HandleFunc("/api/history", historyHandler)
	fmt.Println("Engine successfully running on port 8080...")
	http.ListenAndServe("127.0.0.1:8080", nil)
}
